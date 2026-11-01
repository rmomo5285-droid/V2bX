package accesslog

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Config is optional. Ingest endpoints accept JSONEachRow directly; the
// clickhouse_http backend constructs an INSERT for a configurable database/table.
type Config struct {
	Enabled        bool
	Backend        string
	Endpoint       string
	Database       string
	Table          string
	Username       string
	Password       string `json:",omitzero"`
	PasswordFile   string `json:",omitzero"`
	QueueCapacity  int
	QueueMaxBytes  int
	BatchSize      int
	BatchMaxBytes  int
	FlushInterval  string
	RequestTimeout string
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (c Config) Prepare() (Config, error) {
	if !c.Enabled {
		return c, nil
	}
	if c.Backend == "" {
		c.Backend = "ingest"
	}
	if c.Backend != "ingest" && c.Backend != "clickhouse_http" {
		return c, fmt.Errorf("unsupported access log backend")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return c, fmt.Errorf("invalid access log endpoint (no URL credentials or fragment allowed)")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())) {
		return c, fmt.Errorf("access log endpoint requires HTTPS (HTTP allowed only on loopback)")
	}
	if c.Backend == "clickhouse_http" {
		if c.Database == "" {
			c.Database = "default"
		}
		if c.Table == "" {
			c.Table = "access_log"
		}
		if !identifier.MatchString(c.Database) || !identifier.MatchString(c.Table) {
			return c, fmt.Errorf("invalid access log database/table identifier")
		}
		q := u.Query()
		q.Set("query", "INSERT INTO "+c.Database+"."+c.Table+" FORMAT JSONEachRow")
		q.Set("async_insert", "1")
		q.Set("wait_for_async_insert", "1")
		q.Set("wait_end_of_query", "1")
		u.RawQuery = q.Encode()
	} else if u.RawQuery != "" {
		return c, fmt.Errorf("ingest endpoint must not contain a query string")
	}
	c.Endpoint = u.String()
	if c.Username == "" || len(c.Username) > 128 || strings.ContainsAny(c.Username, "\r\n") {
		return c, fmt.Errorf("invalid access log username")
	}
	if c.PasswordFile != "" {
		if c.Password != "" {
			return c, fmt.Errorf("use either Password or PasswordFile")
		}
		f, err := os.Open(c.PasswordFile)
		if err != nil {
			return c, fmt.Errorf("open access log password file: %w", err)
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, 4097))
		if err != nil || len(b) > 4096 {
			return c, fmt.Errorf("invalid access log password file")
		}
		c.Password = strings.TrimRight(string(b), "\r\n")
	}
	if c.Password == "" || len(c.Password) > 4096 || strings.ContainsAny(c.Password, "\r\n") {
		return c, fmt.Errorf("invalid access log password")
	}
	if c.QueueCapacity == 0 {
		c.QueueCapacity = 256
	}
	if c.QueueMaxBytes == 0 {
		c.QueueMaxBytes = 256 << 10
	}
	if c.BatchSize == 0 {
		c.BatchSize = 64
	}
	if c.BatchMaxBytes == 0 {
		c.BatchMaxBytes = 64 << 10
	}
	if c.QueueCapacity < 1 || c.QueueCapacity > 8192 || c.QueueMaxBytes < 4096 || c.QueueMaxBytes > 8<<20 || c.BatchSize < 1 || c.BatchSize > 512 || c.BatchMaxBytes < 4096 || c.BatchMaxBytes > 64<<10 {
		return c, fmt.Errorf("access log queue/batch limits out of range")
	}
	if c.FlushInterval == "" {
		c.FlushInterval = "5s"
	}
	if c.RequestTimeout == "" {
		c.RequestTimeout = "10s"
	}
	for name, value := range map[string]string{"FlushInterval": c.FlushInterval, "RequestTimeout": c.RequestTimeout} {
		d, err := time.ParseDuration(value)
		if err != nil || d < 100*time.Millisecond || d > time.Minute {
			return c, fmt.Errorf("invalid access log %s", name)
		}
	}
	return c, nil
}
