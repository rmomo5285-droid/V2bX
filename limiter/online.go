package limiter

import (
	"net/netip"
	"sync"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/accesslog"
)

type deviceKey struct{ user, ip string }
type deviceState struct {
	uid, refs int
	qualified bool
}

// DeviceLease represents one accepted connection/session. Multiple connections
// from one source IP share an entry; snapshots never consume that entry.
type DeviceLease struct {
	once    sync.Once
	limiter *Limiter
	key     deviceKey
	state   *deviceState
}

func (l *Limiter) AcquireDevice(user, source string) *DeviceLease {
	ip, err := netip.ParseAddr(source)
	if err != nil {
		return nil
	}
	l.userStateMu.RLock()
	defer l.userStateMu.RUnlock()
	v, ok := l.UserLimitInfo.Load(user)
	if !ok {
		return nil
	}
	key := deviceKey{user, ip.Unmap().String()}
	l.onlineMu.Lock()
	defer l.onlineMu.Unlock()
	if l.devices == nil {
		l.devices = make(map[deviceKey]*deviceState)
	}
	state := l.devices[key]
	if state == nil {
		state = &deviceState{uid: v.(*UserLimitInfo).UID, qualified: l.OnlineMinTraffic <= 0}
		l.devices[key] = state
	}
	state.refs++
	return &DeviceLease{limiter: l, key: key, state: state}
}

func (d *DeviceLease) Close() {
	if d == nil {
		return
	}
	d.once.Do(func() {
		l := d.limiter
		l.onlineMu.Lock()
		defer l.onlineMu.Unlock()
		// Stale closes must not decrement a deleted/re-added user's state.
		if l.devices[d.key] == d.state {
			d.state.refs--
			if d.state.refs == 0 {
				delete(l.devices, d.key)
			}
		}
	})
}

func (l *Limiter) RecordAccess(user, source, network, target string) {
	if !accesslog.Enabled() || l.NodeID == 0 {
		return
	}
	v, ok := l.UserLimitInfo.Load(user)
	if !ok {
		return
	}
	event, ok := accesslog.NewEvent(l.NodeID, v.(*UserLimitInfo).UID, source, network, target)
	if ok {
		accesslog.Record(event)
	}
}

func (l *Limiter) ActiveDeviceSnapshot() map[int][]string {
	l.onlineMu.Lock()
	defer l.onlineMu.Unlock()
	result := make(map[int][]string)
	for key, state := range l.devices {
		if state.refs > 0 && state.qualified {
			result[state.uid] = append(result[state.uid], key.ip)
		}
	}
	return result
}

func (l *Limiter) hasActiveDevice(user, ip string) bool {
	l.onlineMu.Lock()
	defer l.onlineMu.Unlock()
	state := l.devices[deviceKey{user, ip}]
	return state != nil && state.refs > 0
}

// Preserve the per-user probe traffic gate, latching qualification until the
// last connection closes so quiet accounting periods do not hide live IPs.
func (l *Limiter) QualifyOnlineDevices(traffic []panel.UserTraffic) {
	if l.OnlineMinTraffic <= 0 {
		return
	}
	qualified := make(map[int]struct{})
	for _, t := range traffic {
		if t.Upload+t.Download >= l.OnlineMinTraffic {
			qualified[t.UID] = struct{}{}
		}
	}
	l.onlineMu.Lock()
	defer l.onlineMu.Unlock()
	for _, state := range l.devices {
		if _, ok := qualified[state.uid]; ok {
			state.qualified = true
		}
	}
}
