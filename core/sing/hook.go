package sing

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/common/rate"

	"github.com/InazumaV/V2bX/limiter"

	"github.com/InazumaV/V2bX/common/counter"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	N "github.com/sagernet/sing/common/network"
)

var _ adapter.ConnectionTracker = (*HookServer)(nil)

type HookServer struct {
	counter sync.Map //map[string]*counter.TrafficCounter
}

func (h *HookServer) ModeList() []string {
	return nil
}

func (h *HookServer) RoutedConnection(_ context.Context, conn net.Conn, m adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) net.Conn {
	l, err := limiter.GetLimiter(m.Inbound)
	if err != nil {
		log.Warn("get limiter for ", m.Inbound, " error: ", err)
		return conn
	}
	taguuid := format.UserTag(m.Inbound, m.User)
	ip := m.Source.Addr.String()
	if b, r := l.CheckLimit(taguuid, ip, true, true); r {
		conn.Close()
		if shouldLog, suppressed := l.ShouldLogRejection(taguuid, "connection_or_device_limit"); shouldLog {
			log.Error(fmt.Sprintf(
				"Rejected user %s source %s: reason=connection_or_device_limit suppressed=%d",
				m.User, ip, suppressed))
		}
		return conn
	} else if b != nil {
		conn = rate.NewConnRateLimiter(conn, b)
	}
	if l != nil {
		destination := m.Destination.String()
		protocol := m.Protocol
		if reason, reject := l.CheckRoute("tcp", destination, protocol); reject {
			if shouldLog, suppressed := l.ShouldLogRejection(taguuid, reason); shouldLog {
				log.Error(fmt.Sprintf(
					"Routing policy rejected user %s target %s: reason=%s suppressed=%d",
					m.User,
					destination,
					reason,
					suppressed))
			}
			conn.Close()
			return conn
		}
		if decision := l.CheckOutbound(taguuid, m.Destination.String()); decision.Reject {
			if shouldLog, suppressed := l.ShouldLogRejection(taguuid, "abuse:"+decision.Reason); shouldLog {
				log.Error(fmt.Sprintf(
					"Abuse guard rejected user %s target %s: reason=%s count=%d blocked_until=%s suppressed=%d",
					m.User,
					m.Destination.String(),
					decision.Reason,
					decision.Count,
					decision.BlockedUntil.UTC().Format(time.RFC3339),
					suppressed))
			}
			conn.Close()
			return conn
		}
	}
	var t *counter.TrafficCounter
	if c, ok := h.counter.Load(m.Inbound); !ok {
		t = counter.NewTrafficCounter()
		h.counter.Store(m.Inbound, t)
	} else {
		t = c.(*counter.TrafficCounter)
	}
	conn = counter.NewConnCounter(conn, t.GetCounter(m.User))
	return conn
}

func (h *HookServer) RoutedPacketConnection(_ context.Context, conn N.PacketConn, m adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) N.PacketConn {
	l, err := limiter.GetLimiter(m.Inbound)
	if err != nil {
		log.Warn("get limiter for ", m.Inbound, " error: ", err)
		return conn
	}
	ip := m.Source.Addr.String()
	taguuid := format.UserTag(m.Inbound, m.User)
	if b, r := l.CheckLimit(taguuid, ip, false, false); r {
		conn.Close()
		if shouldLog, suppressed := l.ShouldLogRejection(taguuid, "connection_or_device_limit"); shouldLog {
			log.Error(fmt.Sprintf(
				"Rejected user %s source %s: reason=connection_or_device_limit suppressed=%d",
				m.User, ip, suppressed))
		}
		return conn
	} else if b != nil {
		//conn = rate.NewPacketConnCounter(conn, b)
	}
	if l != nil {
		destination := m.Destination.String()
		protocol := m.Protocol
		if reason, reject := l.CheckRoute("udp", destination, protocol); reject {
			if shouldLog, suppressed := l.ShouldLogRejection(taguuid, reason); shouldLog {
				log.Error(fmt.Sprintf(
					"Routing policy rejected user %s target %s: reason=%s suppressed=%d",
					m.User,
					destination,
					reason,
					suppressed))
			}
			conn.Close()
			return conn
		}
		if decision := l.CheckOutbound(taguuid, m.Destination.String()); decision.Reject {
			if shouldLog, suppressed := l.ShouldLogRejection(taguuid, "abuse:"+decision.Reason); shouldLog {
				log.Error(fmt.Sprintf(
					"Abuse guard rejected user %s target %s: reason=%s count=%d blocked_until=%s suppressed=%d",
					m.User,
					m.Destination.String(),
					decision.Reason,
					decision.Count,
					decision.BlockedUntil.UTC().Format(time.RFC3339),
					suppressed))
			}
			conn.Close()
			return conn
		}
	}
	var t *counter.TrafficCounter
	if c, ok := h.counter.Load(m.Inbound); !ok {
		t = counter.NewTrafficCounter()
		h.counter.Store(m.Inbound, t)
	} else {
		t = c.(*counter.TrafficCounter)
	}
	conn = counter.NewPacketConnCounter(conn, t.GetCounter(m.User))
	return conn
}
