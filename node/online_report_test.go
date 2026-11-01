package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
)

func TestOnlineHeartbeatContinuesAfterFailureAndClearsDepartedUID(t *testing.T) {
	var reports []map[int][]string
	fail := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[int][]string
		json.NewDecoder(r.Body).Decode(&body)
		reports = append(reports, body)
		if fail {
			w.WriteHeader(500)
		} else {
			w.Write([]byte(`{"data":true}`))
		}
	}))
	defer s.Close()
	api, err := panel.New(&conf.ApiConfig{APIHost: s.URL, NodeID: 1, NodeType: "hysteria2", Timeout: 1})
	if err != nil {
		t.Fatal(err)
	}
	limiter.Init()
	l := limiter.AddLimiter("heartbeat", &conf.LimitConfig{}, []panel.UserInfo{{Id: 7, Uuid: "u"}}, nil)
	defer limiter.DeleteLimiter("heartbeat")
	c := &Controller{server: &reloadTestCore{}, apiClient: api, limiter: l, Options: &conf.Options{}}
	d := l.AcquireDevice("heartbeat|u", "198.51.100.1")
	for i := 0; i < 7; i++ {
		if err := c.reportOnlineUsersTask(); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range reports {
		if len(r[7]) != 1 {
			t.Fatal("unchanged long connection was not refreshed")
		}
	}
	d.Close()
	fail = true
	c.reportOnlineUsersTask()
	if len(c.reportedOnline) != 1 {
		t.Fatal("failed report advanced acknowledged baseline")
	}
	fail = false
	c.reportOnlineUsersTask()
	last := reports[len(reports)-1]
	ips, ok := last[7]
	if !ok || len(ips) != 0 || len(c.reportedOnline) != 0 {
		t.Fatalf("departed UID not cleared: %v", last)
	}
}
