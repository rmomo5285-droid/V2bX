package panel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/InazumaV/V2bX/conf"
	"github.com/go-resty/resty/v2"
)

// Panel is the interface for different panel's api.

type Client struct {
	requestContext   context.Context
	cancelRequests   context.CancelFunc
	client           *resty.Client
	APIHost          string
	APISendIP        string
	Token            string
	NodeType         string
	NodeId           int
	nodeEtag         string
	userEtag         string
	responseBodyHash string
	UserList         *UserListBody
	AliveMap         *AliveMap
	xboardV2         bool
}

func New(c *conf.ApiConfig) (*Client, error) {
	protocol := strings.ToLower(strings.TrimSpace(c.APIProtocol))
	if protocol != "" && protocol != "uniproxy" && protocol != "xboard-v2" {
		return nil, fmt.Errorf("unsupported ApiProtocol %q", c.APIProtocol)
	}
	if c.MachineID < 0 || (c.MachineID > 0 && protocol == "uniproxy") {
		return nil, errors.New("MachineID requires Xboard v2 authentication")
	}
	xboardV2 := protocol == "xboard-v2" || c.MachineID > 0
	var client *resty.Client
	if c.APISendIP != "" {
		client = resty.NewWithLocalAddr(&net.TCPAddr{
			IP: net.ParseIP(c.APISendIP),
		})
	} else {
		client = resty.New()
	}
	client.SetRetryCount(3)
	client.SetRedirectPolicy(resty.NoRedirectPolicy())
	if c.Timeout > 0 {
		client.SetTimeout(time.Duration(c.Timeout) * time.Second)
	} else {
		client.SetTimeout(5 * time.Second)
	}
	client.OnError(func(req *resty.Request, err error) {
		var v *resty.ResponseError
		if errors.As(err, &v) {
			logrus.Error(redactSensitiveText(v.Err.Error(), c.Key))
		}
	})
	client.SetBaseURL(c.APIHost)
	// Check node type
	c.NodeType = strings.ToLower(c.NodeType)
	switch c.NodeType {
	case "v2ray":
		c.NodeType = "vmess"
	case
		"vmess",
		"trojan",
		"shadowsocks",
		"hysteria",
		"hysteria2",
		"tuic",
		"anytls",
		"vless":
	default:
		return nil, fmt.Errorf("unsupported Node type: %s", c.NodeType)
	}
	// set params
	params := map[string]string{
		"node_id": strconv.Itoa(c.NodeID),
		"token":   c.Key,
	}
	if !xboardV2 {
		params["node_type"] = c.NodeType
	}
	if c.MachineID > 0 {
		params["machine_id"] = strconv.Itoa(c.MachineID)
	}
	client.SetQueryParams(params)
	requestContext, cancelRequests := context.WithCancel(context.Background())
	return &Client{
		requestContext: requestContext,
		cancelRequests: cancelRequests,
		client:         client,
		Token:          c.Key,
		APIHost:        c.APIHost,
		APISendIP:      c.APISendIP,
		NodeType:       c.NodeType,
		NodeId:         c.NodeID,
		UserList:       &UserListBody{},
		AliveMap:       &AliveMap{},
		xboardV2:       xboardV2,
	}, nil
}

func (c *Client) request() *resty.Request {
	r := c.client.R()
	if c.requestContext != nil {
		r.SetContext(c.requestContext)
	}
	// A POST may already have been billed when its response is lost.
	// Retry read requests only; POST ambiguity remains visible to the caller.
	r.AddRetryCondition(func(_ *resty.Response, err error) bool {
		return r.Method == "GET" && err != nil
	})
	return r
}

// CloseRequests interrupts in-flight panel requests before controller teardown.
func (c *Client) CloseRequests() {
	if c != nil && c.cancelRequests != nil {
		c.cancelRequests()
	}
}

func (c *Client) apiPath(endpoint string) string {
	if c.xboardV2 {
		return "/api/v2/server/" + endpoint
	}
	return "/api/v1/server/UniProxy/" + endpoint
}

// Retry the full snapshot when applying an otherwise valid response failed.
func (c *Client) InvalidateNodeCache() {
	c.nodeEtag = ""
	c.responseBodyHash = ""
}

func (c *Client) InvalidateUserCache() {
	c.userEtag = ""
}
