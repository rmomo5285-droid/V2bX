package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"encoding/json"
)

// Security type
const (
	None    = 0
	Tls     = 1
	Reality = 2
)

type NodeInfo struct {
	Id           int
	Type         string
	Security     int
	PushInterval time.Duration
	PullInterval time.Duration
	RawDNS       RawDNS
	Rules        Rules

	// origin
	VAllss      *VAllssNode
	Shadowsocks *ShadowsocksNode
	Trojan      *TrojanNode
	Tuic        *TuicNode
	AnyTls      *AnyTlsNode
	Hysteria    *HysteriaNode
	Hysteria2   *Hysteria2Node
	Common      *CommonNode
}

type CommonNode struct {
	Host       string      `json:"host"`
	ServerPort int         `json:"server_port"`
	ServerName string      `json:"server_name"`
	Routes     []Route     `json:"routes"`
	BaseConfig *BaseConfig `json:"base_config"`
}

type Route struct {
	Id          int         `json:"id"`
	Match       interface{} `json:"match"`
	Action      string      `json:"action"`
	ActionValue string      `json:"action_value"`
}
type BaseConfig struct {
	PushInterval any `json:"push_interval"`
	PullInterval any `json:"pull_interval"`
}

// VAllssNode is vmess and vless node info
type VAllssNode struct {
	CommonNode
	Tls                 int             `json:"tls"`
	TlsSettings         TlsSettings     `json:"tls_settings"`
	TlsSettingsBack     *TlsSettings    `json:"tlsSettings"`
	Network             string          `json:"network"`
	NetworkSettings     json.RawMessage `json:"network_settings"`
	NetworkSettingsBack json.RawMessage `json:"networkSettings"`
	Encryption          string          `json:"encryption"`
	EncryptionSettings  EncSettings     `json:"encryption_settings"`
	ServerName          string          `json:"server_name"`

	// vless only
	Flow          string        `json:"flow"`
	RealityConfig RealityConfig `json:"-"`
}

type TlsSettings struct {
	ServerName  string `json:"server_name"`
	Dest        string `json:"dest"`
	ServerPort  string `json:"server_port"`
	ShortId     string `json:"short_id"`
	PrivateKey  string `json:"private_key"`
	Mldsa65Seed string `json:"mldsa65Seed"`
	Xver        uint64 `json:"xver,string"`
}

type EncSettings struct {
	Mode          string `json:"mode"`
	Ticket        string `json:"ticket"`
	ServerPadding string `json:"server_padding"`
	PrivateKey    string `json:"private_key"`
}

type RealityConfig struct {
	Xver         uint64 `json:"Xver"`
	MinClientVer string `json:"MinClientVer"`
	MaxClientVer string `json:"MaxClientVer"`
	MaxTimeDiff  string `json:"MaxTimeDiff"`
}

type ShadowsocksNode struct {
	CommonNode
	Cipher    string `json:"cipher"`
	ServerKey string `json:"server_key"`
}

type TrojanNode struct {
	CommonNode
	Network         string          `json:"network"`
	NetworkSettings json.RawMessage `json:"networkSettings"`
}

type TuicNode struct {
	CommonNode
	CongestionControl string `json:"congestion_control"`
	ZeroRTTHandshake  bool   `json:"zero_rtt_handshake"`
}

type AnyTlsNode struct {
	CommonNode
	PaddingScheme []string `json:"padding_scheme,omitempty"`
}

type HysteriaNode struct {
	CommonNode
	UpMbps   int    `json:"up_mbps"`
	DownMbps int    `json:"down_mbps"`
	Obfs     string `json:"obfs"`
}

type Hysteria2Node struct {
	CommonNode
	Ignore_Client_Bandwidth bool   `json:"ignore_client_bandwidth"`
	UpMbps                  int    `json:"up_mbps"`
	DownMbps                int    `json:"down_mbps"`
	ObfsType                string `json:"obfs"`
	ObfsPassword            string `json:"obfs-password"`
}

type RawDNS struct {
	DNSMap  map[string]map[string]interface{}
	DNSJson []byte
}

