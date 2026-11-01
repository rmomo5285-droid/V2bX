package panel

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-resty/resty/v2"
)

func TestAliveHTTPFailureIsReturned(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer s.Close()
	c := &Client{client: resty.New().SetBaseURL(s.URL)}
	data := map[int][]string{1: {"198.51.100.1"}}
	if err := c.ReportNodeOnlineUsers(&data); err == nil {
		t.Fatal("alive HTTP 500 silently reported as success")
	}
}
