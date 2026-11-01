package dispatcher

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/internal/testaccesslog"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

type testManager struct {
	outbound.Manager
	handler outbound.Handler
}

func (m *testManager) GetDefaultHandler() outbound.Handler { return m.handler }

type testHandler struct {
	outbound.Handler
	during func(*transport.Link)
}

func (*testHandler) Tag() string                                     { return "test" }
func (h *testHandler) Dispatch(_ context.Context, l *transport.Link) { h.during(l) }

func TestXrayRouteUploadsTCPAndUDPAndTracksHalfClose(t *testing.T) {
	events := testaccesslog.Start(t)
	limiter.Init()
	l := limiter.AddLimiter("xray-report", &conf.LimitConfig{}, []panel.UserInfo{{Id: 42, Uuid: "u"}}, nil)
	l.NodeID = 3
	defer limiter.DeleteLimiter("xray-report")
	for _, network := range []net.Network{net.Network_TCP, net.Network_UDP} {
		ctx := session.ContextWithInbound(context.Background(), &session.Inbound{Tag: "xray-report", User: &protocol.MemoryUser{Email: "xray-report|u"}, Source: net.TCPDestination(net.ParseAddress("198.51.100.1"), 12345)})
		ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{}})
		r, w := pipe.New()
		link := &transport.Link{Reader: r, Writer: w}
		h := &testHandler{during: func(link *transport.Link) {
			if len(l.ActiveDeviceSnapshot()[42]) != 1 {
				t.Fatal("route not tracked")
			}
			if _, err := link.Reader.(buf.TimeoutReader).ReadMultiBufferTimeout(time.Millisecond); err != buf.ErrReadTimeout {
				t.Fatalf("timeout handshake read not preserved: %v", err)
			}
			common.Close(link.Writer)
			if len(l.ActiveDeviceSnapshot()[42]) != 1 {
				t.Fatal("half close removed active opposite direction")
			}
			common.Interrupt(link.Reader)
		}}
		d := &DefaultDispatcher{ohm: &testManager{handler: h}}
		d.routedDispatch(ctx, link, net.Destination{Network: network, Address: net.ParseAddress("example.com"), Port: 443}, l, "")
		e := testaccesslog.Next(t, events)
		if e.Network != strings.ToLower(network.String()) || e.UserID != 42 || e.NodeID != 3 || e.Host != "example.com" {
			t.Fatalf("wrong core event: %+v", e)
		}
		if len(l.ActiveDeviceSnapshot()) != 0 {
			t.Fatal("completed route retained device")
		}
	}
}

var _ buf.Reader = (*onlineReader)(nil)
var _ buf.TimeoutReader = (*onlineReader)(nil)
