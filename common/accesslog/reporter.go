package accesslog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

type Event struct {
	Time     string `json:"time"`
	Network  string `json:"network"`
	NodeID   int    `json:"node_id"`
	UserID   int    `json:"user_id"`
	UserIP   string `json:"user_ip"`
	Host     string `json:"host"`
	DestIP   string `json:"dest_ip"`
	DestPort uint16 `json:"dest_port"`
}

func NewEvent(nodeID, uid int, source, network, target string) (Event, bool) {
	if uid <= 0 || nodeID <= 0 || int64(nodeID) > 2147483647 || network != "tcp" && network != "udp" || len(target) > 320 {
		return Event{}, false
	}
	ip, err := netip.ParseAddr(source)
	if err != nil {
		return Event{}, false
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return Event{}, false
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return Event{}, false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || len(host) > 253 {
		return Event{}, false
	}
	destIP := ""
	if addr, err := netip.ParseAddr(host); err == nil {
		host = addr.Unmap().String()
		destIP = host
	} else {
		for _, ch := range host {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '.' || ch == '_') {
				return Event{}, false
			}
		}
	}
	return Event{Time: time.Now().UTC().Format("2006-01-02 15:04:05"), Network: network, NodeID: nodeID, UserID: uid,
		UserIP: ip.Unmap().String(), Host: strings.Clone(host), DestIP: destIP, DestPort: uint16(port)}, true
}

type Stats struct {
	Enabled     bool      `json:"enabled"`
	UpdatedAt   time.Time `json:"updated_at"`
	QueueDepth  int       `json:"queue_depth"`
	QueuedBytes int       `json:"queued_bytes"`
	Accepted    uint64    `json:"accepted"`
	Sent        uint64    `json:"sent"`
	Dropped     uint64    `json:"dropped"`
	Failed      uint64    `json:"failed"`
	Uncertain   uint64    `json:"uncertain"`
	LastSuccess time.Time `json:"last_success"`
	LastStatus  int       `json:"last_http_status"`
}

type Reporter struct {
	cfg        Config
	queue      chan Event
	mu         sync.Mutex
	stats      Stats
	closed     bool
	stop       chan struct{}
	done       chan struct{}
	cancel     context.CancelFunc
	ctx        context.Context
	client     *http.Client
	statusPath string
}

var active atomic.Pointer[Reporter]

func Configure(cfg Config, statusPath string) error {
	cfg, err := cfg.Prepare()
	if err != nil {
		return err
	}
	var next *Reporter
	if cfg.Enabled {
		next = newReporter(cfg, statusPath)
	}
	old := active.Swap(next)
	if old != nil {
		old.Close()
	}
	if next != nil {
		writeStatus(statusPath, next.Snapshot())
	}
	if next == nil && statusPath != "" {
		writeStatus(statusPath, Stats{UpdatedAt: time.Now().UTC()})
	}
	return nil
}

func Enabled() bool { return active.Load() != nil }
func Record(event Event) {
	if r := active.Load(); r != nil {
		r.Try(event)
	}
}
func Shutdown() {
	if r := active.Swap(nil); r != nil {
		r.Close()
	}
}

func newReporter(cfg Config, statusPath string) *Reporter {
	ctx, cancel := context.WithCancel(context.Background())
	timeout, _ := time.ParseDuration(cfg.RequestTimeout)
	r := &Reporter{cfg: cfg, queue: make(chan Event, cfg.QueueCapacity), stop: make(chan struct{}), done: make(chan struct{}), ctx: ctx, cancel: cancel, statusPath: statusPath,
		client: &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil, MaxIdleConns: 1, MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: timeout},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	r.stats.Enabled = true
	go r.run()
	return r
}

func eventBytes(e Event) int {
	return 128 + len(e.Time) + len(e.Network) + len(e.UserIP) + len(e.Host) + len(e.DestIP)
}

