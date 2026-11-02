//go:build sing && xray && hysteria2

package limiter_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/core"
	_ "github.com/InazumaV/V2bX/core/imports"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/InazumaV/V2bX/node"
	hyclient "github.com/apernet/hysteria/core/v2/client"
)

// Exercise the actual listeners, authentication, forwarding and byte counters
// through a local Xboard HTTP fixture. This makes no external network requests.
func TestThreeCoreProxyForwarding(t *testing.T) {
	limiter.Init()
	restore := limiter.AllowLoopbackForTest()
	defer restore()
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	for _, kind := range []string{"xray", "sing", "hysteria2"} {
		t.Run(kind, func(t *testing.T) {
			f := startProxyFixture(t, kind, echo.Addr().String())
			connection, err := f.dial()
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			connection.SetDeadline(time.Now().Add(5 * time.Second))
			payload := bytes.Repeat([]byte("V2bX-loopback-test"), 100)
			if _, err := connection.Write(payload); err != nil {
				t.Fatal(err)
			}
			reply := make([]byte, len(payload))
			if _, err := io.ReadFull(connection, reply); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(payload, reply) {
				t.Fatal("forwarded data corrupted")
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				rows, err := f.backend.GetUserTrafficSlice(f.tag, false)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) == 1 && rows[0].UID == 7 && rows[0].Upload >= int64(len(payload)) && rows[0].Download >= int64(len(payload)) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("missing traffic accounting: %+v", rows)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

type proxyFixture struct {
	shutdown                func()
	backend                 core.Core
	controller              *node.Controller
	tag, kind, uuid, target string
	port                    int
}

func startProxyFixture(t *testing.T, kind, target string) *proxyFixture {
	t.Helper()
	proto := "vless"
	if kind == "hysteria2" {
		proto = kind
	}
	reserve, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	reserve.Close()
	const uuid = "00112233-4455-6677-8899-aabbccddeeff"
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/config"):
			fmt.Fprintf(w, `{"server_port":%d,"network":"tcp","tls":0,"base_config":{"pull_interval":3600,"push_interval":3600}}`, port)
		case strings.HasSuffix(r.URL.Path, "/user"):
			fmt.Fprintf(w, `{"users":[{"id":7,"uuid":%q}]}`, uuid)
		case strings.HasSuffix(r.URL.Path, "/alivelist"):
			fmt.Fprint(w, `{"alive":{}}`)
		default:
			fmt.Fprint(w, `{"data":true}`)
		}
	}))
	t.Cleanup(apiServer.Close)
	cfg := conf.CoreConfig{Type: kind, XrayConfig: conf.NewXrayConfig(), SingConfig: conf.NewSingConfig(), Hysteria2Config: conf.NewHysteria2Config()}
	cfg.XrayConfig.LogConfig.AccessPath = "none"
	cfg.XrayConfig.LogConfig.Level = "error"
	// The updated Xray fork also blocks loopback in freedom by default.
	// Scope its explicit exception to this test's local outbound file.
	if kind == "xray" {
		cfg.XrayConfig.OutboundConfigPath = filepath.Join(t.TempDir(), "outbound.json")
		if err := os.WriteFile(cfg.XrayConfig.OutboundConfigPath, []byte(`[{"protocol":"freedom","tag":"test-loopback","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1/32"]}]}}]`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	backend, err := core.NewCore([]conf.CoreConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	var closeCoreOnce sync.Once
	closeCore := func() { closeCoreOnce.Do(func() { _ = backend.Close() }) }
	t.Cleanup(closeCore)
	if err := backend.Start(); err != nil {
		t.Fatal(err)
	}
	api, err := panel.New(&conf.ApiConfig{APIHost: apiServer.URL, NodeID: 1, NodeType: proto})
	if err != nil {
		t.Fatal(err)
	}
	cert := conf.NewCertConfig()
	if kind == "hysteria2" {
		cert.CertMode = "self"
		cert.CertDomain = "localhost"
		cert.CertFile = filepath.Join(t.TempDir(), "cert.pem")
		cert.KeyFile = filepath.Join(t.TempDir(), "key.pem")
	}
	tag := "test-" + kind
	controller := node.NewController(backend, api, &conf.Options{Name: tag, ListenIP: "127.0.0.1", SendIP: "0.0.0.0", Core: kind, XrayOptions: &conf.XrayOptions{DisableSniffing: true}, SingOptions: &conf.SingOptions{}, CertConfig: cert})
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	return &proxyFixture{backend: backend, controller: controller, tag: tag, kind: kind, uuid: uuid, target: target, port: port, shutdown: func() { _ = controller.Close(); closeCore(); apiServer.Close() }}
}

func (f *proxyFixture) dial() (net.Conn, error) {
	if f.kind == "hysteria2" {
		client, err := f.newHysteriaClient()
		if err != nil {
			return nil, err
		}
		conn, err := client.TCP(f.target)
		if err != nil {
			client.Close()
			return nil, err
		}
		return &hyTestConn{Conn: conn, client: client}, nil
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", f.port), 3*time.Second)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	id := []byte{0, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	header := append([]byte{0}, id...)
	header = append(header, 0, 1)
	_, port, err := net.SplitHostPort(f.target)
	if err != nil {
		conn.Close()
		return nil, err
	}
	destinationPort, err := strconv.Atoi(port)
	if err != nil {
		conn.Close()
		return nil, err
	}
	header = binary.BigEndian.AppendUint16(header, uint16(destinationPort))
	header = append(header, 1, 127, 0, 0, 1)
	if _, err := conn.Write(header); err != nil {
		conn.Close()
		return nil, err
	}
	return &vlessTestConn{Conn: conn}, nil
}

func (f *proxyFixture) newHysteriaClient() (hyclient.Client, error) {
	client, _, err := hyclient.NewClient(&hyclient.Config{ServerAddr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: f.port}, Auth: f.uuid, TLSConfig: hyclient.TLSConfig{ServerName: "localhost", InsecureSkipVerify: true}})
	return client, err
}

type hyTestConn struct {
	net.Conn
	client hyclient.Client
}

func (c *hyTestConn) Close() error { err := c.Conn.Close(); c.client.Close(); return err }

type vlessTestConn struct {
	net.Conn
	responseRead bool
}

func (c *vlessTestConn) Read(p []byte) (int, error) {
	if !c.responseRead {
		response := make([]byte, 2)
		if _, err := io.ReadFull(c.Conn, response); err != nil {
			return 0, err
		}
		if response[0] != 0 || response[1] != 0 {
			return 0, fmt.Errorf("invalid VLESS response %v", response)
		}
		c.responseRead = true
	}
	return c.Conn.Read(p)
}
