package panel

import (
	"reflect"
	"regexp"
	"testing"
)

func TestAppendBlockRulesNormalizesPanelDomains(t *testing.T) {
	var rules Rules
	err := appendBlockRules(&rules, []string{
		"Example.COM",
		"*.video.example.com",
		"regexp:^explicit\\.example$",
		"protocol:BitTorrent",
		"example.com",
	})
	if err != nil {
		t.Fatalf("append block rules: %v", err)
	}

	wantProtocols := []string{"bittorrent"}
	if !reflect.DeepEqual(rules.Protocol, wantProtocols) {
		t.Fatalf("protocol rules = %#v, want %#v", rules.Protocol, wantProtocols)
	}
	if len(rules.Regexp) != 3 {
		t.Fatalf("domain rule count = %d, want 3: %#v", len(rules.Regexp), rules.Regexp)
	}

	compiled := make([]*regexp.Regexp, len(rules.Regexp))
	for i, pattern := range rules.Regexp {
		compiled[i] = regexp.MustCompile(pattern)
	}
	assertAnyMatch(t, compiled, "example.com", true)
	assertAnyMatch(t, compiled, "WWW.EXAMPLE.COM", true)
	assertAnyMatch(t, compiled, "notexample.com", false)
	assertAnyMatch(t, compiled, "cdn.video.example.com", true)
	assertAnyMatch(t, compiled, "explicit.example", true)
}

func TestNormalizeDomainRuleEscapesDots(t *testing.T) {
	pattern, err := normalizeDomainRule("taobao.com")
	if err != nil {
		t.Fatalf("normalize domain rule: %v", err)
	}
	rule := regexp.MustCompile(pattern)
	if !rule.MatchString("www.taobao.com") {
		t.Fatal("normalized suffix rule did not match a subdomain")
	}
	if rule.MatchString("taobaoXcom") {
		t.Fatal("normalized suffix rule treated a dot as a wildcard")
	}
}

func TestRouteMatchesRejectsNonStringValues(t *testing.T) {
	if _, err := routeMatches([]interface{}{"ok.example", 42}); err == nil {
		t.Fatal("expected a non-string route match to fail")
	}
}

func assertAnyMatch(t *testing.T, rules []*regexp.Regexp, value string, want bool) {
	t.Helper()
	got := false
	for _, rule := range rules {
		if rule.MatchString(value) {
			got = true
			break
		}
	}
	if got != want {
		t.Fatalf("rules match %q = %v, want %v", value, got, want)
	}
}
