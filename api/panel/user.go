package panel

import (
	"fmt"
	"strings"

	"encoding/json"
	"io"

	"github.com/vmihailenco/msgpack/v5"
)

type OnlineUser struct {
	UID int
	IP  string
}

type UserInfo struct {
	Id          int    `json:"id" msgpack:"id"`
	Uuid        string `json:"uuid" msgpack:"uuid"`
	SpeedLimit  int    `json:"speed_limit" msgpack:"speed_limit"`
	DeviceLimit int    `json:"device_limit" msgpack:"device_limit"`
}

type UserListBody struct {
	Users []UserInfo `json:"users" msgpack:"users"`
}

type AliveMap struct {
	Alive map[int]int `json:"alive"`
}

// GetUserList will pull user from v2board
func (c *Client) GetUserList() ([]UserInfo, error) {
	path := c.apiPath("user")
	r, err := c.request().
		SetHeader("If-None-Match", c.userEtag).
		SetHeader("X-Response-Format", "msgpack").
		SetDoNotParseResponse(true).
		Get(path)
	if r == nil || r.RawResponse == nil {
		return nil, fmt.Errorf("received nil response or raw response")
	}
	defer r.RawResponse.Body.Close()

	if r.StatusCode() == 304 {
		return nil, nil
	}

	if err = c.checkResponse(r, path, err); err != nil {
		return nil, err
	}
	userlist := &UserListBody{}
	if strings.Contains(r.Header().Get("Content-Type"), "application/x-msgpack") {
		decoder := msgpack.NewDecoder(r.RawResponse.Body)
		if err := decoder.Decode(userlist); err != nil {
			return nil, fmt.Errorf("decode user list error: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("decode user list error: trailing MessagePack data")
		}
	} else {
		decoder := json.NewDecoder(r.RawResponse.Body)
		if err := decoder.Decode(userlist); err != nil {
			return nil, fmt.Errorf("decode user list error: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("decode user list error: trailing JSON data")
		}
	}
	// nil means unchanged only for HTTP 304. A valid empty array revokes all.
	if userlist.Users == nil {
		return nil, fmt.Errorf("decode user list error: missing or null users array")
	}
	ids, uuids := make(map[int]bool), make(map[string]bool)
	for _, user := range userlist.Users {
		if user.Id <= 0 || user.Uuid == "" || ids[user.Id] || uuids[user.Uuid] {
			return nil, fmt.Errorf("decode user list error: invalid or duplicate user identity")
		}
		ids[user.Id], uuids[user.Uuid] = true, true
	}
	c.userEtag = r.Header().Get("ETag")
	return userlist.Users, nil
}

// GetUserAlive will fetch the alive_ip count for users
func (c *Client) GetUserAlive() (map[int]int, error) {
	path := c.apiPath("alivelist")
	r, err := c.request().ForceContentType("application/json").Get(path)
	if err = c.checkResponse(r, path, err); err != nil {
		return nil, err
	}
	var alive AliveMap
	if err := json.Unmarshal(r.Body(), &alive); err != nil {
		return nil, fmt.Errorf("decode user alive list: %w", err)
	}
	if alive.Alive == nil {
		return nil, fmt.Errorf("decode user alive list: missing alive object")
	}
	c.AliveMap = &alive
	return alive.Alive, nil
}

type UserTraffic struct {
	UID      int
	Upload   int64
	Download int64
}

// ReportUserTraffic reports the user traffic
func (c *Client) ReportUserTraffic(userTraffic []UserTraffic) error {
	data := make(map[int][]int64, len(userTraffic))
	for i := range userTraffic {
		data[userTraffic[i].UID] = []int64{userTraffic[i].Upload, userTraffic[i].Download}
	}
	path := c.apiPath("push")
	r, err := c.request().
		SetBody(data).
		ForceContentType("application/json").
		Post(path)
	err = c.checkAcknowledgement(r, path, err)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) ReportNodeOnlineUsers(data *map[int][]string) error {
	path := c.apiPath("alive")
	r, err := c.request().
		SetBody(data).
		ForceContentType("application/json").
		Post(path)
	err = c.checkAcknowledgement(r, path, err)

	if err != nil {
		return err
	}

	return nil
}
