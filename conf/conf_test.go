package conf

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestConf_LoadFromPath(t *testing.T) {
	c := New()
	if err := c.LoadFromPath("../example/config.json"); err != nil {
		t.Fatalf("load example config: %v", err)
	}
}

func TestConf_Watch(t *testing.T) {
	data, err := os.ReadFile("../example/config.json")
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	c := New()
	reloaded := make(chan struct{})
	var once sync.Once
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.WatchContext(ctx, path, "", "", func(_ *Conf) {
		once.Do(func() { close(reloaded) })
	}); err != nil {
		t.Fatalf("watch config: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("update config: %v", err)
	}

	select {
	case <-reloaded:
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for config reload")
	}
}
