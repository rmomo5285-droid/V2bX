package sing

import (
	"net"
	"strconv"
	"sync"

	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

type onlineConn struct {
	N.ExtendedConn
	device *limiter.DeviceLease
}

func (c *onlineConn) Close() error { defer c.device.Close(); return c.ExtendedConn.Close() }

// Preserve the protocol writer's headroom and buffer metadata through tracking.
func (c *onlineConn) Upstream() any { return c.ExtendedConn }

type onlinePacketConn struct {
	N.PacketConn
	device *limiter.DeviceLease
	once   sync.Once
}

func (c *onlinePacketConn) Upstream() any { return c.PacketConn }

func (c *onlinePacketConn) Close() error {
	defer c.once.Do(c.device.Close)
	return c.PacketConn.Close()
}

func trackConnection(conn net.Conn, l *limiter.Limiter, m adapter.InboundContext) net.Conn {
	user := format.UserTag(m.Inbound, m.User)
	recordDestination(l, user, m, "tcp")
	return &onlineConn{ExtendedConn: bufio.NewExtendedConn(conn), device: l.AcquireDevice(user, m.Source.Addr.String())}
}

func trackPacketConnection(conn N.PacketConn, l *limiter.Limiter, m adapter.InboundContext) N.PacketConn {
	user := format.UserTag(m.Inbound, m.User)
	recordDestination(l, user, m, "udp")
	return &onlinePacketConn{PacketConn: conn, device: l.AcquireDevice(user, m.Source.Addr.String())}
}

func recordDestination(l *limiter.Limiter, user string, m adapter.InboundContext, network string) {
	target := m.Destination.String()
	if m.Domain != "" && !m.Destination.IsFqdn() {
		target = net.JoinHostPort(m.Domain, strconv.Itoa(int(m.Destination.Port)))
	}
	l.RecordAccess(user, m.Source.Addr.String(), network, target)
}
