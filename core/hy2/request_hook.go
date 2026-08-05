package hy2

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/apernet/hysteria/core/v2/server"
	"github.com/apernet/hysteria/extras/v2/sniff"
	"go.uber.org/zap"
)

var errAbuseGuardRejected = errors.New("destination rejected by abuse guard")

type hy2StreamUsers struct {
	users sync.Map
}

func (t *hy2StreamUsers) Store(stream server.HyStream, user string) {
	t.users.Store(stream, user)
}

func (t *hy2StreamUsers) Load(stream server.HyStream) (string, bool) {
	value, ok := t.users.Load(stream)
	if !ok {
		return "", false
	}
	return value.(string), true
}

func (t *hy2StreamUsers) Delete(stream server.HyStream) {
	t.users.Delete(stream)
}

type policyRequestHook struct {
	tag       string
	delegate  server.RequestHook
	autoSniff server.RequestHook
	streams   *hy2StreamUsers
	logger    *zap.Logger
}

func newPolicyRequestHook(tag string, delegate server.RequestHook, streams *hy2StreamUsers, logger *zap.Logger) *policyRequestHook {
	return &policyRequestHook{
		tag:      tag,
		delegate: delegate,
		autoSniff: &sniff.Sniffer{
			Timeout: 2 * time.Second,
		},
		streams: streams,
		logger:  logger,
	}
}

func (h *policyRequestHook) Check(isUDP bool, reqAddr string) bool {
	if h.delegate != nil && h.delegate.Check(isUDP, reqAddr) {
		return true
	}
	limit, err := limiter.GetLimiter(h.tag)
	if err != nil {
		return true
	}
	if limit.AbuseGuardEnabled() || len(limit.ProtocolRulesSnapshot()) > 0 {
		return true
	}
	return limit.HasDomainRules() && h.autoSniff.Check(isUDP, reqAddr)
}

func (h *policyRequestHook) TCP(stream server.HyStream, reqAddr *string) ([]byte, error) {
	limit, err := limiter.GetLimiter(h.tag)
	if err != nil {
		return nil, fmt.Errorf("get limiter for %s: %w", h.tag, err)
	}

	putback, err := h.runTCPDelegate(stream, reqAddr, limit)
	if err != nil {
		return putback, err
	}
	user, hasUser := h.streams.Load(stream)
	if err := h.enforcePanelRules(limit, user, "tcp", *reqAddr, putback); err != nil {
		return putback, err
	}
	if limit.AbuseGuardEnabled() {
		if !hasUser {
			return putback, fmt.Errorf("%w: missing authenticated stream owner", errAbuseGuardRejected)
		}
		decision := limit.CheckOutbound(format.UserTag(h.tag, user), *reqAddr)
		if decision.Reject {
			taguuid := format.UserTag(h.tag, user)
			if shouldLog, suppressed := limit.ShouldLogRejection(taguuid, "abuse:"+decision.Reason); shouldLog {
				h.logger.Warn("abuse guard rejected TCP request",
					zap.String("tag", h.tag),
					zap.String("uuid", user),
					zap.String("target", *reqAddr),
					zap.String("reason", decision.Reason),
					zap.Int("count", decision.Count),
					zap.Int("suppressed", suppressed),
					zap.Time("blocked_until", decision.BlockedUntil))
			}
			return putback, fmt.Errorf("%w: %s", errAbuseGuardRejected, decision.Reason)
		}
	}
	return putback, nil
}

func (h *policyRequestHook) UDP(data []byte, reqAddr *string) error {
	limit, err := limiter.GetLimiter(h.tag)
	if err != nil {
		return fmt.Errorf("get limiter for %s: %w", h.tag, err)
	}
	if h.delegate != nil && h.delegate.Check(true, *reqAddr) {
		if err := h.delegate.UDP(data, reqAddr); err != nil {
			return err
		}
	} else if limit.HasDomainRules() && h.autoSniff.Check(true, *reqAddr) {
		if err := h.autoSniff.UDP(data, reqAddr); err != nil {
			return err
		}
	}
	return h.enforcePanelRules(limit, "", "udp", *reqAddr, data)
}

func (h *policyRequestHook) runTCPDelegate(stream server.HyStream, reqAddr *string, limit *limiter.Limiter) ([]byte, error) {
	if h.delegate != nil && h.delegate.Check(false, *reqAddr) {
		return h.delegate.TCP(stream, reqAddr)
	}
	if limit.HasDomainRules() && h.autoSniff.Check(false, *reqAddr) {
		return h.autoSniff.TCP(stream, reqAddr)
	}
	if limit.HasProtocolRule("bittorrent") {
		return readTCPPrefix(stream, 3, 2*time.Second)
	}
	return nil, nil
}

func (h *policyRequestHook) enforcePanelRules(limit *limiter.Limiter, user, network, reqAddr string, firstPayload []byte) error {
	target := destinationHost(reqAddr)
	protocol := ""
	if looksLikeBitTorrent(network, firstPayload) {
		protocol = "bittorrent"
	}
	reason, reject := limit.CheckRoute(network, reqAddr, protocol)
	if !reject {
		return nil
	}
	if user != "" {
		taguuid := format.UserTag(h.tag, user)
		if shouldLog, suppressed := limit.ShouldLogRejection(taguuid, reason); shouldLog {
			h.logger.Warn("routing policy rejected request",
				zap.String("tag", h.tag),
				zap.String("uuid", user),
				zap.String("network", network),
				zap.String("target", target),
				zap.String("reason", reason),
				zap.Int("suppressed", suppressed))
		}
	}
	if strings.HasPrefix(reason, limiter.RouteReasonSafety+":") {
		return fmt.Errorf("%w: %s", errSafetyRuleRejected, reason)
	}
	if reason == limiter.RouteReasonPanelDomain {
		return fmt.Errorf("%w: %s", errPanelRuleRejected, target)
	}
	return fmt.Errorf("%w: %s", errPanelRuleRejected, reason)
}

func readTCPPrefix(stream server.HyStream, size int, timeout time.Duration) ([]byte, error) {
	if err := stream.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	defer stream.SetReadDeadline(time.Time{})
	buffer := make([]byte, size)
	n, err := io.ReadFull(stream, buffer)
	if err != nil {
		return buffer[:n], nil
	}
	return buffer, nil
}

func looksLikeBitTorrent(network string, payload []byte) bool {
	if network == "tcp" {
		return len(payload) >= 3 && payload[0] == 19 && payload[1] == 'B' && payload[2] == 'i'
	}
	if len(payload) >= 8 && bytes.Equal(payload[:8], []byte{0x00, 0x00, 0x04, 0x17, 0x27, 0x10, 0x19, 0x80}) {
		return true
	}
	return len(payload) > 10 && payload[0] == 'd' &&
		(bytes.Contains(payload, []byte("1:y1:q")) || bytes.Contains(payload, []byte("9:get_peers")) ||
			bytes.Contains(payload, []byte("13:announce_peer")))
}

func destinationHost(reqAddr string) string {
	host, _, err := net.SplitHostPort(reqAddr)
	if err != nil {
		return reqAddr
	}
	return host
}
