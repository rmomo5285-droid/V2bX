package limiter

import (
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/InazumaV/V2bX/conf"
)

const (
	defaultAbuseWindow             = 10 * time.Second
	defaultAbuseBlock              = 10 * time.Minute
	defaultMaxClientConnections    = 20
	defaultMaxOutboundConnections  = 300
	defaultMaxDistinctDestinations = 128
	defaultMaxDistinctIPTargets    = 32
	defaultMaxDistinctPorts        = 16
	defaultMaxTrackedUsers         = 10000
)

type AbuseDecision struct {
	Reject       bool
	Reason       string
	BlockedUntil time.Time
	Count        int
}

type abuseGuardSettings struct {
	window                  time.Duration
	block                   time.Duration
	maxClientConnections    int
	maxOutboundConnections  int
	maxDistinctDestinations int
	maxDistinctIPTargets    int
	maxDistinctPorts        int
	maxTrackedUsers         int64
}

type abuseUserState struct {
	mu sync.Mutex

	windowStart         time.Time
	lastSeen            time.Time
	blockedUntil        time.Time
	blockReason         string
	clientConnections   int
	outboundConnections int
	destinations        map[string]struct{}
	ipTargets           map[string]struct{}
	ports               map[string]struct{}
}

type abuseGuard struct {
	settings abuseGuardSettings
	users    sync.Map
	count    atomic.Int64
	nextGC   atomic.Int64
	addMu    sync.Mutex
}

// UDPFlowGuard bounds target fan-out inside one UDP association. Native
// Hysteria2's upstream hook exposes the UUID only when the association is
// created, while later datagrams may change destination. Per-user checks cover
// association creation; this guard closes the same-session bypass.
type UDPFlowGuard struct {
	mu           sync.Mutex
	settings     abuseGuardSettings
	blocked      AbuseDecision
	destinations map[string]struct{}
	ipTargets    map[string]struct{}
	ports        map[string]struct{}
}

func newAbuseGuard(config *conf.AbuseGuardConfig) *abuseGuard {
	if config == nil || !config.Enable {
		return nil
	}
	settings := abuseGuardSettings{
		window:                  durationOrDefault(config.WindowSeconds, defaultAbuseWindow),
		block:                   durationOrDefault(config.BlockSeconds, defaultAbuseBlock),
		maxClientConnections:    intOrDefault(config.MaxClientConnections, defaultMaxClientConnections),
		maxOutboundConnections:  intOrDefault(config.MaxOutboundConnections, defaultMaxOutboundConnections),
		maxDistinctDestinations: intOrDefault(config.MaxDistinctDestinations, defaultMaxDistinctDestinations),
		maxDistinctIPTargets:    intOrDefault(config.MaxDistinctIPTargets, defaultMaxDistinctIPTargets),
		maxDistinctPorts:        intOrDefault(config.MaxDistinctPorts, defaultMaxDistinctPorts),
		maxTrackedUsers:         int64(intOrDefault(config.MaxTrackedUsers, defaultMaxTrackedUsers)),
	}
	guard := &abuseGuard{settings: settings}
	guard.nextGC.Store(time.Now().Add(settings.window).UnixNano())
	return guard
}

