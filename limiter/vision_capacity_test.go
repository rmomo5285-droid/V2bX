//go:build sing && xray && hysteria2

package limiter_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/accesslog"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/InazumaV/V2bX/node"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	xcore "github.com/xtls/xray-core/core"
	xconf "github.com/xtls/xray-core/infra/conf"
)

type capacityFixture struct {
	backend                             vcore.Core
	client                              *xcore.Instance
	target                              *httptest.Server
	tag                                 string
	wire, pushed, pushes, polls, alives atomic.Int64
	lastPush                            atomic.Int64
	mu                                  sync.Mutex
	streamStop                          chan struct{}
	streaming                           atomic.Int64
	panelDevices                        map[int][]string
	logFault                            atomic.Bool
	logRequests, logAcknowledged        atomic.Int64
	logHeaderHangs, logBodyHangs        atomic.Int64
}

func startCapacityFixture(t *testing.T, users int, flow string) *capacityFixture {
	t.Helper()
	f := &capacityFixture{tag: "vision-capacity", panelDevices: make(map[int][]string)}
	// The mock panel and HTTPS target are deliberately in the same process.
	// cgroup/RSS numbers therefore include the load generator and fixtures.
	payload := bytes.Repeat([]byte{0x52}, 16*1024)
	f.target = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/probe" {
			w.Write(payload[:32])
			return
		}
		w.Header().Set("Content-Length", "1099511627776")
		f.mu.Lock()
		stop := f.streamStop
		f.mu.Unlock()
		f.streaming.Add(1)
		writing := true
		defer func() {
			if writing {
				f.streaming.Add(-1)
			}
		}()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := w.Write(payload); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-ticker.C:
			case <-stop:
				// Stop producing data without closing the TLS connection. The
				// client must drain data already accepted by the target socket
				// before cancellation can discard in-flight bytes.
				f.streaming.Add(-1)
				writing = false
				<-r.Context().Done()
				return
			case <-r.Context().Done():
				return
			}
		}
	}))
	f.target.Listener = &accountingListener{Listener: f.target.Listener, written: &f.wire}
	f.target.TLS = &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13}
	f.target.StartTLS()
	t.Cleanup(f.target.Close)
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	key, err := x509.MarshalPKCS8PrivateKey(f.target.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{certFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.target.TLS.Certificates[0].Certificate[0]}), keyFile: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	reserve, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	reserve.Close()
	var panelUsers []panel.UserInfo
	var outbounds []any
	pin := fmt.Sprintf("%x", sha256.Sum256(f.target.TLS.Certificates[0].Certificate[0]))
	for i := 0; i < users; i++ {
		uuid := fmt.Sprintf("00112233-4455-6677-8899-%012x", i+1)
		panelUsers = append(panelUsers, panel.UserInfo{Id: i + 1, Uuid: uuid})
		outbounds = append(outbounds, map[string]any{"tag": fmt.Sprintf("user-%d", i+1), "protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": port, "users": []any{map[string]any{"id": uuid, "encryption": "none", "flow": flow}}}}}, "streamSettings": map[string]any{"network": "tcp", "security": "tls", "tlsSettings": map[string]any{"serverName": "example.com", "pinnedPeerCertSha256": pin, "fingerprint": "chrome"}}})
	}
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/accesslog":
			request := f.logRequests.Add(1)
			decoder := json.NewDecoder(io.LimitReader(r.Body, 65537))
			count := int64(0)
			for {
				var event accesslog.Event
				if err := decoder.Decode(&event); err != nil {
					if err != io.EOF {
						t.Error(err)
					}
					break
				}
				if event.NodeID != 1 || event.UserID < 1 || event.UserID > users {
					t.Error("invalid real access event")
				}
				count++
			}
			if f.logFault.Load() {
				if request%2 == 0 {
					f.logBodyHangs.Add(1)
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				} else {
					f.logHeaderHangs.Add(1)
				}
				select {
				case <-r.Context().Done():
				case <-time.After(20 * time.Second):
				}
				return
			}
			f.logAcknowledged.Add(count)
		case strings.HasSuffix(r.URL.Path, "/config"):
			fmt.Fprintf(w, `{"server_port":%d,"network":"tcp","tls":1,"flow":%q,"base_config":{"pull_interval":1,"push_interval":1}}`, port, flow)
		case strings.HasSuffix(r.URL.Path, "/user"):
			f.polls.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"users": panelUsers})
		case strings.HasSuffix(r.URL.Path, "/alivelist"):
			fmt.Fprint(w, `{"alive":{}}`)
		case strings.HasSuffix(r.URL.Path, "/push"):
			var batch map[int][]int64
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			for uid, row := range batch {
				if uid < 1 || uid > users || len(row) != 2 {
					t.Error("invalid traffic row")
					continue
				}
				f.pushed.Add(row[1])
			}
			f.pushes.Add(1)
			f.lastPush.Store(time.Now().UnixNano())
			fmt.Fprint(w, `{"data":true}`)
		case strings.HasSuffix(r.URL.Path, "/alive"):
			var devices map[int][]string
			if err := json.NewDecoder(r.Body).Decode(&devices); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			f.mu.Lock()
			for uid, ips := range devices {
				if len(ips) == 0 {
					delete(f.panelDevices, uid)
				} else {
					f.panelDevices[uid] = ips
				}
			}
			f.mu.Unlock()
			f.alives.Add(1)
			fmt.Fprint(w, `{"data":true}`)
		default:
			fmt.Fprint(w, `{"data":true}`)
		}
	}))
	t.Cleanup(apiServer.Close)
	if os.Getenv("V2BX_CAPACITY_ACCESSLOG") == "timeout-recovery" {
		// Keep production queue, batch, flush and ten-second request defaults.
		if err := accesslog.Configure(accesslog.Config{Enabled: true, Endpoint: apiServer.URL + "/accesslog", Username: "load-test", Password: "local-test-only"}, ""); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(accesslog.Shutdown)
	}
	cfg := conf.CoreConfig{Type: "xray", XrayConfig: conf.NewXrayConfig()}
	cfg.XrayConfig.LogConfig.Level = "error"
	cfg.XrayConfig.LogConfig.AccessPath = "none"
	cfg.XrayConfig.OutboundConfigPath = filepath.Join(dir, "outbound.json")
	if err := os.WriteFile(cfg.XrayConfig.OutboundConfigPath, []byte(`[{"protocol":"freedom","tag":"test-loopback","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1/32"]}]}}]`), 0600); err != nil {
		t.Fatal(err)
	}
	f.backend, err = vcore.NewCore([]conf.CoreConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.backend.Close() })
	if err = f.backend.Start(); err != nil {
		t.Fatal(err)
	}
	api, err := panel.New(&conf.ApiConfig{APIHost: apiServer.URL, NodeID: 1, NodeType: "vless"})
	if err != nil {
		t.Fatal(err)
	}
	cert := conf.NewCertConfig()
	cert.CertMode, cert.CertDomain, cert.CertFile, cert.KeyFile = "file", "example.com", certFile, keyFile
	controller := node.NewController(f.backend, api, &conf.Options{Name: f.tag, ListenIP: "127.0.0.1", SendIP: "0.0.0.0", Core: "xray", XrayOptions: &conf.XrayOptions{DisableSniffing: true}, CertConfig: cert})
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controller.Close() })
	encoded, _ := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "error", "access": "none"}, "outbounds": outbounds})
	var source xconf.Config
	if err = json.Unmarshal(encoded, &source); err != nil {
		t.Fatal(err)
	}
	built, err := source.Build()
	if err != nil {
		t.Fatal(err)
	}
	f.client, err = xcore.New(built)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.client.Close() })
	if err = f.client.Start(); err != nil {
		t.Fatal(err)
	}
	return f
}

