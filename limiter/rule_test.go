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