func durationOrDefault(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func intOrDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func (g *abuseGuard) checkClientConnection(user string, now time.Time) AbuseDecision {
	state, ok := g.stateFor(user, now)
	if !ok {
		return AbuseDecision{Reject: true, Reason: "tracker_capacity"}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if decision := g.prepareState(state, now); decision.Reject {
		return decision
	}
	state.clientConnections++
	if state.clientConnections > g.settings.maxClientConnections {
		return g.block(state, now, "client_connection_rate", state.clientConnections)
	}
	return AbuseDecision{}
}

func (l *Limiter) NewUDPFlowGuard() *UDPFlowGuard {
	if l == nil || l.abuse == nil {
		return nil
	}
	return &UDPFlowGuard{
		settings:     l.abuse.settings,
		destinations: make(map[string]struct{}),
		ipTargets:    make(map[string]struct{}),
		ports:        make(map[string]struct{}),
	}
}

func (g *UDPFlowGuard) Check(destination string) AbuseDecision {
	if g == nil {
		return AbuseDecision{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.blocked.Reject {
		return g.blocked
	}
	host, port := splitDestination(destination)
	if host != "" {
		g.destinations[host] = struct{}{}
		if len(g.destinations) > g.settings.maxDistinctDestinations {
			g.blocked = AbuseDecision{Reject: true, Reason: "udp_flow_destination_scan", Count: len(g.destinations)}
			return g.blocked
		}
		if net.ParseIP(host) != nil {
			g.ipTargets[host] = struct{}{}
			if len(g.ipTargets) > g.settings.maxDistinctIPTargets {
				g.blocked = AbuseDecision{Reject: true, Reason: "udp_flow_ip_scan", Count: len(g.ipTargets)}
				return g.blocked
			}
		}
	}
	if port != "" {
		g.ports[port] = struct{}{}
		if len(g.ports) > g.settings.maxDistinctPorts {
			g.blocked = AbuseDecision{Reject: true, Reason: "udp_flow_port_scan", Count: len(g.ports)}
			return g.blocked
		}
	}
	return AbuseDecision{}
}

func (g *abuseGuard) checkOutbound(user, destination string, now time.Time) AbuseDecision {
	state, ok := g.stateFor(user, now)
	if !ok {
		return AbuseDecision{Reject: true, Reason: "tracker_capacity"}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if decision := g.prepareState(state, now); decision.Reject {
		return decision
	}

	state.outboundConnections++
	if state.outboundConnections > g.settings.maxOutboundConnections {
		return g.block(state, now, "outbound_connection_rate", state.outboundConnections)
	}

	host, port := splitDestination(destination)
	if host != "" {
		state.destinations[host] = struct{}{}
		if len(state.destinations) > g.settings.maxDistinctDestinations {
			return g.block(state, now, "distinct_destination_scan", len(state.destinations))
		}
		if net.ParseIP(host) != nil {
			state.ipTargets[host] = struct{}{}
			if len(state.ipTargets) > g.settings.maxDistinctIPTargets {
				return g.block(state, now, "distinct_ip_scan", len(state.ipTargets))
			}
		}
	}
	if port != "" {
		state.ports[port] = struct{}{}
		if len(state.ports) > g.settings.maxDistinctPorts {
			return g.block(state, now, "distinct_port_scan", len(state.ports))
		}
	}
	return AbuseDecision{}
}

func (g *abuseGuard) isBlocked(user string, now time.Time) AbuseDecision {
	value, ok := g.users.Load(user)
	if !ok {
		return AbuseDecision{}
	}
	state := value.(*abuseUserState)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.lastSeen = now
	if now.Before(state.blockedUntil) {
		return AbuseDecision{Reject: true, Reason: state.blockReason, BlockedUntil: state.blockedUntil}
	}
	return AbuseDecision{}
}

func (g *abuseGuard) stateFor(user string, now time.Time) (*abuseUserState, bool) {
	g.cleanup(now)
	if value, ok := g.users.Load(user); ok {
		return value.(*abuseUserState), true
	}

	g.addMu.Lock()
	defer g.addMu.Unlock()
	if value, ok := g.users.Load(user); ok {
		return value.(*abuseUserState), true
	}
	if g.count.Load() >= g.settings.maxTrackedUsers {
		return nil, false
	}
	state := &abuseUserState{}
	state.reset(now)
	g.users.Store(user, state)
	g.count.Add(1)
	return state, true
}

func (g *abuseGuard) prepareState(state *abuseUserState, now time.Time) AbuseDecision {
	state.lastSeen = now
	if now.Before(state.blockedUntil) {
		return AbuseDecision{Reject: true, Reason: state.blockReason, BlockedUntil: state.blockedUntil}
	}
	if !state.blockedUntil.IsZero() || now.Sub(state.windowStart) >= g.settings.window {
		state.reset(now)
	}
	return AbuseDecision{}
}

func (g *abuseGuard) block(state *abuseUserState, now time.Time, reason string, count int) AbuseDecision {
	state.blockReason = reason
	state.blockedUntil = now.Add(g.settings.block)
	return AbuseDecision{Reject: true, Reason: reason, BlockedUntil: state.blockedUntil, Count: count}
}

func (s *abuseUserState) reset(now time.Time) {
	s.windowStart = now
	s.lastSeen = now
	s.blockedUntil = time.Time{}
	s.blockReason = ""
	s.clientConnections = 0
	s.outboundConnections = 0
	s.destinations = make(map[string]struct{})
	s.ipTargets = make(map[string]struct{})
	s.ports = make(map[string]struct{})
}

func (g *abuseGuard) removeUser(user string) {
	if _, loaded := g.users.LoadAndDelete(user); loaded {
		g.count.Add(-1)
	}
}

func (g *abuseGuard) cleanup(now time.Time) {
	next := g.nextGC.Load()
	if now.UnixNano() < next || !g.nextGC.CompareAndSwap(next, now.Add(g.settings.window).UnixNano()) {
		return
	}
	staleAfter := g.settings.block * 2
	if g.settings.window*2 > staleAfter {
		staleAfter = g.settings.window * 2
	}
	g.users.Range(func(key, value interface{}) bool {
		state := value.(*abuseUserState)
		state.mu.Lock()
		stale := now.Sub(state.lastSeen) > staleAfter && now.After(state.blockedUntil)
		state.mu.Unlock()
		if stale && g.users.CompareAndDelete(key, state) {
			g.count.Add(-1)
		}
		return true
	})
}

func splitDestination(destination string) (host, port string) {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return "", ""
	}
	host, port, err := net.SplitHostPort(destination)
	if err != nil {
		host = destination
		port = ""
	}
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	return host, strings.TrimSpace(port)
}
