package panel

import (
	"encoding/json"
	"fmt"
	"github.com/go-resty/resty/v2"
	"net/url"
	path2 "path"
	"regexp"
	"strings"
)

var sensitiveQueryParameter = regexp.MustCompile(`(?i)([?&](?:token|api[_-]?key)=)[^&\s]+`)

// Debug set the client debug for client
func (c *Client) Debug() {
	c.client.SetDebug(true)
}

func (c *Client) assembleURL(path string) string {
	return path2.Join(c.APIHost + path)
}
func (c *Client) checkResponse(res *resty.Response, path string, err error) error {
	if err != nil {
		return fmt.Errorf("request %s failed: %s", c.assembleURL(path), redactSensitiveText(err.Error(), c.Token))
	}
	if res == nil {
		return fmt.Errorf("request %s failed: empty response", c.assembleURL(path))
	}
	if (res.StatusCode() < 200 || res.StatusCode() >= 300) && res.StatusCode() != 304 {
		body := res.Body()
		return fmt.Errorf("request %s failed: %s", c.assembleURL(path), redactSensitiveText(string(body), c.Token))
	}
	return nil
}

func (c *Client) checkAcknowledgement(res *resty.Response, path string, err error) error {
	if err := c.checkResponse(res, path, err); err != nil {
		return err
	}
	var ack struct {
		Data bool `json:"data"`
	}
	if err := json.Unmarshal(res.Body(), &ack); err != nil || !ack.Data {
		return fmt.Errorf("request %s failed: missing successful acknowledgement", c.assembleURL(path))
	}
	return nil
}

func redactSensitiveText(text, token string) string {
	text = sensitiveQueryParameter.ReplaceAllString(text, `${1}[REDACTED]`)
	if token == "" {
		return text
	}
	text = strings.ReplaceAll(text, token, "[REDACTED]")
	if escaped := url.QueryEscape(token); escaped != token {
		text = strings.ReplaceAll(text, escaped, "[REDACTED]")
	}
	return text
}
