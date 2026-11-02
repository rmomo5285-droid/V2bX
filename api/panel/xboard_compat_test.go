package panel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/InazumaV/V2bX/conf"
	"github.com/vmihailenco/msgpack/v5"
)

func TestXboardStaticNodeAuthenticationAndEndpoints(t *testing.T) {
	for _, mode := range []string{"legacy", "v2", "machine"} {
		t.Run(mode, func(t *testing.T) {
			prefix := "/api/v1/server/UniProxy/"
			if mode != "legacy" {
				prefix = "/api/v2/server/"
			}
			seen := make(map[string]bool)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get("token") != "test-key" || q.Get("node_id") != "9" {
					t.Error("missing Xboard auth parameters")
				}
				if mode == "legacy" && q.Get("node_type") != "hysteria2" {
					t.Error("legacy node type missing")
				}
				if mode != "legacy" && q.Has("node_type") {
					t.Error("v2 must not filter by legacy node type")
				}
				if mode == "machine" && q.Get("machine_id") != "3" {
					t.Error("missing machine id")
				}
				if !strings.HasPrefix(r.URL.Path, prefix) {
					t.Errorf("wrong endpoint: %s", r.URL.Path)
				}
				endpoint := strings.TrimPrefix(r.URL.Path, prefix)
				seen[endpoint] = true
				switch endpoint {
				case "config":
					fmt.Fprint(w, `{"server_port":443,"base_config":{"push_interval":60,"pull_interval":60}}`)
				case "user":
					fmt.Fprint(w, `{"users":[]}`)
				case "alivelist":
					fmt.Fprint(w, `{"alive":{}}`)
				default:
					fmt.Fprint(w, `{"data":true}`)
				}
			}))
			defer s.Close()
			cfg := &conf.ApiConfig{APIHost: s.URL, NodeID: 9, NodeType: "hysteria2", Key: "test-key"}
			if mode == "v2" {
				cfg.APIProtocol = "xboard-v2"
			}
			if mode == "machine" {
				cfg.MachineID = 3
			}
			c, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.GetNodeInfo(); err != nil {
				t.Fatal(err)
			}
			users, err := c.GetUserList()
			if err != nil || users == nil || len(users) != 0 {
				t.Fatalf("empty users response: %v, %v", users, err)
			}
			if _, err := c.GetUserAlive(); err != nil {
				t.Fatal(err)
			}
			if err := c.ReportUserTraffic([]UserTraffic{{UID: 1, Upload: 4}}); err != nil {
				t.Fatal(err)
			}
			alive := map[int][]string{1: {}}
			if err := c.ReportNodeOnlineUsers(&alive); err != nil {
				t.Fatal(err)
			}
			if len(seen) != 5 {
				t.Fatalf("endpoints exercised: %v", seen)
			}
		})
	}
}

func TestUserResponseValidationDoesNotAdvanceETag(t *testing.T) {
	for _, body := range []string{`{}`, `{"users":null}`, `{"users":[]`, `{"users":[]} {}`, `{"message":"users"}`, `{"users":[{"id":0,"uuid":"u"}]}`, `{"users":[{"id":1,"uuid":""}]}`, `{"users":[{"id":1,"uuid":"a"},{"id":1,"uuid":"b"}]}`, `{"users":[{"id":1,"uuid":"a"},{"id":2,"uuid":"a"}]}`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-None-Match") != "old" {
					t.Error("invalid response changed cached ETag")
				}
				w.Header().Set("ETag", "invalid")
				fmt.Fprint(w, body)
			}))
			defer s.Close()
			c, _ := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "hysteria2"})
			c.userEtag = "old"
			for i := 0; i < 2; i++ {
				if _, err := c.GetUserList(); err == nil {
					t.Fatal("invalid user response accepted")
				}
			}
		})
	}
}

func TestEmptyMsgpackUsersAndNotModifiedAreDistinct(t *testing.T) {
	first := true
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !first {
			if r.Header.Get("If-None-Match") != "empty" {
				t.Error("valid response did not save ETag")
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		first = false
		w.Header().Set("Content-Type", "application/x-msgpack")
		w.Header().Set("ETag", "empty")
		data, _ := msgpack.Marshal(UserListBody{Users: []UserInfo{}})
		w.Write(data)
	}))
	defer s.Close()
	c, _ := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "hysteria2"})
	users, err := c.GetUserList()
	if err != nil || users == nil || len(users) != 0 {
		t.Fatalf("empty MessagePack list: %v, %v", users, err)
	}
	users, err = c.GetUserList()
	if err != nil || users != nil {
		t.Fatalf("304 should be nil: %v, %v", users, err)
	}
}

func TestInvalidNodeConfigDoesNotPoisonValidators(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "old" {
			t.Error("invalid config updated ETag")
		}
		w.Header().Set("ETag", "bad")
		fmt.Fprint(w, `{"server_port":443}`) // missing base_config
	}))
	defer s.Close()
	c, _ := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "hysteria2"})
	c.nodeEtag = "old"
	for i := 0; i < 2; i++ {
		if _, err := c.GetNodeInfo(); err == nil {
			t.Fatal("invalid config was accepted or suppressed as unchanged")
		}
	}
	if c.responseBodyHash != "" {
		t.Fatal("invalid config updated body hash")
	}
}

func TestAliveFailurePreservesLastSnapshot(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer s.Close()
	c, _ := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "hysteria2"})
	c.AliveMap = &AliveMap{Alive: map[int]int{7: 4}}
	if _, err := c.GetUserAlive(); err == nil {
		t.Fatal("failed alive request treated as an empty snapshot")
	}
	if c.AliveMap.Alive[7] != 4 {
		t.Fatal("last known device count lost")
	}
}
