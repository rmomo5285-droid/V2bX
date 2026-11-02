package task

import (
	"errors"
	"sync"
	"time"
)

type taskRun struct {
	stop chan struct{}
	done chan struct{}
}

type Task struct {
	Interval time.Duration
	Execute  func() error
	access   sync.Mutex
	execute  sync.Mutex
	run      *taskRun
}

func (t *Task) Start(first bool) error {
	t.access.Lock()
	defer t.access.Unlock()
	if t.run != nil {
		return nil
	}
	if t.Interval <= 0 || t.Execute == nil {
		return errors.New("task requires a positive interval and an execute function")
	}
	run := &taskRun{stop: make(chan struct{}), done: make(chan struct{})}
	t.run = run
	interval, execute := t.Interval, t.Execute
	go func() {
		defer close(run.done)
		defer func() {
			t.access.Lock()
			defer t.access.Unlock()
			// An old generation must never close a later Start's channel.
			if t.run == run {
				t.run = nil
				close(run.stop)
			}
		}()
		invoke := func() bool {
			// Callbacks may close themselves, so Close cannot wait for them.
			// Serialize restarted generations until the old callback returns.
			t.execute.Lock()
			defer t.execute.Unlock()
			select {
			case <-run.stop:
				return false
			default:
				return execute() == nil
			}
		}
		if first && !invoke() {
			return
		}
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-run.stop:
				return
			case <-timer.C:
				if !invoke() {
					return
				}
				timer.Reset(interval)
			}
		}
	}()
	return nil
}

func (t *Task) Close() {
	t.access.Lock()
	defer t.access.Unlock()
	if t.run != nil {
		close(t.run.stop)
		t.run = nil
	}
}
