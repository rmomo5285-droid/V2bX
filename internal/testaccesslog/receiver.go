// Package testaccesslog supplies a real HTTP receiver for core hook tests.
package testaccesslog

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/common/accesslog"
)

func Start(t *testing.T) <-chan accesslog.Event {
	t.Helper()
	events := make(chan accesslog.Event, 32)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-ClickHouse-Key") != "hook-test" {
			t.Error("missing writer authentication")
		}
		dec := json.NewDecoder(r.Body)
		for {
			var e accesslog.Event
			err := dec.Decode(&e)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Error(err)
				return
			}
			events <- e
		}
	}))
	if err := accesslog.Configure(accesslog.Config{Enabled: true, Endpoint: s.URL, Username: "writer", Password: "hook-test", BatchSize: 1}, ""); err != nil {
		s.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { accesslog.Shutdown(); s.Close() })
	return events
}

func Next(t *testing.T, events <-chan accesslog.Event) accesslog.Event {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("core did not upload an event")
	}
	return accesslog.Event{}
}
