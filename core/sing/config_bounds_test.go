package sing

import (
	"encoding/json"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
)

func TestServerPortRejectsWraparound(t *testing.T) {
	for _, value := range []string{"-1", "0", "65536", "4294967297", "invalid", ""} {
		if _, err := parseServerPort(value); err == nil {
			t.Fatalf("invalid port accepted: %q", value)
		}
	}
	for _, value := range []string{"1", "443", "65535"} {
		if _, err := parseServerPort(value); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInvalidWebsocketEarlyDataAndEmptyHTTPPath(t *testing.T) {
	c := &conf.Options{ListenIP: "127.0.0.1", SingOptions: &conf.SingOptions{}}
	for _, path := range []string{"/?ed=-1", "/?ed=4294967296", "/?ed=bad"} {
		network, _ := json.Marshal(map[string]string{"path": path})
		info := &panel.NodeInfo{Type: "vless", Common: &panel.CommonNode{ServerPort: 12345}, VAllss: &panel.VAllssNode{Network: "ws", NetworkSettings: network}}
		if _, err := getInboundOptions("test", info, c); err == nil {
			t.Fatalf("invalid early data accepted: %s", path)
		}
	}
	info := &panel.NodeInfo{Type: "vless", Common: &panel.CommonNode{ServerPort: 12345}, VAllss: &panel.VAllssNode{Network: "tcp", NetworkSettings: json.RawMessage(`{"header":{"type":"http","request":{"path":[]}}}`)}}
	if _, err := getInboundOptions("test", info, c); err == nil {
		t.Fatal("empty HTTP path accepted")
	}
}
