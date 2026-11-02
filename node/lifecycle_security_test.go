package node

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/limiter"
	"sync/atomic"
	"testing"
	"time"
)

type closingCore struct {
	reloadTestCore
	entered chan struct{}
	release chan struct{}
	deleted atomic.Int32
}

func (c *closingCore) GetUserTrafficSlice(string, bool) ([]panel.UserTraffic, error) {
	close(c.entered)
	<-c.release
	return nil, nil
}
func (c *closingCore) DelNode(string) error { c.deleted.Add(1); return nil }

func TestCloseWaitsForCoreReadersAndPreventsLaterCallbacks(t *testing.T) {
	backend := &closingCore{entered: make(chan struct{}), release: make(chan struct{})}
	c := &Controller{server: backend, limiter: &limiter.Limiter{}, tag: "closing", nodeAdded: true}
	reported := make(chan struct{})
	go func() { c.reportUserTrafficTask(); close(reported) }()
	<-backend.entered
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("core deleted while a report was reading it")
	case <-time.After(50 * time.Millisecond):
	}
	if backend.deleted.Load() != 0 {
		t.Fatal("core deleted while in use")
	}
	close(backend.release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close blocked")
	}
	<-reported
	c.reportUserTrafficTask()
	c.reportOnlineUsersTask()
	c.nodeInfoMonitor()
	c.Close()
	if backend.deleted.Load() != 1 {
		t.Fatal("close was not idempotent")
	}
}
