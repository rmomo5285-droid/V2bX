package hy2

import (
	"net"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/internal/testaccesslog"
	"github.com/InazumaV/V2bX/limiter"
	"go.uber.org/zap"
)

func TestNativeHy2AccessUploadAndConnectionLifetime(t *testing.T) {
	events := testaccesslog.Start(t)
	limiter.Init()
	l := limiter.AddLimiter("hy-report", &conf.LimitConfig{}, []panel.UserInfo{{Id: 42, Uuid: "u"}}, nil)
	l.NodeID = 3
	defer limiter.DeleteLimiter("hy-report")
	logger := &serverLogger{Tag: "hy-report", logger: zap.NewNop()}
	addr := &net.UDPAddr{IP: net.ParseIP("198.51.100.1"), Port: 12345}
	id1, id2 := newAuthID("u"), newAuthID("u")
	logger.Connect(addr, id1, 0)
	logger.Connect(addr, id2, 0)
	logger.TCPRequest(addr, id1, "example.com:443")
	logger.UDPRequest(addr, id2, 1, "[2001:db8::1]:443")
	for _, network := range []string{"tcp", "udp"} {
		e := testaccesslog.Next(t, events)
		if e.UserID != 42 || e.NodeID != 3 || e.Network != network || e.UserIP != "198.51.100.1" {
			t.Fatalf("wrong core event: %+v", e)
		}
	}
	for i := 0; i < 8; i++ {
		l.GetOnlineDevice()
		if len(l.ActiveDeviceSnapshot()[42]) != 1 {
			t.Fatal("long-lived QUIC client vanished")
		}
	}
	logger.Disconnect(addr, id1, nil)
	if len(l.ActiveDeviceSnapshot()[42]) != 1 {
		t.Fatal("one disconnect removed a second session")
	}
	// NAT rebinding does not change the connection identity.
	logger.Disconnect(&net.UDPAddr{IP: addr.IP, Port: 54321}, id2, nil)
	if len(l.ActiveDeviceSnapshot()) != 0 || len(logger.devices) != 0 {
		t.Fatal("disconnect retained online state")
	}
}

func TestSessionIdentityPreservesUserAccounting(t *testing.T) {
	limiter.Init()
	limiter.AddLimiter("hy-accounting", &conf.LimitConfig{}, []panel.UserInfo{{Id: 42, Uuid: "u"}}, nil)
	defer limiter.DeleteLimiter("hy-accounting")
	hook := &HookServer{Tag: "hy-accounting", logger: zap.NewNop()}
	for i := 0; i < 2; i++ {
		if !hook.LogTraffic(newAuthID("u"), 100, 200) {
			t.Fatal("internal session identity failed accounting")
		}
	}
	v, ok := hook.Counter.Load("hy-accounting")
	if !ok {
		t.Fatal("counter missing")
	}
	c := v.(*counter.TrafficCounter)
	if c.Len() != 1 || c.GetUpCount("u") != 200 || c.GetDownCount("u") != 400 {
		t.Fatal("per-session identities split user accounting")
	}
}

func TestRevokedUserTerminatesAuthenticatedTraffic(t *testing.T) {
	limiter.Init()
	user := panel.UserInfo{Id: 42, Uuid: "revoked"}
	l := limiter.AddLimiter("revocation", &conf.LimitConfig{}, []panel.UserInfo{user}, nil)
	defer limiter.DeleteLimiter("revocation")
	hook := &HookServer{Tag: "revocation", logger: zap.NewNop()}
	session := newAuthID(user.Uuid)
	if !hook.LogTraffic(session, 1, 2) {
		t.Fatal("valid user rejected")
	}
	l.UpdateUser("revocation", nil, []panel.UserInfo{user})
	if hook.LogTraffic(session, 1, 2) {
		t.Fatal("revoked QUIC session was allowed to continue")
	}
}
