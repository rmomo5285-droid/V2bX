//go:build sing && xray && hysteria2

package limiter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/limiter"
	hyclient "github.com/apernet/hysteria/core/v2/client"
)

// An opt-in, bounded localhost workload. All processes, client traffic, echo
// service and cores share this Go process; these are not VPS bandwidth numbers.
type stressResult struct {
	Core                      string           `json:"core"`
	Round                     int              `json:"round"`
	Mode                      string           `json:"mode"`
	ProcessModel              string           `json:"process_model"`
	Concurrency               int              `json:"concurrency"`
	PayloadBytes              int              `json:"payload_bytes"`
	ChurnRateLimit            int              `json:"churn_rate_limit_per_second"`
	ChurnPacing               string           `json:"churn_pacing,omitempty"`
	SessionMode               string           `json:"session_mode"`
	SuccessfulQUICSessions    uint64           `json:"successful_quic_sessions"`
	ElapsedSeconds            float64          `json:"elapsed_seconds"`
	SuccessfulExchanges       uint64           `json:"successful_exchanges"`
	SuccessfulConnections     uint64           `json:"successful_connections"`
	Failures                  uint64           `json:"failures"`
	FirstErrors               []string         `json:"first_errors,omitempty"`
	RequestsPerSecond         float64          `json:"requests_per_second"`
	PayloadMbpsPerDirection   float64          `json:"payload_mbps_per_direction"`
	LatencyP50UpperMS         float64          `json:"latency_p50_upper_ms"`
	LatencyP95UpperMS         float64          `json:"latency_p95_upper_ms"`
	LatencyP99UpperMS         float64          `json:"latency_p99_upper_ms"`
	ExpectedBytesPerDirection int64            `json:"expected_bytes_per_direction"`
	CountedUpload             int64            `json:"counted_upload"`
	CountedDownload           int64            `json:"counted_download"`
	AccountingExact           bool             `json:"accounting_exact"`
	HeapBeforeBytes           uint64           `json:"heap_before_bytes"`
	PeakHeapBytes             uint64           `json:"peak_heap_bytes"`
	TotalAllocatedBytes       uint64           `json:"total_allocated_bytes"`
	GoroutinesBefore          int              `json:"goroutines_before"`
	PeakGoroutines            int              `json:"peak_goroutines"`
	GoroutinesAfterSettle     int              `json:"goroutines_after_settle"`
	SettleSeconds             float64          `json:"settle_seconds"`
	Settled                   bool             `json:"settled"`
	NodeClosed                bool             `json:"node_closed"`
	MemorySnapshots           []memorySnapshot `json:"memory_snapshots,omitempty"`
}

type memorySnapshot struct {
	Stage         string `json:"stage"`
	HeapAlloc     uint64 `json:"heap_alloc_bytes"`
	HeapInuse     uint64 `json:"heap_inuse_bytes"`
	HeapIdle      uint64 `json:"heap_idle_bytes"`
	HeapReleased  uint64 `json:"heap_released_bytes"`
	HeapObjects   uint64 `json:"heap_objects"`
	StackInuse    uint64 `json:"stack_inuse_bytes"`
	Sys           uint64 `json:"runtime_sys_bytes"`
	RSS           uint64 `json:"rss_bytes"`
	Goroutines    int    `json:"goroutines"`
	ActiveDevices int    `json:"active_devices"`
	NumGC         uint32 `json:"num_gc"`
}

func snapshotMemory(f *proxyFixture, stage string) memorySnapshot {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	devices := 0
	if l, err := limiter.GetLimiter(f.tag); err == nil {
		for _, ips := range l.ActiveDeviceSnapshot() {
			devices += len(ips)
		}
	}
	return memorySnapshot{stage, m.HeapAlloc, m.HeapInuse, m.HeapIdle, m.HeapReleased, m.HeapObjects, m.StackInuse, m.Sys, stressRSS(), runtime.NumGoroutine(), devices, m.NumGC}
}

func stressInt(t *testing.T, name string, fallback, maximum int) int {
	t.Helper()
	if value := os.Getenv(name); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > maximum {
			t.Fatalf("%s must be within 1..%d", name, maximum)
		}
		return n
	}
	return fallback
}

