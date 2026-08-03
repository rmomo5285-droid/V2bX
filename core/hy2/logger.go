package hy2

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/limiter"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type serverLogger struct {
	Tag    string
	logger *zap.Logger
}

var logLevelMap = map[string]zapcore.Level{
	"debug": zapcore.DebugLevel,
	"info":  zapcore.InfoLevel,
	"warn":  zapcore.WarnLevel,
	"error": zapcore.ErrorLevel,
}

var logFormatMap = map[string]zapcore.EncoderConfig{
	"console": {
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		MessageKey:     "msg",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalColorLevelEncoder,
		EncodeTime:     zapcore.RFC3339TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
	},
	"json": {
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		MessageKey:     "msg",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.EpochMillisTimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
	},
}

func (l *serverLogger) Connect(addr net.Addr, uuid string, tx uint64) {
	limiterinfo, err := limiter.GetLimiter(l.Tag)
	if err != nil {
		l.logger.Panic("Get limiter error", zap.String("tag", l.Tag), zap.Error(err))
	}
	if _, r := limiterinfo.CheckLimit(format.UserTag(l.Tag, uuid), extractIPFromAddr(addr), addr.Network() == "tcp", true); r {
		if userLimit, ok := limiterinfo.UserLimitInfo.Load(format.UserTag(l.Tag, uuid)); ok {
			userLimit.(*limiter.UserLimitInfo).OverLimit.Store(true)
		}
		l.logRejection(limiterinfo, uuid, "connection_or_device_limit", "connection or device limit rejected client",
			zap.String("addr", addr.String()))
	}
	l.logger.Info("client connected", zap.String("addr", addr.String()), zap.String("uuid", uuid), zap.Uint64("tx", tx))
}

func (l *serverLogger) Disconnect(addr net.Addr, uuid string, err error) {
	l.logger.Info("client disconnected", zap.String("addr", addr.String()), zap.String("uuid", uuid), zap.Error(err))
}

func (l *serverLogger) TCPRequest(addr net.Addr, uuid, reqAddr string) {
	limiterinfo, err := limiter.GetLimiter(l.Tag)
	if err != nil {
		l.logger.Panic("Get limiter error", zap.String("tag", l.Tag), zap.Error(err))
	}
	if _, r := limiterinfo.CheckLimit(format.UserTag(l.Tag, uuid), extractIPFromAddr(addr), addr.Network() == "tcp", true); r {
		if userLimit, ok := limiterinfo.UserLimitInfo.Load(format.UserTag(l.Tag, uuid)); ok {
			userLimit.(*limiter.UserLimitInfo).OverLimit.Store(true)
		}
		l.logRejection(limiterinfo, uuid, "connection_or_device_limit", "connection or device limit rejected TCP request",
			zap.String("addr", addr.String()), zap.String("target", reqAddr))
	}
	l.logger.Debug("TCP request", zap.String("addr", addr.String()), zap.String("uuid", uuid), zap.String("reqAddr", reqAddr))
}

func (l *serverLogger) TCPError(addr net.Addr, uuid, reqAddr string, err error) {
	if reason := policyRejectionReason(err); reason != "" {
		if limiterinfo, getErr := limiter.GetLimiter(l.Tag); getErr == nil {
			l.logRejection(limiterinfo, uuid, reason, "policy rejected TCP request",
				zap.String("target", reqAddr), zap.String("reason", err.Error()))
		}
		return
	}
	if err == nil {
		l.logger.Debug("TCP closed", zap.String("addr", addr.String()), zap.String("uuid", uuid), zap.String("reqAddr", reqAddr))
	} else {
		l.logger.Debug("TCP error", zap.String("addr", addr.String()), zap.String("uuid", uuid), zap.String("reqAddr", reqAddr), zap.Error(err))
	}
}

