package hy2

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/apernet/hysteria/extras/v2/correctnet"
	"github.com/apernet/hysteria/extras/v2/masq"
	"go.uber.org/zap"
)

// Own listeners and accepted sockets, including HTTP upgrade/hijacked sockets.
// Reserve every port synchronously so a partial bind cannot become a live node.
type masqueradeServers struct {
	servers   []*http.Server
	listeners []*masqueradeListener
	once      sync.Once
	wait      sync.WaitGroup
	err       error
}

func startMasqueradeServers(config *masq.MasqTCPServer, httpAddr, httpsAddr string, logger *zap.Logger) (*masqueradeServers, error) {
	group := &masqueradeServers{}
	addresses := []string{httpAddr, httpsAddr}
	for _, addr := range addresses {
		if addr == "" {
			group.listeners = append(group.listeners, nil)
			continue
		}
		listener, err := correctnet.Listen("tcp", addr)
		if err != nil {
			_ = group.Close()
			return nil, fmt.Errorf("bind masquerade listener %s: %w", addr, err)
		}
		group.listeners = append(group.listeners, &masqueradeListener{Listener: listener, connections: make(map[*masqueradeConnection]struct{})})
	}
	if group.listeners[1] != nil {
		config.HTTPSPort = extractPortFromAddr(group.listeners[1].Addr().String())
	}
	for index, listener := range group.listeners {
		if listener == nil {
			continue
		}
		secure := index == 1
		server := &http.Server{
			Handler:           masqueradeHTTPHandler(config, secure),
			TLSConfig:         config.TLSConfig,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       time.Minute,
		}
		group.servers = append(group.servers, server)
		group.wait.Add(1)
		go func() {
			defer group.wait.Done()
			defer listener.Close()
			var err error
			if secure {
				err = server.ServeTLS(listener, "", "")
			} else {
				err = server.Serve(listener)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				logger.Error("masquerade listener stopped", zap.Error(err))
			}
		}()
	}
	return group, nil
}

func (g *masqueradeServers) Close() error {
	g.once.Do(func() {
		var failures []error
		for _, server := range g.servers {
			if err := server.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				failures = append(failures, err)
			}
		}
		for _, listener := range g.listeners {
			if listener != nil {
				if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
					failures = append(failures, err)
				}
			}
		}
		g.wait.Wait()
		g.err = errors.Join(failures...)
	})
	return g.err
}

func masqueradeHTTPHandler(config *masq.MasqTCPServer, secure bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !secure && config.ForceHTTPS {
			host := r.Host
			if hostname, _, err := net.SplitHostPort(host); err == nil {
				host = hostname
			}
			host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
			if config.HTTPSPort != 443 && config.HTTPSPort != 0 {
				host = net.JoinHostPort(host, strconv.Itoa(config.HTTPSPort))
			} else if net.ParseIP(host) != nil && net.ParseIP(host).To4() == nil {
				host = "[" + host + "]"
			}
			target := url.URL{Scheme: "https", Host: host, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery, ForceQuery: r.URL.ForceQuery}
			http.Redirect(w, r, target.String(), http.StatusMovedPermanently)
			return
		}
		value := fmt.Sprintf(`h3=":%d"; ma=2592000`, config.QUICPort)
		w.Header().Set("Alt-Svc", value)
		wrapped := &masqueradeResponseWriter{ResponseWriter: w, altSvc: value}
		if hijacker, ok := w.(http.Hijacker); ok {
			config.Handler.ServeHTTP(&masqueradeHijackWriter{masqueradeResponseWriter: wrapped, hijacker: hijacker}, r)
		} else {
			config.Handler.ServeHTTP(wrapped, r)
		}
	})
}

type masqueradeResponseWriter struct {
	http.ResponseWriter
	altSvc string
}

func (w *masqueradeResponseWriter) WriteHeader(code int) {
	w.Header().Set("Alt-Svc", w.altSvc)
	w.ResponseWriter.WriteHeader(code)
}

func (w *masqueradeResponseWriter) Write(data []byte) (int, error) {
	w.Header().Set("Alt-Svc", w.altSvc)
	return w.ResponseWriter.Write(data)
}

func (w *masqueradeResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *masqueradeResponseWriter) Flush() {
	w.Header().Set("Alt-Svc", w.altSvc)
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

type masqueradeHijackWriter struct {
	*masqueradeResponseWriter
	hijacker http.Hijacker
}

func (w *masqueradeHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijacker.Hijack()
}

type masqueradeListener struct {
	net.Listener
	mu          sync.Mutex
	closed      bool
	connections map[*masqueradeConnection]struct{}
}

func (l *masqueradeListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	tracked := &masqueradeConnection{Conn: conn, listener: l}
	l.connections[tracked] = struct{}{}
	return tracked, nil
}

func (l *masqueradeListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	connections := make([]*masqueradeConnection, 0, len(l.connections))
	for conn := range l.connections {
		connections = append(connections, conn)
	}
	l.mu.Unlock()
	err := l.Listener.Close()
	for _, conn := range connections {
		_ = conn.Close()
	}
	return err
}

type masqueradeConnection struct {
	net.Conn
	listener *masqueradeListener
	once     sync.Once
	err      error
}

func (c *masqueradeConnection) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.listener.mu.Lock()
		delete(c.listener.connections, c)
		c.listener.mu.Unlock()
	})
	return c.err
}
