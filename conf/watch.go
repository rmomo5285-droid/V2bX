package conf

import (
	"context"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"log"
	"path/filepath"
	"time"
)

// Watch is retained for callers that own the Conf and do not need cancellation.
func (p *Conf) Watch(filePath, xDnsPath, sDnsPath string, reload func()) error {
	return p.WatchContext(context.Background(), filePath, xDnsPath, sDnsPath, func(next *Conf) {
		*p = *next
		reload()
	})
}

// WatchContext serializes reloads and stops delivering them during shutdown.
// Watch directories so editors that replace a file atomically remain supported.
func (p *Conf) WatchContext(ctx context.Context, filePath, xDnsPath, sDnsPath string, reload func(*Conf)) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("new watcher: %w", err)
	}
	watched := make(map[string]bool)
	dirs := make(map[string]bool)
	for _, path := range []string{filePath, xDnsPath, sDnsPath} {
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			watcher.Close()
			return err
		}
		watched[absolute] = true
		dirs[filepath.Dir(absolute)] = true
	}
	for dir := range dirs {
		if err := watcher.Add(dir); err != nil {
			watcher.Close()
			return fmt.Errorf("watch directory: %w", err)
		}
	}
	go func() {
		defer watcher.Close()
		var timer *time.Timer
		var ready <-chan time.Time
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				path, err := filepath.Abs(event.Name)
				if err != nil || !watched[path] || event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
					continue
				}
				if timer == nil {
					timer = time.NewTimer(5 * time.Second)
				} else {
					timer.Reset(5 * time.Second)
				}
				ready = timer.C
			case <-ready:
				ready = nil
				next := New()
				if err := next.LoadFromPath(filePath); err != nil {
					log.Printf("reload config rejected: %s", err)
					continue
				}
				if _, err := next.AccessLog.Prepare(); err != nil {
					log.Printf("reload access log rejected: %s", err)
					continue
				}
				if ctx.Err() == nil {
					reload(next)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("File watcher error: %s", err)
			}
		}
	}()
	return nil
}