// Each probe is a real TLS-over-VLESS connection and emits through the Xray
// access hook. Four workers offer up to 50 short connections per second.
func (f *capacityFixture) probe(ctx context.Context, uid int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ctx = session.SetForcedOutboundTagToContext(ctx, fmt.Sprintf("user-%d", uid))
	destination, err := xnet.ParseDestination("tcp:" + f.target.Listener.Addr().String())
	if err != nil {
		return err
	}
	conn, err := xcore.Dial(ctx, f.client, destination)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	inner := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: "example.com", MinVersion: tls.VersionTLS13})
	if err := inner.HandshakeContext(ctx); err != nil {
		return err
	}
	if _, err := io.WriteString(inner, "GET /probe HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n"); err != nil {
		return err
	}
	response, err := http.ReadResponse(bufio.NewReader(inner), nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err == nil && !bytes.Equal(payload, bytes.Repeat([]byte{0x52}, 32)) {
		return fmt.Errorf("probe payload corrupted")
	}
	return err
}

// Reuse the same node across all rounds; no explicit GC or node restart occurs
// between phases. Distinct UUIDs prevent connections being mistaken for users.
func TestVisionCapacity(t *testing.T) {
	if os.Getenv("V2BX_CAPACITY") != "1" {
		t.Skip("opt-in localhost multi-user load")
	}
	users := stressInt(t, "V2BX_CAPACITY_USERS", 150, 300)
	streams := stressInt(t, "V2BX_CAPACITY_STREAMS", 4, 8)
	seconds := stressInt(t, "V2BX_CAPACITY_SECONDS", 120, 600)
	rounds := stressInt(t, "V2BX_CAPACITY_ROUNDS", 3, 5)
	idle := stressInt(t, "V2BX_CAPACITY_IDLE", 70, 180)
	logMode := os.Getenv("V2BX_CAPACITY_ACCESSLOG")
	if logMode != "" && logMode != "disabled" && logMode != "timeout-recovery" {
		t.Fatal("unsupported accesslog scenario")
	}
	withLogs := logMode == "timeout-recovery"
	t.Setenv("xray.buf.splice", os.Getenv("XRAY_BUF_SPLICE"))
	limiter.Init()
	restore := limiter.AllowLoopbackForTest()
	defer restore()
	f := startCapacityFixture(t, users, "xtls-rprx-vision")
	fixture := &proxyFixture{backend: f.backend, tag: f.tag}
	baseline := snapshotMemory(fixture, "baseline")
	var samples []map[string]any
	var completedProbes int64
	var failures atomic.Int64
	var errorMu sync.Mutex
	var firstErrors []string
	recordError := func(err error) {
		failures.Add(1)
		errorMu.Lock()
		defer errorMu.Unlock()
		if len(firstErrors) < 10 {
			firstErrors = append(firstErrors, err.Error())
		}
	}
	for round := 1; round <= rounds; round++ {
		start := time.Now()
		ctx, cancel := context.WithCancel(context.Background())
		stopStreams := make(chan struct{})
		f.mu.Lock()
		f.streamStop = stopStreams
		f.mu.Unlock()
		stopProbes := make(chan struct{})
		var probeWG sync.WaitGroup
		var wg sync.WaitGroup
		var ready, done atomic.Int64
		var probes atomic.Int64
		var recoveredSent uint64
		if withLogs {
			for worker := 0; worker < 4; worker++ {
				probeWG.Add(1)
				go func(worker int) {
					defer probeWG.Done()
					tick := time.NewTicker(80 * time.Millisecond)
					defer tick.Stop()
					for sequence := worker; ; sequence += 4 {
						select {
						case <-stopProbes:
							return
						case <-ctx.Done():
							return
						case <-tick.C:
						}
						if err := f.probe(ctx, sequence%users+1); err != nil {
							if ctx.Err() == nil {
								recordError(err)
							}
						} else {
							probes.Add(1)
						}
					}
				}(worker)
			}
		}
		admission := make(chan struct{}, 32)
		for uid := 1; uid <= users; uid++ {
			for stream := 0; stream < streams; stream++ {
				wg.Add(1)
				go func(uid int) {
					defer wg.Done()
					defer done.Add(1)
					admission <- struct{}{}
					dialCtx := session.SetForcedOutboundTagToContext(ctx, fmt.Sprintf("user-%d", uid))
					dest, err := xnet.ParseDestination("tcp:" + f.target.Listener.Addr().String())
					if err != nil {
						<-admission
						recordError(err)
						return
					}
					conn, err := xcore.Dial(dialCtx, f.client, dest)
					if err != nil {
						<-admission
						recordError(err)
						return
					}
					defer conn.Close()
					inner := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: "example.com", MinVersion: tls.VersionTLS13})
					handshakeCtx, stopHandshake := context.WithTimeout(ctx, 15*time.Second)
					err = inner.HandshakeContext(handshakeCtx)
					stopHandshake()
					if err != nil {
						<-admission
						recordError(err)
						return
					}
					stopClose := context.AfterFunc(ctx, func() { conn.Close() })
					defer stopClose()
					if _, err = io.WriteString(inner, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"); err != nil {
						<-admission
						recordError(err)
						return
					}
					response, err := http.ReadResponse(bufio.NewReader(inner), nil)
					<-admission
					if err != nil {
						recordError(err)
						return
					}
					ready.Add(1)
					buffer := make([]byte, 16*1024)
					for {
						n, err := response.Body.Read(buffer)
						if n > 0 && !bytes.Equal(buffer[:n], bytes.Repeat([]byte{0x52}, n)) {
							recordError(fmt.Errorf("payload corrupted"))
							return
						}
						if err != nil {
							if ctx.Err() == nil {
								recordError(err)
							}
							return
						}
					}
				}(uid)
			}
		}
		ticker := time.NewTicker(time.Second)
		for elapsed := 1; elapsed <= seconds; elapsed++ {
			<-ticker.C
			if withLogs && elapsed == seconds/4 {
				f.logFault.Store(true)
			}
			if withLogs && elapsed == seconds*3/4 {
				f.logFault.Store(false)
				recoveredSent = accesslog.CurrentStats().Sent
			}
			if elapsed == seconds || elapsed%10 == 0 {
				memory := snapshotMemory(fixture, fmt.Sprintf("round_%d_active_%ds", round, elapsed))
				l, _ := limiter.GetLimiter(f.tag)
				active := len(l.ActiveDeviceSnapshot())
				f.mu.Lock()
				panelActive := len(f.panelDevices)
				f.mu.Unlock()
				age := float64(0)
				if stamp := f.lastPush.Load(); stamp != 0 {
					age = time.Since(time.Unix(0, stamp)).Seconds()
				}
				sample := map[string]any{"round": round, "elapsed": elapsed, "ready_connections": ready.Load(), "live_users": active, "panel_alive_users": panelActive, "download_report_age_seconds": age, "wire": f.wire.Load(), "pushed": f.pushed.Load(), "pushes": f.pushes.Load(), "polls": f.polls.Load(), "memory": memory}
				if withLogs {
					stats := accesslog.CurrentStats()
					sample["accesslog"] = stats
					sample["accesslog_fault_active"] = f.logFault.Load()
					sample["completed_probe_connections"] = probes.Load()
					if stats.QueueDepth > 256 || stats.QueuedBytes > 256<<10 {
						t.Error("accesslog exceeded production queue bound")
					}
				}
				samples = append(samples, sample)
				data, _ := json.Marshal(sample)
				t.Log("CAPACITY_SAMPLE " + string(data))
				if elapsed >= 20 && (ready.Load() != int64(users*streams) || active != users) {
					t.Errorf("wrong live population: ready=%d users=%d", ready.Load(), active)
				}
				if elapsed >= 20 && (age > 10 || f.pushes.Load() == 0) {
					t.Errorf("traffic reporting stalled with live clients: age=%.1f", age)
				}
				if elapsed >= 70 && panelActive != users {
					t.Errorf("online heartbeat mismatch: %d vs %d", panelActive, users)
				}
			}
		}
		ticker.Stop()
		if withLogs {
			stats := accesslog.CurrentStats()
			// Throughput under one CPU may not fill the queue. Queue overflow is
			// tested deterministically in accesslog's outage regression test;
			// this integration scenario requires actual timeout and recovery.
			if seconds >= 70 && (stats.Uncertain == 0 || stats.Sent <= recoveredSent || f.logHeaderHangs.Load() == 0 || f.logBodyHangs.Load() == 0) {
				t.Errorf("accesslog fault/recovery not exercised: stats=%+v header_hangs=%d body_hangs=%d", stats, f.logHeaderHangs.Load(), f.logBodyHangs.Load())
			}
		}
		close(stopProbes)
		probeWG.Wait()
		close(stopStreams)
		deadline := time.Now().Add(12 * time.Second)
		for f.streaming.Load() != 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if f.streaming.Load() != 0 {
			t.Error("target writers did not quiesce")
		}
		completedProbes += probes.Load()
		allowance := int64(round*users*streams)*256 + completedProbes*256
		expected := f.wire.Load()
		deadline = time.Now().Add(12 * time.Second)
		for f.pushed.Load() < expected-allowance && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		quiescent := map[string]any{"round": round, "stage": "quiescent", "wire": expected, "pushed": f.pushed.Load(), "allowance": allowance, "streaming_writers": f.streaming.Load()}
		samples = append(samples, quiescent)
		data, _ := json.Marshal(quiescent)
		t.Log("CAPACITY_QUIESCENT " + string(data))
		if difference := expected - f.pushed.Load(); difference < 0 || difference > allowance {
			t.Errorf("live quiescent accounting mismatch: target=%d panel=%d allowance=%d", expected, f.pushed.Load(), allowance)
		}
		cancel()
		settled := make(chan struct{})
		go func() { wg.Wait(); close(settled) }()
		select {
		case <-settled:
		case <-time.After(20 * time.Second):
			t.Error("client shutdown blocked")
		}
		// Allow production half-close and heartbeat timers to run naturally.
		time.Sleep(time.Duration(idle) * time.Second)
		f.target.CloseClientConnections()
		// The source was drained while clients were still connected. Only TLS
		// close records can now be written without reaching the proxy.
		wire, pushed := f.wire.Load(), f.pushed.Load()
		if difference := wire - pushed; difference < 0 || difference > allowance {
			t.Errorf("settled accounting mismatch: target=%d panel=%d allowance=%d", wire, pushed, allowance)
		}
		memory := snapshotMemory(fixture, fmt.Sprintf("round_%d_idle", round))
		l, _ := limiter.GetLimiter(f.tag)
		active := len(l.ActiveDeviceSnapshot())
		f.mu.Lock()
		panelActive := len(f.panelDevices)
		f.mu.Unlock()
		idleSample := map[string]any{"round": round, "stage": "idle", "live_users": active, "panel_alive_users": panelActive, "memory": memory, "round_seconds": time.Since(start).Seconds(), "wire": f.wire.Load(), "pushed": f.pushed.Load()}
		if withLogs {
			stats := accesslog.CurrentStats()
			idleSample["accesslog"] = stats
			if idle >= 15 && (stats.QueueDepth != 0 || stats.QueuedBytes != 0) {
				t.Error("accesslog queue did not drain after recovery")
			}
		}
		samples = append(samples, idleSample)
		data, _ = json.Marshal(idleSample)
		t.Log("CAPACITY_IDLE " + string(data))
		if active != 0 || memory.ManagedLinks != 0 {
			t.Errorf("connections retained after disconnect: users=%d links=%d", active, memory.ManagedLinks)
		}
		if idle >= 65 && memory.Goroutines > baseline.Goroutines+64 {
			t.Errorf("goroutines retained after disconnect: baseline=%d idle=%d", baseline.Goroutines, memory.Goroutines)
		}
		if idle >= 65 && panelActive != 0 {
			t.Errorf("panel retained departed users: %d", panelActive)
		}
	}
	if failures.Load() != 0 {
		t.Errorf("load errors=%d first=%v", failures.Load(), firstErrors)
	}
	result := map[string]any{"os": runtime.GOOS, "scope": "clients, node, mock panel and HTTPS target share process and cgroup; no forced GC or restart between rounds; not a VPS capacity claim", "users": users, "connections_per_user": streams, "rounds": rounds, "seconds_per_round": seconds, "gomaxprocs": runtime.GOMAXPROCS(0), "splice": os.Getenv("XRAY_BUF_SPLICE"), "failures": failures.Load(), "first_errors": firstErrors, "baseline": baseline, "samples": samples}
	result["accesslog_scenario"] = logMode
	result["accesslog_requests"] = f.logRequests.Load()
	result["accesslog_header_hangs"] = f.logHeaderHangs.Load()
	result["accesslog_body_hangs"] = f.logBodyHangs.Load()
	data, _ := json.MarshalIndent(result, "", "  ")
	if path := os.Getenv("V2BX_CAPACITY_REPORT"); path != "" {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
