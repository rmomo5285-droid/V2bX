package dispatcher

import (
	"context"
	sync "sync"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

type ManagedWriter struct {
	writer  buf.Writer
	manager *LinkManager
	stop    func() bool
	abort   func()
}

func (w *ManagedWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	return w.writer.WriteMultiBuffer(mb)
}

func (w *ManagedWriter) Close() error {
	if w.stop != nil {
		w.stop()
	}
	w.manager.RemoveWriter(w)
	return common.Close(w.writer)
}

// One manager per user, including simultaneous first connections. Load then
// Store can orphan a manager and make some connections impossible to revoke.
func (d *DefaultDispatcher) manageLink(ctx context.Context, user string, writer buf.Writer, reader buf.Reader, abort func()) *ManagedWriter {
	v, _ := d.LinkManagers.LoadOrStore(user, &LinkManager{links: make(map[*ManagedWriter]buf.Reader)})
	lm := v.(*LinkManager)
	w := &ManagedWriter{writer: writer, manager: lm, abort: abort}
	// Some protocol writers do not implement Close. A finished inbound must
	// still release its registration instead of retaining its buffers forever.
	lm.mu.Lock()
	w.stop = context.AfterFunc(ctx, func() { lm.RemoveWriter(w) })
	lm.links[w] = reader
	lm.mu.Unlock()
	return w
}

func (w *ManagedWriter) Interrupt() { _ = w.Close() }

type LinkManager struct {
	links map[*ManagedWriter]buf.Reader
	mu    sync.Mutex
}

func (m *LinkManager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.links)
}

func (m *LinkManager) AddLink(writer *ManagedWriter, reader buf.Reader) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.links[writer] = reader
}

func (m *LinkManager) RemoveWriter(writer *ManagedWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.links, writer)
}

func (m *LinkManager) CloseAll() {
	m.mu.Lock()
	links := make(map[*ManagedWriter]buf.Reader, len(m.links))
	for w, r := range m.links {
		links[w] = r
	}
	m.mu.Unlock()
	for w, r := range links {
		if w.abort != nil {
			w.abort()
		}
		common.Close(w)
		common.Interrupt(r)
	}
}