func TestProxyStress(t *testing.T) {
	if os.Getenv("V2BX_STRESS") != "1" {
		t.Skip("opt in with V2BX_STRESS=1; localhost only")
	}
	seconds := stressInt(t, "V2BX_STRESS_SECONDS", 8, 120)
	concurrency := stressInt(t, "V2BX_STRESS_CONCURRENCY", 32, 128)
	churnRate := stressInt(t, "V2BX_STRESS_CHURN_RPS", 1000, 5000)
	idleSeconds := 0
	rounds := stressInt(t, "V2BX_STRESS_ROUNDS", 1, 5)
	if os.Getenv("V2BX_STRESS_IDLE_SECONDS") != "" {
		idleSeconds = stressInt(t, "V2BX_STRESS_IDLE_SECONDS", 15, 300)
		if idleSeconds < 5 {
			t.Fatal("V2BX_STRESS_IDLE_SECONDS must be at least 5 to include the default TCP half-close timeout")
		}
	}
	limiter.Init()
	restore := limiter.AllowLoopbackForTest()
	defer restore()
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	var results []stressResult
	for _, kind := range []string{"xray", "sing", "hysteria2"} {
		for _, mode := range []string{"persistent", "churn"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := startProxyFixture(t, kind, echo.Addr().String())
				size := 64 * 1024
				if mode == "churn" {
					size = 4096
				}
				for round := 1; round <= rounds; round++ {
					r := runProxyStress(t, f, mode, concurrency, size, time.Duration(seconds)*time.Second, churnRate)
					r.Round = round
					if idleSeconds > 0 {
						idleStart := time.Now()
						r.MemorySnapshots = append(r.MemorySnapshots, snapshotMemory(f, "disconnected"))
						checkpoints := []int{idleSeconds}
						if idleSeconds > 5 {
							checkpoints = []int{5, idleSeconds}
						}
						if idleSeconds > 60 {
							checkpoints = []int{5, 60, idleSeconds}
						}
						if idleSeconds > 120 {
							checkpoints = []int{5, 60, 120, idleSeconds}
						}
						for _, seconds := range checkpoints {
							if wait := time.Duration(seconds)*time.Second - time.Since(idleStart); wait > 0 {
								time.Sleep(wait)
							}
							r.MemorySnapshots = append(r.MemorySnapshots, snapshotMemory(f, fmt.Sprintf("natural_idle_%ds", seconds)))
						}
						runtime.GC()
						r.MemorySnapshots = append(r.MemorySnapshots, snapshotMemory(f, "forced_gc_diagnostic"))
						debug.FreeOSMemory()
						r.MemorySnapshots = append(r.MemorySnapshots, snapshotMemory(f, "forced_os_release_diagnostic"))
						last := r.MemorySnapshots[len(r.MemorySnapshots)-1]
						if last.ActiveDevices != 0 {
							t.Errorf("devices still active after idle: %d", last.ActiveDevices)
						}
					}
					if round == rounds {
						f.shutdown()
						r.NodeClosed = true
					}
					settleStart := time.Now()
					for {
						r.GoroutinesAfterSettle = runtime.NumGoroutine()
						if r.GoroutinesAfterSettle <= r.GoroutinesBefore || time.Since(settleStart) >= 6*time.Second {
							break
						}
						time.Sleep(50 * time.Millisecond)
					}
					r.SettleSeconds = time.Since(settleStart).Seconds()
					r.Settled = r.GoroutinesAfterSettle <= r.GoroutinesBefore
					results = append(results, r)
					data, _ := json.Marshal(r)
					t.Log("STRESS_RESULT " + string(data))
					if r.Failures != 0 || r.SuccessfulExchanges == 0 {
						t.Errorf("workload failed: %d failures, errors=%v", r.Failures, r.FirstErrors)
					}
					if !r.AccountingExact {
						t.Errorf("counter mismatch: expected %d each, got %d/%d", r.ExpectedBytesPerDirection, r.CountedUpload, r.CountedDownload)
					}
					if !r.Settled {
						t.Errorf("goroutines did not return to baseline within 6s: before=%d after=%d", r.GoroutinesBefore, r.GoroutinesAfterSettle)
					}
				}
			})
		}
	}
	if path := os.Getenv("V2BX_STRESS_REPORT"); path != "" {
		document := struct {
			DateUTC     string         `json:"date_utc"`
			OS          string         `json:"os"`
			Arch        string         `json:"arch"`
			GoVersion   string         `json:"go_version"`
			LogicalCPUs int            `json:"logical_cpus"`
			GOMAXPROCS  int            `json:"gomaxprocs"`
			Scope       string         `json:"scope"`
			Results     []stressResult `json:"results"`
		}{time.Now().UTC().Format(time.RFC3339), runtime.GOOS, runtime.GOARCH, runtime.Version(), runtime.NumCPU(), runtime.GOMAXPROCS(0), "Single-process localhost clients, proxy and echo. VLESS TCP for Xray/sing; native Hysteria2 QUIC carrying TCP. No VPS, WAN or production-panel load. Latency histogram resolution 0.1ms; quantiles are upper bounds. Heap and RSS include clients and server; no CPU utilization measurement. Baseline GC precedes each round. Optional idle snapshots keep the node running; forced GC and OS release are diagnostic only. Repeated rounds reuse the same node and user; shutdown occurs after the last round.", results}
		data, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func runProxyStress(t *testing.T, f *proxyFixture, mode string, concurrency, size int, duration time.Duration, churnRate int) stressResult {
	t.Helper()
	if os.Getenv("V2BX_STRESS_ISOLATE_CLIENTS") == "1" && os.Getenv("V2BX_STRESS_CLIENT_HELPER") != "1" {
		return runIsolatedProxyStress(t, f, mode, concurrency, size, duration, churnRate)
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	r := stressResult{Core: f.kind, Mode: mode, Concurrency: concurrency, PayloadBytes: size, HeapBeforeBytes: before.HeapAlloc, PeakHeapBytes: before.HeapAlloc, GoroutinesBefore: runtime.NumGoroutine()}
	r.ProcessModel = "single-process-clients-server-echo"
	r.SessionMode = "new-transport-per-connection"
	reuseQUIC := f.kind == "hysteria2" && mode == "churn" && os.Getenv("V2BX_STRESS_FRESH_QUIC") != "1"
	if reuseQUIC {
		r.SessionMode = "one-quic-session-per-worker"
	}
	r.PeakGoroutines = r.GoroutinesBefore
	r.MemorySnapshots = append(r.MemorySnapshots, snapshotMemory(f, "baseline_after_gc"))
	if mode == "churn" {
		r.ChurnRateLimit = churnRate
		r.ChurnPacing = "global-ticker"
		if os.Getenv("V2BX_STRESS_BURST_CHURN") == "1" {
			r.ChurnPacing = "synchronized-workers"
		}
	}
	var histogram [50001]atomic.Uint64
	var successes, connections, failures atomic.Uint64
	var quicSessions atomic.Uint64
	var errorsMu sync.Mutex
	var firstErrors []string
	recordError := func(err error) {
		failures.Add(1)
		errorsMu.Lock()
		defer errorsMu.Unlock()
		if len(firstErrors) < 5 {
			firstErrors = append(firstErrors, err.Error())
		}
	}
	stopSample := make(chan struct{})
	sampleDone := make(chan struct{})
	go func() {
		defer close(sampleDone)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		drain := func() {
			if f.backend == nil { // Client-only subprocess; the parent owns counters.
				return
			}
			rows, err := f.backend.GetUserTrafficSlice(f.tag, true)
			if err != nil {
				recordError(err)
				return
			}
			for _, row := range rows {
				if row.UID != 7 {
					recordError(fmt.Errorf("unexpected UID %d", row.UID))
				}
				r.CountedUpload += row.Upload
				r.CountedDownload += row.Download
			}
		}
		for {
			select {
			case <-ticker.C:
				drain()
				var mem runtime.MemStats
				runtime.ReadMemStats(&mem)
				if mem.HeapAlloc > r.PeakHeapBytes {
					r.PeakHeapBytes = mem.HeapAlloc
				}
				if n := runtime.NumGoroutine(); n > r.PeakGoroutines {
					r.PeakGoroutines = n
				}
			case <-stopSample:
				drain()
				return
			}
		}
	}()
	var workers sync.WaitGroup
	startGate := make(chan struct{})
	start := time.Now()
	deadline := start.Add(duration)
	workContext, cancelWork := context.WithDeadline(context.Background(), deadline)
	defer cancelWork()
	var churnTick <-chan time.Time
	if mode == "churn" && r.ChurnPacing == "global-ticker" {
		// Share one admission clock. Per-worker sleeps synchronize workers into
		// repeated concurrency-sized handshake bursts instead of a paced rate.
		ticker := time.NewTicker(time.Second / time.Duration(churnRate))
		defer ticker.Stop()
		churnTick = ticker.C
	}
	for i := 0; i < concurrency; i++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			payload := bytes.Repeat([]byte{byte(worker + 1)}, size)
			reply := make([]byte, size)
			var connection net.Conn
			var session hyclient.Client
			defer func() {
				if connection != nil {
					connection.Close()
				}
				if session != nil {
					session.Close()
				}
			}()
			<-startGate
			for time.Now().Before(deadline) {
				if churnTick != nil {
					select {
					case <-workContext.Done():
						return
					case <-churnTick:
					}
					if time.Now().After(deadline) {
						return
					}
				}
				operationStart := time.Now()
				if connection == nil {
					var err error
					if reuseQUIC {
						if session == nil {
							session, err = f.newHysteriaClient()
							if err != nil {
								recordError(err)
								return
							}
							quicSessions.Add(1)
						}
						connection, err = session.TCP(f.target)
					} else {
						connection, err = f.dial()
						if err == nil && f.kind == "hysteria2" {
							quicSessions.Add(1)
						}
					}
					if err != nil {
						recordError(err)
						return
					}
					connections.Add(1)
				}
				connection.SetDeadline(time.Now().Add(5 * time.Second))
				n, err := connection.Write(payload)
				if err != nil {
					recordError(err)
					return
				}
				if n != len(payload) {
					recordError(io.ErrShortWrite)
					return
				}
				if _, err := io.ReadFull(connection, reply); err != nil {
					recordError(err)
					return
				}
				if !bytes.Equal(payload, reply) {
					recordError(fmt.Errorf("corrupt payload"))
					return
				}
				bucket := (time.Since(operationStart).Nanoseconds() + 99999) / 100000
				if bucket < 1 {
					bucket = 1
				}
				if bucket >= int64(len(histogram)) {
					bucket = int64(len(histogram) - 1)
				}
				histogram[bucket].Add(1)
				successes.Add(1)
				if mode == "churn" {
					connection.Close()
					connection = nil
					// Bound client ephemeral-port and certificate-handshake pressure.
					// This is a paced stability workload, not maximum connection capacity.
					if churnTick == nil {
						interval := time.Second * time.Duration(concurrency) / time.Duration(churnRate)
						if pause := interval - time.Since(operationStart); pause > 0 {
							time.Sleep(pause)
						}
					}
				}
			}
		}(i)
	}
	close(startGate)
	workers.Wait()
	r.ElapsedSeconds = time.Since(start).Seconds()
	// Let the final server-side write complete before the final counter drain.
	time.Sleep(100 * time.Millisecond)
	close(stopSample)
	<-sampleDone
	r.SuccessfulExchanges = successes.Load()
	r.SuccessfulConnections = connections.Load()
	r.SuccessfulQUICSessions = quicSessions.Load()
	r.Failures = failures.Load()
	r.FirstErrors = firstErrors
	r.ExpectedBytesPerDirection = int64(r.SuccessfulExchanges) * int64(size)
	r.AccountingExact = r.CountedUpload == r.ExpectedBytesPerDirection && r.CountedDownload == r.ExpectedBytesPerDirection
	r.RequestsPerSecond = float64(r.SuccessfulExchanges) / r.ElapsedSeconds
	r.PayloadMbpsPerDirection = float64(r.ExpectedBytesPerDirection) * 8 / r.ElapsedSeconds / 1e6
	quantile := func(percent uint64) float64 {
		want := (r.SuccessfulExchanges*percent + 99) / 100
		var count uint64
		for i := range histogram {
			count += histogram[i].Load()
			if count >= want {
				return float64(i) / 10
			}
		}
		return 5000
	}
	r.LatencyP50UpperMS = quantile(50)
	r.LatencyP95UpperMS = quantile(95)
	r.LatencyP99UpperMS = quantile(99)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	r.TotalAllocatedBytes = after.TotalAlloc - before.TotalAlloc
	return r
}
