package limiter

import (
	"strings"
	"sync"
	"time"

	"github.com/InazumaV/V2bX/conf"
)

const defaultAuditInterval = 10 * time.Second

type rejectionAuditState struct {
	lastLogged time.Time
	lastSeen   time.Time
	suppressed int
}

type rejectionAudit struct {
	mu          sync.Mutex
	states      map[string]*rejectionAuditState
	interval    time.Duration
	maxKeys     int
	nextCleanup time.Time
}

func newRejectionAudit(config *conf.AbuseGuardConfig) *rejectionAudit {
	maxUsers := defaultMaxTrackedUsers
	interval := defaultAuditInterval
	if config != nil {
		maxUsers = intOrDefault(config.MaxTrackedUsers, defaultMaxTrackedUsers)
		interval = durationOrDefault(config.WindowSeconds, defaultAuditInterval)
	}
	maxKeys := maxUsers * 4
	if maxKeys < 1024 {
		maxKeys = 1024
	}
	if maxKeys > 50000 {
		maxKeys = 50000
	}
	now := time.Now()
	return &rejectionAudit{
		states:      make(map[string]*rejectionAuditState),
		interval:    interval,
		maxKeys:     maxKeys,
		nextCleanup: now.Add(interval),
	}
}

// ShouldLogRejection rate-limits audit output without changing the reject
// decision. suppressed is the number of duplicate events since the last log.
func (l *Limiter) ShouldLogRejection(user, reason string) (logEvent bool, suppressed int) {
	if l == nil || l.audit == nil {
		return true, 0
	}
	return l.audit.allow(user, reason, time.Now())
}

func (a *rejectionAudit) allow(user, reason string, now time.Time) (bool, int) {
	key := user + "\x00" + reason
	a.mu.Lock()
	defer a.mu.Unlock()

	if !now.Before(a.nextCleanup) {
		staleBefore := now.Add(-4 * a.interval)
		for candidate, state := range a.states {
			if state.lastSeen.Before(staleBefore) {
				delete(a.states, candidate)
			}
		}
		a.nextCleanup = now.Add(a.interval)
	}
	if state, ok := a.states[key]; ok {
		state.lastSeen = now
		if now.Sub(state.lastLogged) >= a.interval {
			suppressed := state.suppressed
			state.lastLogged = now
			state.suppressed = 0
			return true, suppressed
		}
		state.suppressed++
		return false, 0
	}
	if len(a.states) >= a.maxKeys {
		return false, 0
	}
	a.states[key] = &rejectionAuditState{lastLogged: now, lastSeen: now}
	return true, 0
}

func (a *rejectionAudit) removeUser(user string) {
	if a == nil {
		return
	}
	prefix := user + "\x00"
	a.mu.Lock()
	for key := range a.states {
		if strings.HasPrefix(key, prefix) {
			delete(a.states, key)
		}
	}
	a.mu.Unlock()
}