func (l *serverLogger) UDPRequest(addr net.Addr, uuid string, sessionId uint32, reqAddr string) {
	limiterinfo, err := limiter.GetLimiter(l.Tag)
	if err != nil {
		l.logger.Panic("Get limiter error", zap.String("tag", l.Tag), zap.Error(err))
	}
	taguuid := format.UserTag(l.Tag, uuid)
	if _, r := limiterinfo.CheckLimit(taguuid, extractIPFromAddr(addr), addr.Network() == "tcp", true); r {
		if userLimit, ok := limiterinfo.UserLimitInfo.Load(format.UserTag(l.Tag, uuid)); ok {
			userLimit.(*limiter.UserLimitInfo).OverLimit.Store(true)
		}
		l.logRejection(limiterinfo, uuid, "connection_or_device_limit", "connection or device limit rejected UDP request",
			zap.String("addr", addr.String()), zap.String("target", reqAddr))
	}
	if decision := limiterinfo.CheckOutbound(taguuid, reqAddr); decision.Reject {
		if userLimit, ok := limiterinfo.UserLimitInfo.Load(taguuid); ok {
			userLimit.(*limiter.UserLimitInfo).OverLimit.Store(true)
		}
		l.logRejection(limiterinfo, uuid, "abuse:"+decision.Reason, "abuse guard rejected UDP request",
			zap.String("target", reqAddr),
			zap.String("reason", decision.Reason),
			zap.Int("count", decision.Count),
			zap.Time("blocked_until", decision.BlockedUntil))
	}
	l.logger.Debug("UDP request", zap.String("addr", addr.String()), zap.String("uuid", uuid), zap.Uint32("sessionId", sessionId), zap.String("reqAddr", reqAddr))
}

func (l *serverLogger) UDPError(addr net.Addr, uuid string, sessionId uint32, err error) {
	if reason := policyRejectionReason(err); reason != "" {
		if limiterinfo, getErr := limiter.GetLimiter(l.Tag); getErr == nil {
			l.logRejection(limiterinfo, uuid, reason, "policy rejected UDP request",
				zap.Uint32("sessionId", sessionId), zap.String("reason", err.Error()))
		}
		return
	}
	if err == nil {
		l.logger.Debug("UDP closed", zap.String("addr", addr.String()), zap.String("uuid", uuid), zap.Uint32("sessionId", sessionId))
	} else {
		l.logger.Debug("UDP error", zap.String("addr", addr.String()), zap.String("uuid", uuid), zap.Uint32("sessionId", sessionId), zap.Error(err))
	}
}

func (l *serverLogger) logRejection(limiterinfo *limiter.Limiter, uuid, reason, message string, fields ...zap.Field) {
	taguuid := format.UserTag(l.Tag, uuid)
	shouldLog, suppressed := limiterinfo.ShouldLogRejection(taguuid, reason)
	if !shouldLog {
		return
	}
	base := []zap.Field{
		zap.String("tag", l.Tag),
		zap.String("uuid", uuid),
		zap.Int("suppressed", suppressed),
	}
	l.logger.Warn(message, append(base, fields...)...)
}

func policyRejectionReason(err error) string {
	switch {
	case errors.Is(err, errSafetyRuleRejected):
		return "safety"
	case errors.Is(err, errPanelRuleRejected):
		return "panel_rule"
	case errors.Is(err, errAbuseGuardRejected):
		return "abuse"
	default:
		return ""
	}
}

func initLogger(logLevel string, logFormat string, output string) (*zap.Logger, error) {
	level, ok := logLevelMap[strings.ToLower(logLevel)]
	if !ok {
		return nil, fmt.Errorf("unsupported log level: %s", logLevel)
	}
	enc, ok := logFormatMap[strings.ToLower(logFormat)]
	if !ok {
		return nil, fmt.Errorf("unsupported log format: %s", logFormat)
	}
	outputPaths := []string{"stderr"}
	if output != "" {
		outputPaths = []string{output}
	}
	c := zap.Config{
		Level:             zap.NewAtomicLevelAt(level),
		DisableCaller:     true,
		DisableStacktrace: true,
		Encoding:          strings.ToLower(logFormat),
		EncoderConfig:     enc,
		OutputPaths:       outputPaths,
		ErrorOutputPaths:  outputPaths,
	}
	logger, err := c.Build()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %s", err)
	}
	return logger, nil
}

func extractIPFromAddr(addr net.Addr) string {
	switch v := addr.(type) {
	case *net.TCPAddr:
		return v.IP.String()
	case *net.UDPAddr:
		return v.IP.String()
	case *net.IPAddr:
		return v.IP.String()
	default:
		return ""
	}
}
