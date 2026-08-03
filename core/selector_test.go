package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
)

type selectorTestCore struct {
	installed    bool
	addErr       error
	cleanupErr   error
	panicOnAdd   bool
	panicOnClean bool
	deleteCalls  int
}

func (c *selectorTestCore) Start() error { return nil }
func (c *selectorTestCore) Close() error { return nil }
func (c *selectorTestCore) AddNode(string, *panel.NodeInfo, *conf.Options) error {
	c.installed = true
	if c.panicOnAdd {
		panic("injected add panic")
	}
	return c.addErr
}
func (c *selectorTestCore) DelNode(string) error {
	c.deleteCalls++
	if c.panicOnClean {
		panic("injected cleanup panic")
	}
	if c.cleanupErr == nil {
		c.installed = false
	}
	return c.cleanupErr
}
func (c *selectorTestCore) AddUsers(*AddUsersParams) (int, error) { return 0, nil }
func (c *selectorTestCore) GetUserTrafficSlice(string, bool) ([]panel.UserTraffic, error) {
	return nil, nil
}
func (c *selectorTestCore) DelUsers([]panel.UserInfo, string, *panel.NodeInfo) error {
	return nil
}
func (c *selectorTestCore) Protocols() []string { return []string{"test"} }
func (c *selectorTestCore) Type() string        { return "test" }

func TestSelectorAddNodeCleansPartialCoreState(t *testing.T) {
	for _, test := range []struct {
		name       string
		panicOnAdd bool
	}{
		{name: "returned error"},
		{name: "panic", panicOnAdd: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &selectorTestCore{
				addErr:     errors.New("injected add failure"),
				panicOnAdd: test.panicOnAdd,
			}
			selector := &Selector{cores: map[string]Core{"fake": backend}}
			err := selector.AddNode(
				"partial",
				&panel.NodeInfo{Type: "test"},
				&conf.Options{CoreName: "fake", Core: "test"},
			)
			if err == nil {
				t.Fatal("partial AddNode unexpectedly succeeded")
			}
			if backend.installed {
				t.Fatal("partially installed backend state was not removed")
			}
			if backend.deleteCalls != 1 {
				t.Fatalf("cleanup calls = %d, want 1", backend.deleteCalls)
			}
			if _, exists := selector.nodes.Load("partial"); exists {
				t.Fatal("failed node was published in selector")
			}
		})
	}
}

func TestSelectorAddNodeReportsCleanupFailure(t *testing.T) {
	backend := &selectorTestCore{
		addErr:     errors.New("injected add failure"),
		cleanupErr: errors.New("injected cleanup failure"),
	}
	selector := &Selector{cores: map[string]Core{"fake": backend}}
	err := selector.AddNode(
		"partial",
		&panel.NodeInfo{Type: "test"},
		&conf.Options{CoreName: "fake", Core: "test"},
	)
	if err == nil || !strings.Contains(err.Error(), "injected add failure") ||
		!strings.Contains(err.Error(), "injected cleanup failure") {
		t.Fatalf("error did not retain add and cleanup failures: %v", err)
	}
	if _, exists := selector.nodes.Load("partial"); exists {
		t.Fatal("failed node was published in selector")
	}
}

func TestSelectorAddNodePublishesOnlyAfterSuccess(t *testing.T) {
	backend := &selectorTestCore{}
	selector := &Selector{cores: map[string]Core{"fake": backend}}
	if err := selector.AddNode(
		"ready",
		&panel.NodeInfo{Type: "test"},
		&conf.Options{CoreName: "fake", Core: "test"},
	); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if selected, exists := selector.nodes.Load("ready"); !exists || selected != backend {
		t.Fatal("successful node was not published")
	}
	if backend.deleteCalls != 0 {
		t.Fatalf("successful AddNode triggered %d cleanup calls", backend.deleteCalls)
	}
}
