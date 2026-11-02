package task

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not finish the expected operation")
	}
}

func currentRun(ts *Task) *taskRun {
	ts.access.Lock()
	defer ts.access.Unlock()
	return ts.run
}

func TestStoppedCallbackCannotStopRestartedTask(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			entered, release, restarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			ts := &Task{Interval: time.Hour, Execute: func() error {
				if calls.Add(1) == 1 {
					close(entered)
					<-release
					if fail {
						return errors.New("old callback failed after Close")
					}
				} else {
					close(restarted)
				}
				return nil
			}}
			if err := ts.Start(true); err != nil {
				t.Fatal(err)
			}
			old := currentRun(ts)
			await(t, entered)
			ts.Close()
			ts.Close()
			if err := ts.Start(true); err != nil {
				t.Fatal(err)
			}
			defer ts.Close()
			next := currentRun(ts)
			close(release)
			await(t, old.done)
			await(t, restarted)
			if currentRun(ts) != next {
				t.Fatal("old callback replaced new generation")
			}
			select {
			case <-next.stop:
				t.Fatal("old callback stopped new generation")
			default:
			}
			ts.Close()
			await(t, next.done)
			if calls.Load() != 2 {
				t.Fatalf("unexpected callback count: %d", calls.Load())
			}
		})
	}
}

func TestCallbackCanCloseItselfAndReturnError(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	ts := &Task{Interval: time.Hour}
	ts.Execute = func() error {
		close(entered)
		<-release
		ts.Close()
		return errors.New("error after self-close")
	}
	if err := ts.Start(true); err != nil {
		t.Fatal(err)
	}
	run := currentRun(ts)
	await(t, entered)
	close(release)
	await(t, run.done)
	if currentRun(ts) != nil {
		t.Fatal("self-closed task still running")
	}
}

func TestStartRejectsInvalidTask(t *testing.T) {
	for _, ts := range []*Task{{}, {Interval: time.Second}, {Execute: func() error { return nil }}} {
		if ts.Start(false) == nil {
			t.Fatal("invalid task was accepted")
		}
	}
}
