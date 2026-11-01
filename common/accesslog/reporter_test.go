package accesslog

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig(endpoint string) Config {
	return Config{Enabled: true, Endpoint: endpoint, Username: "writer", Password: "test-secret", FlushInterval: "100ms", RequestTimeout: "200ms", BatchSize: 2}
}
func event(t *testing.T) Event {
	t.Helper()
	e, ok := NewEvent(3, 42, "::ffff:198.51.100.1", "tcp", "Example.COM.:443")
	if !ok {
		t.Fatal("invalid fixture")
	}
	return e
}
func awaitStats(t *testing.T, r *Reporter, predicate func(Stats) bool) Stats {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := r.Snapshot()
		if predicate(s) {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("stats condition not reached: %+v", r.Snapshot())
	return Stats{}
}

func TestBatchWireFormatAndConfiguredDestination(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/custom-ingest" || r.Method != "POST" || r.Header.Get("X-ClickHouse-Key") != "test-secret" || r.Header.Get("X-ClickHouse-User") != "writer" {
			t.Error("endpoint or authentication mismatch")
		}
		b, _ := io.ReadAll(r.Body)
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) != 2 {
			t.Errorf("batch lines=%d", len(lines))
		}
		for _, line := range lines {
			var got map[string]any
			if err := json.Unmarshal([]byte(line), &got); err != nil {
				t.Error(err)
			}
			if len(got) != 8 || got["host"] != "example.com" || got["user_id"] != float64(42) || got["user_ip"] != "198.51.100.1" {
				t.Errorf("wrong event: %v", got)
			}
		}
	}))
	defer s.Close()
	cfg, err := testConfig(s.URL + "/custom-ingest").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	r := newReporter(cfg, "")
	defer r.Close()
	r.Try(event(t))
	r.Try(event(t))
	awaitStats(t, r, func(s Stats) bool { return s.Sent == 2 })
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestQueueStaysBoundedDuringOutageAndShutdown(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}))
	defer s.Close()
	defer close(release)
	c := testConfig(s.URL)
	c.BatchSize = 1
	c.QueueCapacity = 4
	c.QueueMaxBytes = 4096
	cfg, _ := c.Prepare()
	r := newReporter(cfg, "")
	r.Try(event(t))
	<-entered
	start := time.Now()
	for i := 0; i < 10000; i++ {
		r.Try(event(t))
	}
	if time.Since(start) > time.Second {
		t.Fatal("producer blocked on network")
	}
	stats := r.Snapshot()
	if stats.QueueDepth > 4 || stats.QueuedBytes > 4096 || stats.Dropped == 0 {
		t.Fatalf("unbounded queue: %+v", stats)
	}
	r.Close()
	stats = r.Snapshot()
	if stats.QueueDepth != 0 || stats.QueuedBytes != 0 || stats.Uncertain == 0 {
		t.Fatalf("shutdown retained queue: %+v", stats)
	}
}

func TestDeliveryFailureClassificationAndNoBlindRetry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		code      int
		body      string
		uncertain bool
	}{{"bad_password", 403, "denied", false}, {"server_failure", 500, "failed", false}, {"late_clickhouse_exception", 200, "Code: 241 exception", true}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.code)
				io.WriteString(w, tc.body)
			}))
			defer s.Close()
			c := testConfig(s.URL)
			c.BatchSize = 1
			cfg, _ := c.Prepare()
			r := newReporter(cfg, "")
			defer r.Close()
			r.Try(event(t))
			st := awaitStats(t, r, func(s Stats) bool { return s.Failed+s.Uncertain == 1 })
			if (st.Uncertain == 1) != tc.uncertain || st.Sent != 0 {
				t.Fatalf("wrong outcome: %+v", st)
			}
			time.Sleep(150 * time.Millisecond)
			if calls.Load() != 1 {
				t.Fatal("uncertain batch was retried")
			}
		})
	}
}

func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer s.Close()
	c := testConfig(s.URL)
	c.BatchSize = 1
	cfg, _ := c.Prepare()
	r := newReporter(cfg, "")
	defer r.Close()
	r.Try(event(t))
	awaitStats(t, r, func(s Stats) bool { return s.Failed == 1 })
	if leaked.Load() {
		t.Fatal("followed redirect with write credentials")
	}
}

func TestConfigAndEventValidation(t *testing.T) {
	for _, endpoint := range []string{"http://example.com/ingest", "https://user:secret@example.com", "https://example.com/ingest?password=x", "not-a-url"} {
		c := testConfig(endpoint)
		if _, err := c.Prepare(); err == nil {
			t.Errorf("accepted %s", endpoint)
		}
	}
	c := testConfig("https://db.example.net:8443/custom")
	c.Backend = "clickhouse_http"
	c.Database = "custom_db"
	c.Table = "events"
	p, err := c.Prepare()
	if err != nil || !strings.Contains(p.Endpoint, "custom_db.events") {
		t.Fatalf("database/table not configurable: %v", err)
	}
	c.Table = "events; DROP TABLE x"
	if _, err := c.Prepare(); err == nil {
		t.Fatal("accepted SQL identifier injection")
	}
	e, ok := NewEvent(1, 2, "2001:db8::1", "udp", "[2001:db8::2]:443")
	if !ok || e.DestIP != "2001:db8::2" || e.Network != "udp" {
		t.Fatalf("IPv6: %+v", e)
	}
	if _, ok := NewEvent(1, 2, "198.51.100.1", "tcp", strings.Repeat("a", 300)+":443"); ok {
		t.Fatal("oversize host accepted")
	}
}
