package dispatcher

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/InazumaV/V2bX/limiter"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/transport"
)

// Track both halves: a client can finish uploading while a long download is
// still active. Mux may return from Dispatch before its asynchronous flow ends.
func trackLink(ctx context.Context, link *transport.Link, device *limiter.DeviceLease) {
	if device == nil {
		return
	}
	stop := context.AfterFunc(ctx, device.Close)
	var remaining atomic.Int32
	remaining.Store(2)
	finish := func() {
		if remaining.Add(-1) == 0 {
			stop()
			device.Close()
		}
	}
	link.Reader = &onlineReader{Reader: link.Reader, finish: finish}
	link.Writer = &onlineWriter{Writer: link.Writer, finish: finish}
}

type onlineReader struct {
	buf.Reader
	once   sync.Once
	finish func()
}

func (r *onlineReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := r.Reader.ReadMultiBuffer()
	if err != nil {
		r.once.Do(r.finish)
	}
	return mb, err
}

// Keep the optional timeout read used by proxy handshakes. A timeout is not
// EOF: it must not release a still-live upload half or its device lease.
func (r *onlineReader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	reader, ok := r.Reader.(buf.TimeoutReader)
	if !ok {
		return nil, buf.ErrNotTimeoutReader
	}
	mb, err := reader.ReadMultiBufferTimeout(timeout)
	if err != nil && err != buf.ErrReadTimeout && err != buf.ErrNotTimeoutReader {
		r.once.Do(r.finish)
	}
	return mb, err
}
func (r *onlineReader) Interrupt()   { r.once.Do(r.finish); common.Interrupt(r.Reader) }
func (r *onlineReader) Close() error { r.once.Do(r.finish); return common.Close(r.Reader) }

type onlineWriter struct {
	buf.Writer
	once   sync.Once
	finish func()
}

// Online tracking observes completion, not the payload; keep the underlying
// per-chunk user counter available to the core's zero-copy path.
func (w *onlineWriter) SpliceCounter() stats.Counter {
	if counted, ok := w.Writer.(interface{ SpliceCounter() stats.Counter }); ok {
		return counted.SpliceCounter()
	}
	return nil
}

func (w *onlineWriter) Close() error { w.once.Do(w.finish); return common.Close(w.Writer) }
func (w *onlineWriter) Interrupt()   { w.once.Do(w.finish); common.Interrupt(w.Writer) }
