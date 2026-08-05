package limiter

import (
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
)

func TestUpdateRuleRejectsInvalidPatternWithoutReplacingRules(t *testing.T) {
	l := &Limiter{}
	if err := l.UpdateRule(&panel.Rules{
		Regexp:   []string{`^allowed\.example$`},
		Protocol: []string{"bittorrent"},
	}); err != nil {
		t.Fatalf("initial rule update failed: %v", err)
	}

	if !l.CheckDomainRule("allowed.example") {
		t.Fatal("initial domain rule was not installed")
	}
	if !l.CheckProtocolRule("bittorrent") {
		t.Fatal("initial protocol rule was not installed")
	}

	if err := l.UpdateRule(&panel.Rules{Regexp: []string{"["}}); err == nil {
		t.Fatal("invalid regexp was accepted")
	}
	if !l.CheckDomainRule("allowed.example") {
		t.Fatal("invalid update replaced the last valid domain rules")
	}
	if !l.CheckProtocolRule("bittorrent") {
		t.Fatal("invalid update replaced the last valid protocol rules")
	}
}

func TestUpdateRuleNil(t *testing.T) {
	if err := (&Limiter{}).UpdateRule(nil); err == nil {
		t.Fatal("nil rules were accepted")
	}
}

func TestCheckRouteUsesSameSafetyDomainAndProtocolOrder(t *testing.T) {
	l := &Limiter{}
	if err := l.UpdateRule(&panel.Rules{
		Regexp:   []string{`(?i)(?:^|\.)blocked\.example\.?$`},
		Protocol: []string{"bittorrent"},
	}); err != nil {
		t.Fatalf("update rules: %v", err)
	}

	tests := []struct {
		name        string
		network     string
		destination string
		protocol    string
		wantReason  string
	}{
		{name: "safety first", network: "tcp", destination: "127.0.0.1:25", wantReason: "safety:private_or_special_ip"},
		{name: "panel domain", network: "tcp", destination: "API.BLOCKED.EXAMPLE:443", wantReason: RouteReasonPanelDomain},
		{name: "panel protocol normalized", network: "tcp", destination: "example.net:443", protocol: "BitTorrent", wantReason: "panel_protocol:bittorrent"},
		{name: "allowed", network: "tcp", destination: "www.bilibili.com:443"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reason, rejected := l.CheckRoute(test.network, test.destination, test.protocol)
			if rejected != (test.wantReason != "") || reason != test.wantReason {
				t.Fatalf("CheckRoute() = %q, %v; want %q, %v", reason, rejected, test.wantReason, test.wantReason != "")
			}
		})
	}
}