// Try never waits for network I/O. Limits include both queue count and bytes.
func (r *Reporter) Try(e Event) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	size := eventBytes(e)
	if r.closed || size > 1024 || r.stats.QueuedBytes+size > r.cfg.QueueMaxBytes {
		r.stats.Dropped++
		return false
	}
	select {
	case r.queue <- e:
		r.stats.QueuedBytes += size
		r.stats.Accepted++
		return true
	default:
		r.stats.Dropped++
		return false
	}
}

func (r *Reporter) Snapshot() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats
	s.QueueDepth = len(r.queue)
	s.UpdatedAt = time.Now().UTC()
	return s
}

func (r *Reporter) Close() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		r.stats.Enabled = false
		close(r.stop)
	}
	r.mu.Unlock()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-r.done:
	case <-timer.C:
		r.cancel()
		<-r.done
	}
	r.client.CloseIdleConnections()
}

func writeStatus(path string, stats Stats) {
	if path == "" {
		return
	}
	b, _ := json.MarshalIndent(stats, "", "  ")
	// Fixed-size status, never access events or credentials. Use same-directory
	// rename so readers never observe a partial snapshot.
	f, err := os.CreateTemp(filepath.Dir(path), ".accesslog-status-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	closeErr := f.Close()
	if err == nil && closeErr == nil {
		_ = os.Rename(f.Name(), path)
	}
}

func (r *Reporter) run() {
	defer close(r.done)
	defer r.cancel()
	interval, _ := time.ParseDuration(r.cfg.FlushInterval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	status := time.NewTicker(30 * time.Second)
	defer status.Stop()
	batch := make([]byte, 0, r.cfg.BatchMaxBytes)
	count := 0
	flush := func() {
		if count == 0 {
			return
		}
		r.send(batch, count)
		clear(batch)
		batch = batch[:0]
		count = 0
	}
	appendEvent := func(e Event) {
		r.mu.Lock()
		r.stats.QueuedBytes -= eventBytes(e)
		r.mu.Unlock()
		line, _ := json.Marshal(e)
		if len(batch)+len(line)+1 > r.cfg.BatchMaxBytes {
			flush()
		}
		batch = append(batch, line...)
		batch = append(batch, '\n')
		count++
		if count >= r.cfg.BatchSize {
			flush()
		}
	}
	writeStatus(r.statusPath, r.Snapshot())
	defer func() { writeStatus(r.statusPath, r.Snapshot()) }()
	for {
		select {
		case e := <-r.queue:
			appendEvent(e)
		case <-ticker.C:
			flush()
		case <-status.C:
			s := r.Snapshot()
			writeStatus(r.statusPath, s)
			if s.Failed+s.Dropped+s.Uncertain > 0 {
				log.WithFields(log.Fields{"sent": s.Sent, "dropped": s.Dropped, "failed": s.Failed, "uncertain": s.Uncertain, "queued": s.QueueDepth, "http_status": s.LastStatus}).Warn("Access log delivery status")
			}
		case <-r.stop:
			for {
				select {
				case e := <-r.queue:
					appendEvent(e)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (r *Reporter) send(body []byte, count int) {
	req, err := http.NewRequestWithContext(r.ctx, http.MethodPost, r.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		r.mu.Lock()
		r.stats.Failed += uint64(count)
		r.mu.Unlock()
		return
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("User-Agent", "V2bX/accesslog")
	req.Header.Set("X-ClickHouse-User", r.cfg.Username)
	req.Header.Set("X-ClickHouse-Key", r.cfg.Password)
	response, err := r.client.Do(req)
	if err != nil {
		r.mu.Lock()
		r.stats.Uncertain += uint64(count)
		r.stats.LastStatus = 0
		r.mu.Unlock()
		return
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stats.LastStatus = response.StatusCode
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		r.stats.Failed += uint64(count)
		return
	}
	if readErr != nil || len(bytes.TrimSpace(data)) != 0 || response.Header.Get("X-ClickHouse-Exception-Code") != "" {
		r.stats.Uncertain += uint64(count)
		return
	}
	r.stats.Sent += uint64(count)
	r.stats.LastSuccess = time.Now().UTC()
}