type Rules struct {
	Regexp   []string
	Protocol []string
}

func routeMatches(match interface{}) ([]string, error) {
	var matches []string
	switch value := match.(type) {
	case string:
		matches = strings.Split(value, ",")
	case []string:
		matches = append(matches, value...)
	case []interface{}:
		matches = make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("route match must contain strings, got %T", item)
			}
			matches = append(matches, text)
		}
	default:
		return nil, fmt.Errorf("route match must be a string or string array, got %T", match)
	}

	result := matches[:0]
	for _, match := range matches {
		if match = strings.TrimSpace(match); match != "" {
			result = append(result, match)
		}
	}
	return result, nil
}

func normalizeDomainRule(match string) (string, error) {
	match = strings.TrimSpace(match)
	if strings.HasPrefix(match, "regexp:") {
		pattern := strings.TrimSpace(strings.TrimPrefix(match, "regexp:"))
		if pattern == "" {
			return "", fmt.Errorf("empty regexp rule")
		}
		return pattern, nil
	}

	match = strings.TrimPrefix(match, "domain:")
	match = strings.TrimSpace(match)
	match = strings.TrimPrefix(match, "*.")
	match = strings.Trim(match, ".")
	if match == "" {
		return "", fmt.Errorf("empty domain rule")
	}
	if ip := net.ParseIP(match); ip != nil {
		return "^" + regexp.QuoteMeta(ip.String()) + "$", nil
	}
	match = strings.ToLower(match)
	return `(?i)(?:^|\.)` + regexp.QuoteMeta(match) + `\.?$`, nil
}

func appendBlockRules(rules *Rules, matches []string) error {
	domains := make(map[string]struct{}, len(rules.Regexp))
	protocols := make(map[string]struct{}, len(rules.Protocol))
	for _, rule := range rules.Regexp {
		domains[rule] = struct{}{}
	}
	for _, protocol := range rules.Protocol {
		protocols[protocol] = struct{}{}
	}

	for _, match := range matches {
		if strings.HasPrefix(match, "protocol:") {
			protocol := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(match, "protocol:")))
			if protocol == "" {
				return fmt.Errorf("empty protocol rule")
			}
			if _, exists := protocols[protocol]; !exists {
				rules.Protocol = append(rules.Protocol, protocol)
				protocols[protocol] = struct{}{}
			}
			continue
		}
		pattern, err := normalizeDomainRule(match)
		if err != nil {
			return err
		}
		if _, exists := domains[pattern]; !exists {
			rules.Regexp = append(rules.Regexp, pattern)
			domains[pattern] = struct{}{}
		}
	}
	return nil
}

