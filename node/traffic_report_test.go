package node

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
)

type trafficReportCore struct {
	reloadTestCore
	batches [][]panel.UserTraffic
	readErr error
}

func (c *trafficReportCore) GetUserTrafficSlice(_ string, reset bool) ([]panel.UserTraffic, error) {
	if !reset {
		return nil, errors.New("report did not reset counters")
	}
	if len(c.batches) == 0 {
		return nil, c.readErr
	}
	batch := c.batches[0]
	c.batches = c.batches[1:]
	return batch, c.readErr
}

func TestTrafficReportRetainsFailedBatchesAndMergesNewCounters(t *testing.T) {
	var reports []map[int][]int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/server/UniProxy/push" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var body map[int][]int64
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		reports = append(reports, body)
		if len(reports) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"data":true}`))
	}))
	defer s.Close()
	api, err := panel.New(&conf.ApiConfig{APIHost: s.URL, NodeID: 1, NodeType: "hysteria2", Timeout: 1})
	if err != nil {
		t.Fatal(err)
	}
	core := &trafficReportCore{batches: [][]panel.UserTraffic{
		{{UID: 7, Upload: 100, Download: 200}},
		{{UID: 7, Upload: 30, Download: 40}, {UID: 8, Upload: 5, Download: 9}},
	}}
	c := &Controller{server: core, apiClient: api, limiter: &limiter.Limiter{}, tag: "retry"}
	if err := c.reportUserTrafficTask(); err != nil {
		t.Fatal(err)
	}
	if len(c.pendingTraffic) != 1 {
		t.Fatal("failed batch lost")
	}
	if err := c.reportUserTrafficTask(); err != nil {
		t.Fatal(err)
	}
	if len(c.pendingTraffic) != 2 {
		t.Fatal("new counters not retained")
	}
	// A core read failure must not prevent retrying an already drained batch.
	core.readErr = errors.New("temporary core read failure")
	if err := c.reportUserTrafficTask(); err != nil {
		t.Fatal(err)
	}
	if len(c.pendingTraffic) != 0 {
		t.Fatal("acknowledged batch was not cleared")
	}
	if err := c.reportUserTrafficTask(); err != nil {
		t.Fatal(err)
	}
	want := []map[int][]int64{
		{7: {100, 200}},
		{7: {130, 240}, 8: {5, 9}},
		{7: {130, 240}, 8: {5, 9}},
	}
	if !reflect.DeepEqual(reports, want) {
		t.Fatalf("reports = %#v, want %#v", reports, want)
	}
}
