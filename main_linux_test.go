package main

import (
	"net"
	"testing"

	"golang.org/x/sys/unix"
)

// Check the actual socket protocol, not just the go.mod spelling. A future Go
// upgrade must not silently expose MPTCP listeners on an unpatched Linux host.
func TestDefaultTCPListenerDisablesMPTCP(t *testing.T) {
	t.Setenv("GODEBUG", "")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	raw, err := listener.(*net.TCPListener).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var protocol int
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		protocol, socketErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PROTOCOL)
	}); err != nil {
		t.Fatal(err)
	}
	if socketErr != nil {
		t.Fatal(socketErr)
	}
	if protocol != unix.IPPROTO_TCP {
		t.Fatalf("default listener protocol = %d, want ordinary TCP (%d)", protocol, unix.IPPROTO_TCP)
	}
}
