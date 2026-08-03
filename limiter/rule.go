package limiter

import (
	"fmt"
	"regexp"

	"github.com/InazumaV/V2bX/api/panel"
)

func (l *Limiter) CheckDomainRule(destination string) (reject bool) {
	l.ruleMu.RLock()
	defer l.ruleMu.RUnlock()
	// have rule
	for i := range l.DomainRules {
		if l.DomainRules[i].MatchString(destination) {
			reject = true
			break
		}
	}
	return
}

func (l *Limiter) CheckProtocolRule(protocol string) (reject bool) {
	l.ruleMu.RLock()
	defer l.ruleMu.RUnlock()
	for i := range l.ProtocolRules {
		if l.ProtocolRules[i] == protocol {
			reject = true
			break
		}
	}
	return
}

func (l *Limiter) HasDomainRules() bool {
	l.ruleMu.RLock()
	defer l.ruleMu.RUnlock()
	return len(l.DomainRules) > 0
}

func (l *Limiter) HasProtocolRule(protocol string) bool {
	return l.CheckProtocolRule(protocol)
}

func (l *Limiter) ProtocolRulesSnapshot() []string {
	l.ruleMu.RLock()
	defer l.ruleMu.RUnlock()
	return append([]string(nil), l.ProtocolRules...)
}

func (l *Limiter) UpdateRule(rule *panel.Rules) error {
	if rule == nil {
		return fmt.Errorf("rules cannot be nil")
	}
	compiled := make([]*regexp.Regexp, len(rule.Regexp))
	for i := range rule.Regexp {
		r, err := regexp.Compile(rule.Regexp[i])
		if err != nil {
			return fmt.Errorf("invalid domain rule %d (%q): %w", i, rule.Regexp[i], err)
		}
		compiled[i] = r
	}
	protocols := append([]string(nil), rule.Protocol...)
	l.ruleMu.Lock()
	l.DomainRules = compiled
	l.ProtocolRules = protocols
	l.ruleMu.Unlock()
	return nil
}
