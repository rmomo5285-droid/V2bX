package limiter

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	"github.com/juju/ratelimit"
)

var limitLock sync.RWMutex
var limiter map[string]*Limiter

func Init() {
	limiter = map[string]*Limiter{}
}

type Limiter struct {
	NodeID           int
	OnlineMinTraffic int64
	onlineMu         sync.Mutex
	devices          map[deviceKey]*deviceState
	ruleMu           sync.RWMutex
	userStateMu      sync.RWMutex
	abuse            *abuseGuard
	audit            *rejectionAudit
	DomainRules      []*regexp.Regexp
	ProtocolRules    []string
	SpeedLimit       int
	UserOnlineIP     *sync.Map      // Key: TagUUID, value: {Key: Ip, value: Uid}
	OldUserOnline    *sync.Map      // Key: Ip, value: Uid
	UUIDtoUID        map[string]int // Key: UUID, value: Uid
	UserLimitInfo    *sync.Map      // Key: TagUUID value: UserLimitInfo
	SpeedLimiter     *sync.Map      // key: TagUUID, value: *ratelimit.Bucket
	AliveList        map[int]int    // Key: Uid, value: alive_ip
}

type UserLimitInfo struct {
	mu                sync.Mutex
	UID               int
	SpeedLimit        int
	DeviceLimit       int
	DynamicSpeedLimit int
	ExpireTime        int64
	OverLimit         atomic.Bool
}

func AddLimiter(tag string, l *conf.LimitConfig, users []panel.UserInfo, aliveList map[int]int) *Limiter {
	info := &Limiter{
		SpeedLimit:    l.SpeedLimit,
		abuse:         newAbuseGuard(l.AbuseGuardConfig),
		audit:         newRejectionAudit(l.AbuseGuardConfig),
		UserOnlineIP:  new(sync.Map),
		UserLimitInfo: new(sync.Map),
		SpeedLimiter:  new(sync.Map),
		AliveList:     cloneIntMap(aliveList),
		OldUserOnline: new(sync.Map),
	}
	uuidmap := make(map[string]int)
	for i := range users {
		uuidmap[users[i].Uuid] = users[i].Id
		userLimit := newUserLimitInfo(users[i])
		info.UserLimitInfo.Store(format.UserTag(tag, users[i].Uuid), userLimit)
	}
	info.UUIDtoUID = uuidmap
	limitLock.Lock()
	limiter[tag] = info
	limitLock.Unlock()
	return info
}

func GetLimiter(tag string) (info *Limiter, err error) {
	limitLock.RLock()
	info, ok := limiter[tag]
	limitLock.RUnlock()
	if !ok {
		return nil, errors.New("not found")
	}
	return info, nil
}

func DeleteLimiter(tag string) {
	limitLock.Lock()
	delete(limiter, tag)
	limitLock.Unlock()
}

func (l *Limiter) UpdateUser(tag string, added []panel.UserInfo, deleted []panel.UserInfo) {
	l.userStateMu.Lock()
	defer l.userStateMu.Unlock()

	for i := range deleted {
		l.removeUserLocked(tag, deleted[i])
	}
	for i := range added {
		l.addUserLocked(tag, added[i])
	}
}

// StageNewUsers authorizes users that are not present in the limiter yet. It is
// used immediately before the core publishes those users, closing the small
// window where a freshly accepted connection could otherwise be rejected.
// Existing users are left untouched so dynamic limits survive a failed update.
func (l *Limiter) StageNewUsers(tag string, users []panel.UserInfo) []panel.UserInfo {
	l.userStateMu.Lock()
	defer l.userStateMu.Unlock()

	staged := make([]panel.UserInfo, 0, len(users))
	for i := range users {
		if _, exists := l.UUIDtoUID[users[i].Uuid]; exists {
			continue
		}
		l.addUserLocked(tag, users[i])
		staged = append(staged, users[i])
	}
	return staged
}

// RollbackStagedUsers removes only entries that still match the staged user.
// This avoids deleting a newer panel update if one was committed concurrently.
func (l *Limiter) RollbackStagedUsers(tag string, staged []panel.UserInfo) {
	l.userStateMu.Lock()
	defer l.userStateMu.Unlock()
	for i := range staged {
		uid, exists := l.UUIDtoUID[staged[i].Uuid]
		if !exists || uid != staged[i].Id {
			continue
		}
		l.removeUserLocked(tag, staged[i])
	}
}

