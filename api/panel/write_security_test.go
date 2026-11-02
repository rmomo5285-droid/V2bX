package panel

import (
	"fmt"
	"github.com/InazumaV/V2bX/conf"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestReportsRequireExplicitAcknowledgement(t *testing.T) {
	for _, body := range []string{`<html>login</html>`, `{}`, `{"data":false}`, `{"data":true} trailing`, `{"data":true}`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
			defer s.Close()
			c, err := New(&conf.ApiConfig{APIHost: s.URL, NodeID: 1, NodeType: "vmess"})
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseRequests()
			if err := c.ReportUserTraffic([]UserTraffic{{UID: 1, Upload: 5}}); (err == nil) != (body == `{"data":true}`) {
				t.Fatalf("ack %q: %v", body, err)
			}
		})
	}
}

func TestAmbiguousPostIsNotAutomaticallyRetried(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		requests.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer s.Close()
	c, err := New(&conf.ApiConfig{APIHost: s.URL, NodeID: 1, NodeType: "vmess"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseRequests()
	if err := c.ReportUserTraffic([]UserTraffic{{UID: 1, Upload: 5}}); err == nil {
		t.Fatal("lost acknowledgement reported as success")
	}
	if requests.Load() != 1 {
		t.Fatalf("non-idempotent POST sent %d times", requests.Load())
	}
}
