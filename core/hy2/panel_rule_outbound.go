package hy2

import (
	"errors"
	"fmt"
	"net"

	"github.com/InazumaV/V2bX/limiter"
	"github.com/apernet/hysteria/core/v2/server"
	"go.uber.org/zap"
)

var errPanelRuleRejected = errors.New("destination rejected by panel rule")
var errSafetyRuleRejected = errors.New("destination rejected by node safety rule")

type panelRuleOutbound struct {
	tag      string
	outbound server.Outbound
	logger   *zap.Logger
}

func (o *panelRuleOutbound) TCP(reqAddr string) (net.Conn, error) {
	if err := o.check("tcp", reqAddr); err != nil {
		return nil, err
	}
	return o.outbound.TCP(reqAddr)
}

func (o *panelRuleOutbound) UDP(reqAddr string) (server.UDPConn, error) {
	if err := o.check("udp", reqAddr); err != nil {
		return nil, err
	}
	conn, err := o.outbound.UDP(reqAddr)
	if err != nil {
		return nil, err
	}
	limit, err := limiter.GetLimiter(o.tag)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("get limiter for %s: %w", o.tag, err)
	}
	return &panelRuleUDPConn{
		UDPConn: conn,
		check: func(reqAddr string) error {
			return o.check("udp", reqAddr)
		},
		flow:   limit.NewUDPFlowGuard(),
		tag:    o.tag,
		logger: o.logger,
	}, nil
}

func (o *panelRuleOutbound) check(network, reqAddr string) error {
	l, err := limiter.GetLimiter(o.tag)
	if err != nil {
		return fmt.Errorf("get limiter for %s: %w", o.tag, err)
	}
	destination := destinationHost(reqAddr)
	if reason, reject := limiter.CheckSafetyDestination(network, reqAddr); reject {
		return fmt.Errorf("%w: %s target %s", errSafetyRuleRejected, reason, destination)
	}
	if l.CheckDomainRule(destination) {
		return fmt.Errorf("%w: %s", errPanelRuleRejected, destination)
	}
	return nil
}

type panelRuleUDPConn struct {
	server.UDPConn
	check  func(reqAddr string) error
	flow   *limiter.UDPFlowGuard
	tag    string
	logger *zap.Logger
}

func (c *panelRuleUDPConn) WriteTo(b []byte, addr string) (int, error) {
	if err := c.check(addr); err != nil {
		return 0, err
	}
	if decision := c.flow.Check(addr); decision.Reject {
		if c.logger != nil {
			if limit, err := limiter.GetLimiter(c.tag); err == nil {
				if shouldLog, suppressed := limit.ShouldLogRejection("unknown:"+c.tag, "abuse_udp_flow:"+decision.Reason); shouldLog {
					c.logger.Warn("abuse guard rejected UDP flow target",
						zap.String("tag", c.tag),
						zap.String("target", addr),
						zap.String("reason", decision.Reason),
						zap.Int("count", decision.Count),
						zap.Int("suppressed", suppressed))
				}
			}
		}
		return 0, fmt.Errorf("%w: %s count=%d", errAbuseGuardRejected, decision.Reason, decision.Count)
	}
	return c.UDPConn.WriteTo(b, addr)
}
