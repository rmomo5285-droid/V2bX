package sing

import (
	"context"
	"net"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/internal/testaccesslog"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type testPacketConn struct{ N.PacketConn }

func (*testPacketConn) Close() error { return nil }

func TestSingTCPAndUDPAccessUploadAndClose(t *testing.T) {
	events := testaccesslog.Start(t)
	limiter.Init()
	l := limiter.AddLimiter("sing-report", &conf.LimitConfig{}, []panel.UserInfo{{Id: 42, Uuid: "u"}}, nil)
	l.NodeID = 3
	defer limiter.DeleteLimiter("sing-report")
	hook := &HookServer{}
	m := adapter.InboundContext{Inbound: "sing-report", User: "u", Source: M.ParseSocksaddr("198.51.100.1:12345"), Destination: M.ParseSocksaddr("example.com:443")}
	a, b := net.Pipe()
	defer b.Close()
	tcp := hook.RoutedConnection(context.Background(), a, m, nil, nil)
	udp := hook.RoutedPacketConnection(context.Background(), &testPacketConn{}, m, nil, nil)
	for _, network := range []string{"tcp", "udp"} {
		e := testaccesslog.Next(t, events)
		if e.UserID != 42 || e.NodeID != 3 || e.Network != network || e.Host != "example.com" {
			t.Fatalf("wrong core event: %+v", e)
		}
	}
	for i := 0; i < 8; i++ {
		l.GetOnlineDevice()
		if len(l.ActiveDeviceSnapshot()[42]) != 1 {
			t.Fatal("sing flow vanished between snapshots")
		}
	}
	tcp.Close()
	if len(l.ActiveDeviceSnapshot()[42]) != 1 {
		t.Fatal("TCP close removed live UDP session")
	}
	udp.Close()
	if len(l.ActiveDeviceSnapshot()) != 0 {
		t.Fatal("closed sing flows retained")
	}
}
