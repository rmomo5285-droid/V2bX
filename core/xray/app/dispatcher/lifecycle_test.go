package dispatcher

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
)

type terminalReader struct {
	timeout     time.Duration
	interrupted bool
	closed      bool
}

func (r *terminalReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	return buf.MultiBuffer{buf.FromBytes([]byte("tail"))}, io.EOF
}
func (r *terminalReader) ReadMultiBufferTimeout(d time.Duration) (buf.MultiBuffer, error) {
	r.timeout = d
	return r.ReadMultiBuffer()
}
func (r *terminalReader) Interrupt()   { r.interrupted = true }
func (r *terminalReader) Close() error { r.closed = true; return nil }

func TestCounterReaderPreservesTerminalPayloadTimeoutAndLifecycle(t *testing.T) {
	for _, timed := range []bool{false, true} {
		upstream := &terminalReader{}
		var count atomic.Int64
		reader := &CounterReader{Reader: upstream, Counter: &count}
		var mb buf.MultiBuffer
		var err error
		if timed {
			mb, err = reader.ReadMultiBufferTimeout(7 * time.Millisecond)
		} else {
			mb, err = reader.ReadMultiBuffer()
		}
		if err != io.EOF || mb.Len() != 4 || count.Load() != 4 {
			t.Fatalf("terminal payload lost: size=%d count=%d err=%v", mb.Len(), count.Load(), err)
		}
		buf.ReleaseMulti(mb)
		if timed && upstream.timeout != 7*time.Millisecond {
			t.Fatal("caller timeout ignored")
		}
		reader.Reader = &buf.TimeoutWrapperReader{Reader: upstream}
		reader.Interrupt()
		reader.Close()
		if !upstream.interrupted || !upstream.closed {
			t.Fatal("lifecycle did not reach actual reader")
		}
	}
}

type discardWriter struct{}

func (discardWriter) WriteMultiBuffer(mb buf.MultiBuffer) error { buf.ReleaseMulti(mb); return nil }

func TestConcurrentFirstLinksShareManagerAndRevokeEveryConnection(t *testing.T) {
	d := &DefaultDispatcher{}
	const n = 150
	var writers [n]*ManagedWriter
	var aborted atomic.Int32
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			writers[i] = d.manageLink(context.Background(), "same-user", discardWriter{}, &terminalReader{}, func() { aborted.Add(1) })
		}()
	}
	wg.Wait()
	manager := writers[0].manager
	for _, w := range writers {
		if w.manager != manager {
			t.Fatal("concurrent first link orphaned its manager")
		}
	}
	manager.CloseAll()
	if aborted.Load() != n || len(manager.links) != 0 {
		t.Fatalf("incomplete revocation: aborted=%d retained=%d", aborted.Load(), len(manager.links))
	}
}

func TestContextEndReleasesUnclosableProtocolWriter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &DefaultDispatcher{}
	writer := d.manageLink(ctx, "u", discardWriter{}, &terminalReader{}, nil)
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		writer.manager.mu.Lock()
		n := len(writer.manager.links)
		writer.manager.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("finished inbound retained managed writer")
		}
		time.Sleep(time.Millisecond)
	}
}
