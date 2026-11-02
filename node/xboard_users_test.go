package node

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
)

type revokeCore struct {
	reloadTestCore
	deleted  []panel.UserInfo
	failOnce bool
}

func (c *revokeCore) DelUsers(users []panel.UserInfo, _ string, _ *panel.NodeInfo) error {
	if c.failOnce {
		c.failOnce = false
		return errors.New("temporary core failure")
	}
	c.deleted = append(c.deleted, users...)
	return nil
}

func TestFailedUserApplyRetriesSameXboardSnapshot(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/config"):
			w.WriteHeader(http.StatusNotModified)
		case strings.HasSuffix(r.URL.Path, "/user"):
			requests++
			if r.Header.Get("If-None-Match") == "revoked" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", "revoked")
			fmt.Fprint(w, `{"users":[]}`)
		case strings.HasSuffix(r.URL.Path, "/alivelist"):
			fmt.Fprint(w, `{"alive":{}}`)
		}
	}))
	defer s.Close()
	api, err := panel.New(&conf.ApiConfig{APIHost: s.URL, NodeID: 1, NodeType: "hysteria2"})
	if err != nil {
		t.Fatal(err)
	}
	limiter.Init()
	users := []panel.UserInfo{{Id: 7, Uuid: "revoked"}}
	l := limiter.AddLimiter("retry-revoke", &conf.LimitConfig{}, users, nil)
	defer limiter.DeleteLimiter("retry-revoke")
	core := &revokeCore{failOnce: true}
	c := &Controller{server: core, apiClient: api, limiter: l, tag: "retry-revoke", userList: users,
		info: &panel.NodeInfo{Id: 1, Type: "hysteria2"}, Options: &conf.Options{}}
	c.nodeInfoMonitor()
	if len(c.userList) != 1 {
		t.Fatal("failed core update was committed")
	}
	c.nodeInfoMonitor()
	if requests != 2 || len(c.userList) != 0 || len(core.deleted) != 1 {
		t.Fatal("unchanged panel snapshot was not retried after core recovery")
	}
}

func TestXboardNodeCanStartWithoutEligibleUsers(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/config"):
			fmt.Fprint(w, `{"tls":0,"server_port":8443,"base_config":{"pull_interval":60,"push_interval":60}}`)
		case strings.HasSuffix(r.URL.Path, "/user"):
			fmt.Fprint(w, `{"users":[]}`)
		case strings.HasSuffix(r.URL.Path, "/alivelist"):
			fmt.Fprint(w, `{"alive":{}}`)
		}
	}))
	defer s.Close()
	api, err := panel.New(&conf.ApiConfig{APIHost: s.URL, NodeID: 1, NodeType: "vmess"})
	if err != nil {
		t.Fatal(err)
	}
	limiter.Init()
	core := &reloadTestCore{}
	c := NewController(core, api, &conf.Options{Name: "empty-start"})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if core.node == nil || len(c.userList) != 0 {
		t.Fatal("idle node was not started")
	}
}

func TestXboardEmptyUsersRevokesAllEvenWhenAliveFails(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/config"):
			w.WriteHeader(http.StatusNotModified)
		case strings.HasSuffix(r.URL.Path, "/user"):
			fmt.Fprint(w, `{"users":[]}`)
		case strings.HasSuffix(r.URL.Path, "/alivelist"):
			w.WriteHeader(503)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer s.Close()
	api, err := panel.New(&conf.ApiConfig{APIHost: s.URL, NodeID: 1, NodeType: "hysteria2"})
	if err != nil {
		t.Fatal(err)
	}
	limiter.Init()
	users := []panel.UserInfo{{Id: 7, Uuid: "revoked"}}
	l := limiter.AddLimiter("revoke", &conf.LimitConfig{}, users, map[int]int{7: 4})
	defer limiter.DeleteLimiter("revoke")
	core := &revokeCore{}
	c := &Controller{server: core, apiClient: api, limiter: l, tag: "revoke", userList: users,
		info: &panel.NodeInfo{Type: "hysteria2"}, aliveMap: map[int]int{7: 4}, Options: &conf.Options{}}
	if err := c.nodeInfoMonitor(); err != nil {
		t.Fatal(err)
	}
	if len(c.userList) != 0 || len(core.deleted) != 1 || core.deleted[0].Id != 7 {
		t.Fatalf("empty panel list did not revoke users: current=%v deleted=%v", c.userList, core.deleted)
	}
	if c.aliveMap[7] != 4 {
		t.Fatal("failed alive query changed last snapshot")
	}
}