func newUserLimitInfo(user panel.UserInfo) *UserLimitInfo {
	return &UserLimitInfo{
		UID:         user.Id,
		SpeedLimit:  user.SpeedLimit,
		DeviceLimit: user.DeviceLimit,
	}
}

func (l *Limiter) addUserLocked(tag string, user panel.UserInfo) {
	l.UserLimitInfo.Store(format.UserTag(tag, user.Uuid), newUserLimitInfo(user))
	l.UUIDtoUID[user.Uuid] = user.Id
}

func (l *Limiter) removeUserLocked(tag string, user panel.UserInfo) {
	taguuid := format.UserTag(tag, user.Uuid)
	if l.abuse != nil {
		l.abuse.removeUser(taguuid)
	}
	if l.audit != nil {
		l.audit.removeUser(taguuid)
	}
	l.UserLimitInfo.Delete(taguuid)
	l.UserOnlineIP.Delete(taguuid)
	l.onlineMu.Lock()
	for key := range l.devices {
		if key.user == taguuid {
			delete(l.devices, key)
		}
	}
	l.onlineMu.Unlock()
	l.SpeedLimiter.Delete(taguuid)
	delete(l.UUIDtoUID, user.Uuid)
	delete(l.AliveList, user.Id)
}

// RuleSnapshot is an immutable snapshot of the active routing rules.
// Its contents are intentionally opaque outside this package.
type RuleSnapshot struct {
	domainRules   []*regexp.Regexp
	protocolRules []string
}

func CompileRuleSnapshot(rule *panel.Rules) (RuleSnapshot, error) {
	if rule == nil {
		return RuleSnapshot{}, errors.New("rules cannot be nil")
	}
	compiled := make([]*regexp.Regexp, len(rule.Regexp))
	for i := range rule.Regexp {
		pattern, err := regexp.Compile(rule.Regexp[i])
		if err != nil {
			return RuleSnapshot{}, fmt.Errorf("invalid domain rule %d (%q): %w", i, rule.Regexp[i], err)
		}
		compiled[i] = pattern
	}
	return RuleSnapshot{
		domainRules:   compiled,
		protocolRules: append([]string(nil), rule.Protocol...),
	}, nil
}

func (l *Limiter) SnapshotRules() RuleSnapshot {
	l.ruleMu.RLock()
	defer l.ruleMu.RUnlock()
	return RuleSnapshot{
		domainRules:   append([]*regexp.Regexp(nil), l.DomainRules...),
		protocolRules: append([]string(nil), l.ProtocolRules...),
	}
}

func (l *Limiter) RestoreRules(snapshot RuleSnapshot) {
	l.ruleMu.Lock()
	l.DomainRules = append([]*regexp.Regexp(nil), snapshot.domainRules...)
	l.ProtocolRules = append([]string(nil), snapshot.protocolRules...)
	l.ruleMu.Unlock()
}

func (l *Limiter) SetAliveList(aliveList map[int]int) {
	snapshot := cloneIntMap(aliveList)
	l.userStateMu.Lock()
	l.AliveList = snapshot
	l.userStateMu.Unlock()
}

func (l *Limiter) AliveCount(uid int) int {
	l.userStateMu.RLock()
	alive := l.AliveList[uid]
	l.userStateMu.RUnlock()
	return alive
}

func (l *Limiter) UIDForUUID(uuid string) (int, bool) {
	l.userStateMu.RLock()
	uid, ok := l.UUIDtoUID[uuid]
	l.userStateMu.RUnlock()
	return uid, ok
}

