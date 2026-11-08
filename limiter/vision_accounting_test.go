//go:build sing && xray && hysteria2

package limiter_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/InazumaV/V2bX/node"
	xnet "github.com/xtls/xray-core/common/net"
	xcore "github.com/xtls/xray-core/core"
	xconf "github.com/xtls/xray-core/infra/conf"
)

// This is a diagnostic, not an assertion that Vision has a fixed loss ratio.
// Run each flow/splice combination in its own process: Xray's environment and
// logging settings are process-wide. The Linux workflow verifies the actual
// splice log marker; Windows results cannot establish Linux splice behavior.
func TestVisionDownloadAccounting(t *testing.T) {
	if os.Getenv("V2BX_ACCOUNTING") != "1" {
		t.Skip("opt in with V2BX_ACCOUNTING=1; localhost only")
	}
	flow := os.Getenv("V2BX_ACCOUNTING_FLOW")
	if flow != "" && flow != "xtls-rprx-vision" {
		t.Fatal("unsupported diagnostic flow")
	}
	splice := os.Getenv("XRAY_BUF_SPLICE")
	if splice != "auto" && splice != "disable" {
		t.Fatal("set XRAY_BUF_SPLICE to auto or disable")
	}
	// The dotted name takes precedence over the portable environment alias.
	t.Setenv("xray.buf.splice", splice)
	limiter.Init()
	restore := limiter.AllowLoopbackForTest()
	defer restore()
	const half = 8 * 1024 * 1024
	const uid = 7
	const uuid = "00112233-4455-6677-8899-aabbccddeeff"
	var wire atomic.Int64
	var reported atomic.Int64
	var reports atomic.Int64
	var nextOnce, finishOnce sync.Once
	next, finish := make(chan struct{}), make(chan struct{})
	written := []chan struct{}{make(chan struct{}), make(chan struct{})}
	payload := bytes.Repeat([]byte("0123456789abcdef"), half/16)
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(2*half))
		for phase := 0; phase < 2; phase++ {
			if _, err := w.Write(payload); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			close(written[phase])
			gate := next
			if phase == 1 {
				gate = finish
			}
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
	}))
	target.Listener = &accountingListener{Listener: target.Listener, written: &wire}
	target.TLS = &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13}
	target.StartTLS()
	t.Cleanup(target.Close)
	t.Cleanup(func() { nextOnce.Do(func() { close(next) }); finishOnce.Do(func() { close(finish) }) })
	certDir := t.TempDir()
	certFile, keyFile := filepath.Join(certDir, "cert.pem"), filepath.Join(certDir, "key.pem")
	key, err := x509.MarshalPKCS8PrivateKey(target.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{
		certFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: target.TLS.Certificates[0].Certificate[0]}),
		keyFile:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	reserve, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	reserve.Close()
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/config"):
			fmt.Fprintf(w, `{"server_port":%d,"network":"tcp","tls":1,"flow":%q,"base_config":{"pull_interval":3600,"push_interval":1}}`, port, flow)
		case strings.HasSuffix(r.URL.Path, "/user"):
			fmt.Fprintf(w, `{"users":[{"id":7,"uuid":%q}]}`, uuid)
		case strings.HasSuffix(r.URL.Path, "/alivelist"):
			fmt.Fprint(w, `{"alive":{}}`)
		case strings.HasSuffix(r.URL.Path, "/push"):
			var batch map[int][]int64
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch[uid]) != 2 {
				t.Error("invalid accounting push", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			reported.Add(batch[uid][1])
			reports.Add(1)
			fmt.Fprint(w, `{"data":true}`)
		default:
			fmt.Fprint(w, `{"data":true}`)
		}
	}))
	t.Cleanup(apiServer.Close)
	logFile := filepath.Join(certDir, "xray.log")
	cfg := conf.CoreConfig{Type: "xray", XrayConfig: conf.NewXrayConfig()}
	cfg.XrayConfig.LogConfig.Level = "debug"
	cfg.XrayConfig.LogConfig.AccessPath = "none"
	cfg.XrayConfig.LogConfig.ErrorPath = logFile
	cfg.XrayConfig.OutboundConfigPath = filepath.Join(certDir, "outbound.json")
	if err := os.WriteFile(cfg.XrayConfig.OutboundConfigPath, []byte(`[{"protocol":"freedom","tag":"test-loopback","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1/32"]}]}}]`), 0600); err != nil {
		t.Fatal(err)
	}
	backend, err := vcore.NewCore([]conf.CoreConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backend.Close() })
	if err := backend.Start(); err != nil {
		t.Fatal(err)
	}
	api, err := panel.New(&conf.ApiConfig{APIHost: apiServer.URL, NodeID: 1, NodeType: "vless"})
	if err != nil {
		t.Fatal(err)
	}
	cert := conf.NewCertConfig()
	cert.CertMode, cert.CertDomain, cert.CertFile, cert.KeyFile = "file", "example.com", certFile, keyFile
	const tag = "vision-accounting"
	controller := node.NewController(backend, api, &conf.Options{Name: tag, ListenIP: "127.0.0.1", SendIP: "0.0.0.0", Core: "xray", XrayOptions: &conf.XrayOptions{DisableSniffing: true}, CertConfig: cert})
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controller.Close() })
	pin := fmt.Sprintf("%x", sha256.Sum256(target.TLS.Certificates[0].Certificate[0]))
	clientJSON := fmt.Sprintf(`{"log":{"loglevel":"debug","access":"none","error":%q},"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"127.0.0.1","port":%d,"users":[{"id":%q,"encryption":"none","flow":%q}]}]},"streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverName":"example.com","pinnedPeerCertSha256":%q,"fingerprint":"chrome"}}}]}`, logFile, port, uuid, flow, pin)
	var clientSource xconf.Config
	if err := json.Unmarshal([]byte(clientJSON), &clientSource); err != nil {
		t.Fatal(err)
	}
	clientConfig, err := clientSource.Build()
	if err != nil {
		t.Fatal(err)
	}
	client, err := xcore.New(clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	destination, err := xnet.ParseDestination("tcp:" + target.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	conn, err := xcore.Dial(ctx, client, destination)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	inner := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: "example.com", MinVersion: tls.VersionTLS13})
	if err := inner.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(inner, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(inner), nil)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"os": runtime.GOOS, "flow": flow, "splice": splice, "application_download_bytes": 2 * half}
	var phases []map[string]int64
	for phase := 0; phase < 2; phase++ {
		body := make([]byte, half)
		if _, err := io.ReadFull(response.Body, body); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(body, payload) {
			t.Fatal("download payload corrupted")
		}
		select {
		case <-written[phase]:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		// Observe at least three production HTTP push opportunities while the
		// TLS response and its proxy connection are deliberately held open.
		time.Sleep(3200 * time.Millisecond)
		var buffered int64
		rows, err := backend.GetUserTrafficSlice(tag, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			buffered += row.Download
		}
		observation := map[string]int64{"target_wire_bytes": wire.Load(), "panel_download_bytes": reported.Load(), "core_buffered_download_bytes": buffered, "http_pushes": reports.Load()}
		phases = append(phases, observation)
		t.Logf("OPEN_PHASE_%d %+v", phase+1, observation)
		if flow == "" || splice == "disable" || runtime.GOOS != "linux" {
			if reported.Load() < wire.Load()-256 {
				t.Errorf("non-splice accounting delayed: panel=%d target=%d", reported.Load(), wire.Load())
			}
		}
		if phase == 0 {
			nextOnce.Do(func() { close(next) })
		}
	}
	result["open_phases"] = phases
	expected := wire.Load()
	inner.Close()
	conn.Close()
	finishOnce.Do(func() { close(finish) })
	deadline := time.Now().Add(8 * time.Second)
	for reported.Load() < expected-256 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	result["panel_download_after_client_close"] = reported.Load()
	// Closing the client-facing API is not proof that the target-facing TCP
	// read returned: splice can still be waiting for a new read or EOF. Force
	// target EOF separately to distinguish delayed accounting from lost bytes.
	target.CloseClientConnections()
	deadline = time.Now().Add(8 * time.Second)
	for reported.Load() < expected-256 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	final := reported.Load()
	result["panel_download_after_target_eof"] = final
	result["target_wire_before_close"] = expected
	result["http_pushes"] = reports.Load()
	logData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	enteredSplice := bytes.Contains(logData, []byte("CopyRawConn splice"))
	result["splice_observed"] = enteredSplice
	if runtime.GOOS == "linux" && flow != "" && splice == "auto" && !enteredSplice {
		t.Error("Vision auto diagnostic did not enter splice; result cannot validate the reported path")
	}
	if splice == "disable" && enteredSplice {
		t.Error("splice-disable control still entered splice")
	}
	if final < expected-256 || final > expected+256 {
		t.Errorf("accounting after target EOF differs: panel=%d target=%d", final, expected)
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	t.Log("ACCOUNTING_RESULT " + string(data))
	if path := os.Getenv("V2BX_ACCOUNTING_REPORT"); path != "" {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

type accountingListener struct {
	net.Listener
	written *atomic.Int64
}

func (l *accountingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &accountingConn{Conn: c, written: l.written}, nil
}

type accountingConn struct {
	net.Conn
	written *atomic.Int64
}

func (c *accountingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written.Add(int64(n))
	return n, err
}