func (c *Client) GetNodeInfo() (node *NodeInfo, err error) {
	const path = "/api/v1/server/UniProxy/config"
	r, err := c.client.
		R().
		SetHeader("If-None-Match", c.nodeEtag).
		ForceContentType("application/json").
		Get(path)

	if r.StatusCode() == 304 {
		return nil, nil
	}
	hash := sha256.Sum256(r.Body())
	newBodyHash := hex.EncodeToString(hash[:])
	if c.responseBodyHash == newBodyHash {
		return nil, nil
	}
	c.responseBodyHash = newBodyHash
	c.nodeEtag = r.Header().Get("ETag")
	if err = c.checkResponse(r, path, err); err != nil {
		return nil, err
	}

	if r != nil {
		defer func() {
			if r.RawBody() != nil {
				r.RawBody().Close()
			}
		}()
	} else {
		return nil, fmt.Errorf("received nil response")
	}
	node = &NodeInfo{
		Id:   c.NodeId,
		Type: c.NodeType,
		RawDNS: RawDNS{
			DNSMap:  make(map[string]map[string]interface{}),
			DNSJson: []byte(""),
		},
	}
	// parse protocol params
	var cm *CommonNode
	switch c.NodeType {
	case "vmess", "vless":
		rsp := &VAllssNode{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode v2ray params error: %s", err)
		}
		if len(rsp.NetworkSettingsBack) > 0 {
			rsp.NetworkSettings = rsp.NetworkSettingsBack
			rsp.NetworkSettingsBack = nil
		}
		if rsp.TlsSettingsBack != nil {
			rsp.TlsSettings = *rsp.TlsSettingsBack
			rsp.TlsSettingsBack = nil
		}
		cm = &rsp.CommonNode
		node.VAllss = rsp
		node.Security = node.VAllss.Tls
	case "shadowsocks":
		rsp := &ShadowsocksNode{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode shadowsocks params error: %s", err)
		}
		cm = &rsp.CommonNode
		node.Shadowsocks = rsp
		node.Security = None
	case "trojan":
		rsp := &TrojanNode{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode trojan params error: %s", err)
		}
		cm = &rsp.CommonNode
		node.Trojan = rsp
		node.Security = Tls
	case "tuic":
		rsp := &TuicNode{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode tuic params error: %s", err)
		}
		cm = &rsp.CommonNode
		node.Tuic = rsp
		node.Security = Tls
	case "anytls":
		rsp := &AnyTlsNode{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode anytls params error: %s", err)
		}
		cm = &rsp.CommonNode
		node.AnyTls = rsp
		node.Security = Tls
	case "hysteria":
		rsp := &HysteriaNode{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode hysteria params error: %s", err)
		}
		cm = &rsp.CommonNode
		node.Hysteria = rsp
		node.Security = Tls
	case "hysteria2":
		rsp := &Hysteria2Node{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode hysteria2 params error: %s", err)
		}
		cm = &rsp.CommonNode
		node.Hysteria2 = rsp
		node.Security = Tls
	}
	if cm == nil {
		return nil, fmt.Errorf("unsupported node type %q", c.NodeType)
	}
	if cm.BaseConfig == nil {
		return nil, fmt.Errorf("node response is missing base_config")
	}

	// parse rules and dns
	for i := range cm.Routes {
		matches, matchErr := routeMatches(cm.Routes[i].Match)
		if matchErr != nil {
			return nil, fmt.Errorf("parse route %d matches: %w", cm.Routes[i].Id, matchErr)
		}
		switch cm.Routes[i].Action {
		case "block":
			if err := appendBlockRules(&node.Rules, matches); err != nil {
				return nil, fmt.Errorf("parse route %d block rule: %w", cm.Routes[i].Id, err)
			}
		case "dns":
			var domains []string
			domains = append(domains, matches...)
			if len(matches) == 0 {
				return nil, fmt.Errorf("parse route %d dns rule: empty matches", cm.Routes[i].Id)
			}
			if matches[0] != "main" {
				node.RawDNS.DNSMap[strconv.Itoa(i)] = map[string]interface{}{
					"address": cm.Routes[i].ActionValue,
					"domains": domains,
				}
			} else {
				dns := []byte(strings.Join(matches[1:], ""))
				node.RawDNS.DNSJson = dns
			}
		}
	}

	// set interval
	node.PushInterval, err = intervalToTime(cm.BaseConfig.PushInterval)
	if err != nil {
		return nil, fmt.Errorf("parse push interval: %w", err)
	}
	node.PullInterval, err = intervalToTime(cm.BaseConfig.PullInterval)
	if err != nil {
		return nil, fmt.Errorf("parse pull interval: %w", err)
	}

	node.Common = cm
	// clear
	cm.Routes = nil
	cm.BaseConfig = nil

	return node, nil
}

func intervalToTime(value interface{}) (time.Duration, error) {
	var seconds int64
	switch value := value.(type) {
	case int:
		seconds = int64(value)
	case int64:
		seconds = value
	case float64:
		if value != float64(int64(value)) {
			return 0, fmt.Errorf("interval must be a whole number, got %v", value)
		}
		seconds = int64(value)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid interval %q: %w", value, err)
		}
		seconds = parsed
	default:
		return 0, fmt.Errorf("unsupported interval type %T", value)
	}
	if seconds <= 0 {
		return 0, fmt.Errorf("interval must be positive, got %d", seconds)
	}
	return time.Duration(seconds) * time.Second, nil
}
