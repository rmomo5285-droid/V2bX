package hy2

import (
	"sync"

	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/apernet/hysteria/core/v2/server"
	"go.uber.org/zap"
)

var _ server.TrafficLogger = (*HookServer)(nil)

type HookServer struct {
	Tag                   string
	logger                *zap.Logger
	Counter               sync.Map
	ReportMinTrafficBytes int64
	StreamUsers           *hy2StreamUsers
}

func (h *HookServer) TraceStream(stream server.HyStream, stats *server.StreamStats) {
	if h.StreamUsers != nil {
		h.StreamUsers.Store(stream, authUser(stats.AuthID))
	}
}

func (h *HookServer) UntraceStream(stream server.HyStream) {
	if h.StreamUsers != nil {
		h.StreamUsers.Delete(stream)
	}
}

func (h *HookServer) LogTraffic(id string, tx, rx uint64) (ok bool) {
	id = authUser(id)

	limiterinfo, err := limiter.GetLimiter(h.Tag)
	if err != nil {
		h.logger.Error("Get limiter error", zap.String("tag", h.Tag), zap.Error(err))
		return false
	}

	userLimit, ok := limiterinfo.UserLimitInfo.Load(format.UserTag(h.Tag, id))
	if !ok {
		return false
	}
	{
		userlimitInfo := userLimit.(*limiter.UserLimitInfo)
		if userlimitInfo.OverLimit.Swap(false) {
			return false
		}
	}
	if decision := limiterinfo.CheckAbuseBlocked(format.UserTag(h.Tag, id)); decision.Reject {
		return false
	}

	c := counter.LoadOrCreate(&h.Counter, h.Tag)

	{
		tc := c
		tc.Rx(id, int(rx))
		tc.Tx(id, int(tx))
		return true
	}

}

func (s *HookServer) LogOnlineState(id string, online bool) {
}
