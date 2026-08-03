package hy2

import (
	"errors"
	"io"
	"net"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/apernet/hysteria/core/v2/server"
)

type outboundStub struct {
	tcpCalls []string
	udpCalls []string
	udpConn  *udpConnStub
}

func (o *outboundStub) TCP(reqAddr string) (net.Conn, error) {
	o.tcpCalls = append(o.tcpCalls, reqAddr)
	left, right := net.Pipe()
	_ = right.Close()
	return left, nil
}

func (o *outboundStub) UDP(reqAddr string) (server.UDPConn, error) {
	o.udpCalls = append(o.udpCalls, reqAddr)
	if o.udpConn == nil {
		o.udpConn = &udpConnStub{}
	}
	return o.udpConn, nil
}

type udpConnStub struct {
	writes []string
}

func (c *udpConnStub) ReadFrom([]byte) (int, string, error) {
	return 0, "", io.EOF
}

func (c *udpConnStub) WriteTo(b []byte, addr string) (int, error) {
	c.writes = append(c.writes, addr)
	return len(b), nil
}

func (c *udpConnStub) Close() error { return nil }

func newPanelRuleOutboundForTest(t *testing.T, rules panel.Rules) (*panelRuleOutbound, *outboundStub) {
	t.Helper()
	limiter.Init()
	l := limiter.AddLimiter("hy2-test", &conf.LimitConfig{}, nil, map[int]int{})
	if err := l.UpdateRule(&rules); err != nil {
		t.Fatalf("update rules: %v", err)
	}
	stub := &outboundStub{}
	return &panelRuleOutbound{tag: "hy2-test", outbound: stub}, stub
}

func TestPanelRuleOutboundRejectsDomainBeforeTCPDial(t *testing.T) {
	o, stub := newPanelRuleOutboundForTest(t, panel.Rules{
		Regexp: []string{`(?i)(^|\.)blocked\.example$`},
	})

	conn, err := o.TCP("api.blocked.example:443")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("rejected TCP request returned a connection")
	}
	if !errors.Is(err, errPanelRuleRejected) {
		t.Fatalf("expected panel rejection, got %v", err)
	}
	if len(stub.tcpCalls) != 0 {
		t.Fatal("rejected TCP request reached the wrapped outbound")
	}
}

func TestPanelRuleOutboundAllowsUnmatchedTCP(t *testing.T) {
	o, stub := newPanelRuleOutboundForTest(t, panel.Rules{
		Regexp: []string{`(?i)(^|\.)blocked\.example$`},
	})

	conn, err := o.TCP("www.bilibili.com:443")
	if err != nil {
		t.Fatalf("allowed TCP request failed: %v", err)
	}
	_ = conn.Close()
	if len(stub.tcpCalls) != 1 || stub.tcpCalls[0] != "www.bilibili.com:443" {
		t.Fatalf("unexpected TCP calls: %#v", stub.tcpCalls)
	}
}

func TestPanelRuleOutboundRejectsPrivateTargetsAndSMTP(t *testing.T) {
	o, stub := newPanelRuleOutboundForTest(t, panel.Rules{})
	for _, target := range []string{"127.0.0.1:443", "169.254.169.254:80", "[fc00::1]:443", "198.51.100.10:25"} {
		conn, err := o.TCP(target)
		if conn != nil {
			_ = conn.Close()
			t.Fatalf("unsafe target %s returned a connection", target)
		}
		if !errors.Is(err, errSafetyRuleRejected) {
			t.Fatalf("unsafe target %s error = %v", target, err)
		}
	}
	if len(stub.tcpCalls) != 0 {
		t.Fatalf("unsafe targets reached wrapped outbound: %#v", stub.tcpCalls)
	}
}

func TestPanelRuleOutboundChecksEveryUDPWrite(t *testing.T) {
	o, stub := newPanelRuleOutboundForTest(t, panel.Rules{
		Regexp: []string{`^blocked\.example$`},
	})

	conn, err := o.UDP("allowed.example:53")
	if err != nil {
		t.Fatalf("create UDP outbound: %v", err)
	}
	if _, err := conn.WriteTo([]byte("ok"), "allowed.example:53"); err != nil {
		t.Fatalf("allowed UDP write failed: %v", err)
	}
	if _, err := conn.WriteTo([]byte("blocked"), "blocked.example:53"); !errors.Is(err, errPanelRuleRejected) {
		t.Fatalf("expected UDP panel rejection, got %v", err)
	}
	if len(stub.udpConn.writes) != 1 || stub.udpConn.writes[0] != "allowed.example:53" {
		t.Fatalf("unexpected wrapped UDP writes: %#v", stub.udpConn.writes)
	}
}

func TestPanelRuleOutboundBoundsTargetsWithinOneUDPSession(t *testing.T) {
	limiter.Init()
	l := limiter.AddLimiter("hy2-udp-flow-test", &conf.LimitConfig{
		AbuseGuardConfig: &conf.AbuseGuardConfig{
			Enable:                  true,
			MaxDistinctDestinations: 100,
			MaxDistinctIPTargets:    2,
			MaxDistinctPorts:        100,
		},
	}, nil, map[int]int{})
	if err := l.UpdateRule(&panel.Rules{}); err != nil {
		t.Fatalf("update rules: %v", err)
	}
	stub := &outboundStub{}
	o := &panelRuleOutbound{tag: "hy2-udp-flow-test", outbound: stub}
	conn, err := o.UDP("1.1.1.1:53")
	if err != nil {
		t.Fatalf("create UDP outbound: %v", err)
	}
	for _, target := range []string{"1.1.1.1:53", "8.8.8.8:53"} {
		if _, err := conn.WriteTo([]byte("ok"), target); err != nil {
			t.Fatalf("allowed UDP target %s failed: %v", target, err)
		}
	}
	if _, err := conn.WriteTo([]byte("scan"), "9.9.9.9:53"); !errors.Is(err, errAbuseGuardRejected) {
		t.Fatalf("same-session UDP scan was not rejected: %v", err)
	}
	if len(stub.udpConn.writes) != 2 {
		t.Fatalf("wrapped UDP writes = %d, want 2", len(stub.udpConn.writes))
	}
}

func TestPanelRuleOutboundDoesNotTreatTransportAsApplicationProtocol(t *testing.T) {
	o, stub := newPanelRuleOutboundForTest(t, panel.Rules{
		Protocol: []string{"udp"},
	})

	conn, err := o.UDP("1.1.1.1:53")
	if err != nil {
		t.Fatalf("transport-only UDP request was rejected: %v", err)
	}
	_ = conn.Close()
	if len(stub.udpCalls) != 1 {
		t.Fatalf("UDP outbound calls = %d, want 1", len(stub.udpCalls))
	}
}

func TestPanelRuleOutboundPreservesIPv6Host(t *testing.T) {
	o, _ := newPanelRuleOutboundForTest(t, panel.Rules{
		Regexp: []string{`^2606:4700:4700::1111$`},
	})

	conn, err := o.TCP("[2606:4700:4700::1111]:443")
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, errPanelRuleRejected) {
		t.Fatalf("expected IPv6 panel rejection, got %v", err)
	}
}