func cloneIntMap(source map[int]int) map[int]int {
	clone := make(map[int]int, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func (l *Limiter) CheckClientConnection(taguuid string) AbuseDecision {
	if l.abuse == nil {
		return AbuseDecision{}
	}
	return l.abuse.checkClientConnection(taguuid, time.Now())
}

func (l *Limiter) CheckOutbound(taguuid, destination string) AbuseDecision {
	if l.abuse == nil {
		return AbuseDecision{}
	}
	return l.abuse.checkOutbound(taguuid, destination, time.Now())
}

func (l *Limiter) CheckAbuseBlocked(taguuid string) AbuseDecision {
	if l.abuse == nil {
		return AbuseDecision{}
	}
	return l.abuse.isBlocked(taguuid, time.Now())
}

func (l *Limiter) AbuseGuardEnabled() bool {
	return l.abuse != nil
}

func (l *Limiter) UpdateDynamicSpeedLimit(tag, uuid string, limit int, expire time.Time) error {
	taguuid := format.UserTag(tag, uuid)
	if v, ok := l.UserLimitInfo.Load(taguuid); ok {
		info := v.(*UserLimitInfo)
		info.mu.Lock()
		info.DynamicSpeedLimit = limit
		info.ExpireTime = expire.Unix()
		info.mu.Unlock()
		l.SpeedLimiter.Delete(taguuid)
	} else {
		return errors.New("not found")
	}
	return nil
}

func (l *Limiter) CheckLimit(taguuid string, ip string, isTcp bool, noSSUDP bool) (Bucket *ratelimit.Bucket, Reject bool) {
	// check if ipv4 mapped ipv6
	ip = strings.TrimPrefix(ip, "::ffff:")

	// check and gen speed limit Bucket
	nodeLimit := l.SpeedLimit
	userLimit := 0
	deviceLimit := 0
	var uid int
	if v, ok := l.UserLimitInfo.Load(taguuid); ok {
		u := v.(*UserLimitInfo)
		u.mu.Lock()
		deviceLimit = u.DeviceLimit
		uid = u.UID
		if u.ExpireTime < time.Now().Unix() && u.ExpireTime != 0 {
			u.DynamicSpeedLimit = 0
			u.ExpireTime = 0
			l.SpeedLimiter.Delete(taguuid)
		}
		userLimit = determineSpeedLimit(u.SpeedLimit, u.DynamicSpeedLimit)
		u.mu.Unlock()
	} else {
		return nil, true
	}
	if noSSUDP {
		// Store online user for device limit
		newipMap := new(sync.Map)
		newipMap.Store(ip, uid)
		aliveIp := l.AliveCount(uid)
		knownDevice := l.hasActiveDevice(taguuid, ip)
		// If any device is online
		if v, loaded := l.UserOnlineIP.LoadOrStore(taguuid, newipMap); loaded {
			oldipMap := v.(*sync.Map)
			// If this is a new ip
			if _, loaded := oldipMap.LoadOrStore(ip, uid); !loaded {
				if v, loaded := l.OldUserOnline.Load(ip); loaded {
					if v.(int) == uid {
						l.OldUserOnline.Delete(ip)
					}
				} else if deviceLimit > 0 && !knownDevice {
					if deviceLimit <= aliveIp {
						oldipMap.Delete(ip)
						return nil, true
					}
				}
			}
		} else if v, ok := l.OldUserOnline.Load(ip); ok {
			if v.(int) == uid {
				l.OldUserOnline.Delete(ip)
			}
		} else {
			if deviceLimit > 0 && !knownDevice {
				if deviceLimit <= aliveIp {
					l.UserOnlineIP.Delete(taguuid)
					return nil, true
				}
			}
		}
	}

	limit := int64(determineSpeedLimit(nodeLimit, userLimit)) * 1000000 / 8 // If you need the Speed limit
	if limit > 0 {
		Bucket = ratelimit.NewBucketWithQuantum(time.Second, limit, limit) // Byte/s
		if v, ok := l.SpeedLimiter.LoadOrStore(taguuid, Bucket); ok {
			return v.(*ratelimit.Bucket), false
		} else {
			l.SpeedLimiter.Store(taguuid, Bucket)
			return Bucket, false
		}
	} else {
		return nil, false
	}
}

func (l *Limiter) GetOnlineDevice() (*[]panel.OnlineUser, error) {
	var onlineUser []panel.OnlineUser
	l.OldUserOnline.Clear()
	l.UserOnlineIP.Range(func(key, value interface{}) bool {
		taguuid := key.(string)
		ipMap := value.(*sync.Map)
		ipMap.Range(func(key, value interface{}) bool {
			uid := value.(int)
			ip := key.(string)
			l.OldUserOnline.Store(ip, uid)
			onlineUser = append(onlineUser, panel.OnlineUser{UID: uid, IP: ip})
			return true
		})
		l.UserOnlineIP.Delete(taguuid) // Reset online device
		return true
	})

	return &onlineUser, nil
}

type UserIpList struct {
	Uid    int      `json:"Uid"`
	IpList []string `json:"Ips"`
}
