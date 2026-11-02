package hy2

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/limiter"
	"github.com/apernet/hysteria/core/v2/server"
	"github.com/apernet/hysteria/extras/v2/masq"
	"go.uber.org/zap"
)

func TestCallbacksAfterLimiterRemoval(t *testing.T) {
	limiter.Init()
	logger := &serverLogger{Tag: "removed-node", logger: zap.NewNop()}
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}
	for name, callback := range map[string]func(){
		"connect": func() { logger.Connect(addr, "former-user", 0) },
		"tcp":     func() { logger.TCPRequest(addr, "former-user", "example.com:443") },
		"udp":     func() { logger.UDPRequest(addr, "former-user", 1, "example.com:443") },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if panicValue := recover(); panicValue != nil {
					t.Errorf("late callback panicked after limiter removal: %v", panicValue)
				}
			}()
			callback()
		})
	}
	if len(logger.devices) != 0 {
		t.Fatal("late callback recreated device state")
	}
}

func masqueradeTestConfig(t *testing.T, handler http.Handler) *masq.MasqTCPServer {
	t.Helper()
	certServer := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certServer.Close)
	return &masq.MasqTCPServer{QUICPort: 18443, Handler: handler, TLSConfig: &tls.Config{Certificates: certServer.TLS.Certificates}}
}

func TestMasqueradeServersCloseAndRebind(t *testing.T) {
	config := masqueradeTestConfig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Alt-Svc", "unrelated-upstream-value")
		_, _ = io.WriteString(w, "masquerade-ok")
	}))
	group, err := startMasqueradeServers(config, "127.0.0.1:0", "127.0.0.1:0", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = group.Close() })
	// The client trusts the test certificate, not arbitrary server certificates.
	poolServer := httptest.NewTLSServer(http.NotFoundHandler())
	defer poolServer.Close()
	client := poolServer.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs.AddCert(config.TLSConfig.Certificates[0].Leaf)
	client.Transport = transport
	client.Timeout = 3 * time.Second
	defer transport.CloseIdleConnections()
	for index, scheme := range []string{"http", "https"} {
		response, err := client.Get(scheme + "://" + group.listeners[index].Addr().String() + "/test")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != "masquerade-ok" || response.Header.Get("Alt-Svc") != `h3=":18443"; ma=2592000` {
			t.Fatalf("incorrect %s response: %q, %v, %v", scheme, body, response.Header, err)
		}
	}
	addresses := []string{group.listeners[0].Addr().String(), group.listeners[1].Addr().String()}
	if err := group.Close(); err != nil {
		t.Fatal(err)
	}
	if err := group.Close(); err != nil {
		t.Fatal("cleanup must be idempotent", err)
	}
	for _, address := range addresses {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatalf("masquerade port retained after close: %v", err)
		}
		listener.Close()
	}
}

func TestMasqueradePartialBindClosesReservedPort(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	reserved.Close()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	config := masqueradeTestConfig(t, http.NotFoundHandler())
	if group, err := startMasqueradeServers(config, address, occupied.Addr().String(), zap.NewNop()); err == nil {
		group.Close()
		t.Fatal("partial bind unexpectedly succeeded")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("first reserved port leaked on second bind failure: %v", err)
	}
	listener.Close()
}

func TestMasqueradeCloseClosesHijackedConnection(t *testing.T) {
	hijacked := make(chan net.Conn, 1)
	config := masqueradeTestConfig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, writer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_, _ = writer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n")
		_ = writer.Flush()
		hijacked <- conn
	}))
	group, err := startMasqueradeServers(config, "127.0.0.1:0", "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer group.Close()
	client, err := net.DialTimeout("tcp", group.listeners[0].Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = io.WriteString(client, "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n")
	reader := bufio.NewReader(client)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	select {
	case serverConn := <-hijacked:
		defer serverConn.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not hijack connection")
	}
	if err := group.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("hijacked connection survived node cleanup")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("cleanup did not close hijacked socket before deadline")
	}
	group.listeners[0].mu.Lock()
	remaining := len(group.listeners[0].connections)
	group.listeners[0].mu.Unlock()
	if remaining != 0 {
		t.Fatal("cleanup retained accepted connection references")
	}
}

func TestMasqueradeRedirectPreservesPathAndIPv6(t *testing.T) {
	config := &masq.MasqTCPServer{HTTPSPort: 8443, ForceHTTPS: true}
	for _, host := range []string{"[2001:db8::1]", "[2001:db8::1]:80"} {
		request := httptest.NewRequest(http.MethodGet, "http://example.test/a%20b?q=1", nil)
		request.Host = host
		response := httptest.NewRecorder()
		masqueradeHTTPHandler(config, false).ServeHTTP(response, request)
		if response.Code != http.StatusMovedPermanently || !strings.HasPrefix(response.Header().Get("Location"), "https://[2001:db8::1]:8443/a%20b?q=1") {
			t.Fatalf("bad redirect for %s: %s", host, response.Header().Get("Location"))
		}
	}
}

// Run this in a subprocess so the old zap.Fatal cannot kill the parent suite.
func TestMasqueradeConflictIsReturned(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestMasqueradeConflictChild$", "-test.timeout=10s")
	cmd.Env = append(os.Environ(), "V2BX_MASQ_CONFLICT_HELPER=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("masquerade conflict must return an error without exiting: %v\n%s", err, output)
	}
}

func TestMasqueradeConflictChild(t *testing.T) {
	if os.Getenv("V2BX_MASQ_CONFLICT_HELPER") != "1" {
		t.Skip("internal subprocess helper")
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	config := &serverConfig{}
	config.Masquerade.ListenHTTPS = occupied.Addr().String()
	node := &Hysteria2node{Logger: zap.NewNop()}
	if handler, err := node.getMasqHandler(&server.TLSConfig{}, udp, config); err == nil {
		_ = handler
		t.Fatal("occupied masquerade listener was incorrectly accepted")
	}
}
