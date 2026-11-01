package hy2

import (
	"net"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/limiter"
)

var authSequence atomic.Uint64

const sessionSeparator = "#v2bx-session#"

// Hysteria passes AuthID to every lifecycle callback. Give each authenticated
// QUIC connection an internal identity so a rebinding/migration or a rejected
// second connection cannot remove the first connection's device lease.
// All accounting, user lookup and policy hooks resolve it back to the UUID.
func newAuthID(uuid string) string {
	return uuid + sessionSeparator + strconv.FormatUint(authSequence.Add(1), 10)
}
func authUser(id string) string {
	if i := strings.LastIndex(id, sessionSeparator); i >= 0 {
		return id[:i]
	}
	return id
}

type hyDevice struct {
	ip    string
	lease *limiter.DeviceLease
}

func (l *serverLogger) trackDevice(authID, uuid string, addr net.Addr, limit *limiter.Limiter, create bool) {
	ip := extractIPFromAddr(addr)
	l.devicesMu.Lock()
	defer l.devicesMu.Unlock()
	if old, ok := l.devices[authID]; ok {
		if old.ip == ip {
			return
		}
		old.lease.Close()
	} else if !create {
		return
	}
	if l.devices == nil {
		l.devices = make(map[string]hyDevice)
	}
	l.devices[authID] = hyDevice{ip: ip, lease: limit.AcquireDevice(format.UserTag(l.Tag, uuid), ip)}
}
