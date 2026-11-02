package hy2

import (
	"net"
	"sync"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/format"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/apernet/hysteria/core/v2/server"
	"go.uber.org/zap"
)

var _ server.Authenticator = &nodeAuthenticator{}

type V2bX struct {
	usersMap map[string]map[string]int
	mutex    sync.RWMutex
}

func (v *V2bX) authenticate(tag, auth string) bool {
	v.mutex.RLock()
	defer v.mutex.RUnlock()
	users := v.usersMap[tag]
	_, exists := users[auth]
	return exists
}

type nodeAuthenticator struct {
	tag    string
	users  *V2bX
	logger *zap.Logger
}

func (a *nodeAuthenticator) Authenticate(addr net.Addr, auth string, tx uint64) (ok bool, id string) {
	if !a.users.authenticate(a.tag, auth) {
		return false, ""
	}
	limit, err := limiter.GetLimiter(a.tag)
	if err != nil {
		a.logger.Error("Get limiter error", zap.String("tag", a.tag), zap.Error(err))
		return false, ""
	}
	taguuid := format.UserTag(a.tag, auth)
	decision := limit.CheckClientConnection(taguuid)
	if decision.Reject {
		if shouldLog, suppressed := limit.ShouldLogRejection(taguuid, "abuse:"+decision.Reason); shouldLog {
			a.logger.Warn("abuse guard rejected client connection",
				zap.String("tag", a.tag),
				zap.String("uuid", auth),
				zap.String("addr", addr.String()),
				zap.String("reason", decision.Reason),
				zap.Int("count", decision.Count),
				zap.Int("suppressed", suppressed),
				zap.Time("blocked_until", decision.BlockedUntil))
		}
		return false, ""
	}
	return true, newAuthID(auth)
}

func (h *Hysteria2) AddUsers(p *vCore.AddUsersParams) (added int, err error) {
	h.Auth.mutex.Lock()
	defer h.Auth.mutex.Unlock()
	if h.Auth.usersMap[p.Tag] == nil {
		h.Auth.usersMap[p.Tag] = make(map[string]int)
	}
	for _, user := range p.Users {
		h.Auth.usersMap[p.Tag][user.Uuid] = user.Id
	}
	return len(p.Users), nil
}

func (h *Hysteria2) DelUsers(users []panel.UserInfo, tag string, _ *panel.NodeInfo) error {
	node, nodeExists := h.node(tag)
	h.Auth.mutex.Lock()
	defer h.Auth.mutex.Unlock()
	for _, user := range users {
		delete(h.Auth.usersMap[tag], user.Uuid)
		if nodeExists {
			if v, ok := node.TrafficLogger.(*HookServer).Counter.Load(tag); ok {
				v.(*counter.TrafficCounter).Delete(user.Uuid)
			}
		}
	}
	return nil
}

func (h *Hysteria2) GetUserTrafficSlice(tag string, reset bool) ([]panel.UserTraffic, error) {
	trafficSlice := make([]panel.UserTraffic, 0)
	h.Auth.mutex.RLock()
	defer h.Auth.mutex.RUnlock()
	node, ok := h.node(tag)
	if !ok {
		return nil, nil
	}
	hook := node.TrafficLogger.(*HookServer)
	if v, ok := hook.Counter.Load(tag); ok {
		c := v.(*counter.TrafficCounter)
		c.Counters.Range(func(key, value interface{}) bool {
			uuid := key.(string)
			traffic := value.(*counter.TrafficStorage)
			up := traffic.UpCounter.Load()
			down := traffic.DownCounter.Load()
			if up+down > hook.ReportMinTrafficBytes {
				if reset {
					up, down = traffic.Read(true)
				}
				if h.Auth.usersMap[tag][uuid] == 0 {
					c.Delete(uuid)
					return true
				}
				trafficSlice = append(trafficSlice, panel.UserTraffic{
					UID:      h.Auth.usersMap[tag][uuid],
					Upload:   up,
					Download: down,
				})
			}
			return true
		})
		if len(trafficSlice) == 0 {
			return nil, nil
		}
		return trafficSlice, nil
	}
	return nil, nil
}
