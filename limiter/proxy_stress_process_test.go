//go:build sing && xray && hysteria2

package limiter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Keep the same client implementation and zero-error assertions, but give
// clients a separate Go scheduler and heap. Server and echo still share the
// parent's CPU quota, so this is not a remote-host capacity benchmark.
type stressClientRequest struct {
	Kind, UUID, Target, Mode, Report string
	Port, Concurrency, Size, Rate    int
	Duration                         time.Duration
}

func TestProxyStressClientProcess(t *testing.T) {
	if os.Getenv("V2BX_STRESS_CLIENT_HELPER") != "1" {
		t.Skip("internal opt-in subprocess helper")
	}
	var request stressClientRequest
	if err := json.Unmarshal([]byte(os.Getenv("V2BX_STRESS_CLIENT_REQUEST")), &request); err != nil {
		t.Fatal(err)
	}
	f := &proxyFixture{kind: request.Kind, uuid: request.UUID, target: request.Target, port: request.Port}
	result := runProxyStress(t, f, request.Mode, request.Concurrency, request.Size, request.Duration, request.Rate)
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.Report, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func runIsolatedProxyStress(t *testing.T, f *proxyFixture, mode string, concurrency, size int, duration time.Duration, rate int) stressResult {
	t.Helper()
	report := filepath.Join(t.TempDir(), "client-result.json")
	request, err := json.Marshal(stressClientRequest{f.kind, f.uuid, f.target, mode, report, f.port, concurrency, size, rate, duration})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration+45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProxyStressClientProcess$", "-test.timeout=2m")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "V2BX_STRESS_CLIENT_HELPER=") && !strings.HasPrefix(entry, "V2BX_STRESS_CLIENT_REQUEST=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "V2BX_STRESS_CLIENT_HELPER=1", "V2BX_STRESS_CLIENT_REQUEST="+string(request))
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	runtime.GC()
	baseline := snapshotMemory(f, "baseline_after_gc")
	peakHeap, peakGoroutines := baseline.HeapAlloc, baseline.Goroutines
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var upload, download int64
	drain := func() {
		rows, err := f.backend.GetUserTrafficSlice(f.tag, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.UID != 7 {
				t.Fatalf("unexpected UID %d", row.UID)
			}
			upload += row.Upload
			download += row.Download
		}
	}

wait:
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("client subprocess: %v; output: %s", err, output.String())
			}
			break wait
		case <-ticker.C:
			drain()
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			if memory.HeapAlloc > peakHeap {
				peakHeap = memory.HeapAlloc
			}
			if count := runtime.NumGoroutine(); count > peakGoroutines {
				peakGoroutines = count
			}
		}
	}
	time.Sleep(100 * time.Millisecond)
	drain()
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var result stressResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	result.ProcessModel = "separate-client-process-server-and-echo-in-parent"
	result.CountedUpload, result.CountedDownload = upload, download
	result.AccountingExact = upload == result.ExpectedBytesPerDirection && download == result.ExpectedBytesPerDirection
	result.HeapBeforeBytes, result.PeakHeapBytes = baseline.HeapAlloc, peakHeap
	result.GoroutinesBefore, result.PeakGoroutines = baseline.Goroutines, peakGoroutines
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	result.TotalAllocatedBytes = after.TotalAlloc - before.TotalAlloc
	result.MemorySnapshots = []memorySnapshot{baseline}
	return result
}
