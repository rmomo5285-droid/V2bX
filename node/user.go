package node

import (
	"github.com/InazumaV/V2bX/api/panel"
	log "github.com/sirupsen/logrus"
)

func (c *Controller) reportUserTrafficTask() error {
	// Task replacement can leave an old callback finishing its HTTP request.
	// Serialize counter resets, pending batches and acknowledgements.
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if c.closed {
		return nil
	}
	c.trafficReportMu.Lock()
	defer c.trafficReportMu.Unlock()
	c.collectUserTraffic()
	if len(c.pendingTraffic) > 0 {
		batch := make([]panel.UserTraffic, 0, len(c.pendingTraffic))
		for _, traffic := range c.pendingTraffic {
			batch = append(batch, traffic)
		}
		if err := c.apiClient.ReportUserTraffic(batch); err != nil {
			log.WithError(err).WithField("tag", c.tag).Info("Report user traffic failed")
		} else {
			c.pendingTraffic = nil
			log.WithField("tag", c.tag).Infof("Report %d users traffic", len(batch))
		}
	}
	return nil
}

// Caller holds stateMu and, for shared readers, trafficReportMu. Capture before
// user removal or inbound replacement so counters are retained in the retry batch.
func (c *Controller) collectUserTraffic() {
	userTraffic, readErr := c.server.GetUserTrafficSlice(c.tag, true)
	if readErr != nil {
		log.WithError(readErr).WithField("tag", c.tag).Warn("Read user traffic failed")
	}
	c.limiter.QualifyOnlineDevices(userTraffic)
	for _, traffic := range userTraffic {
		if c.pendingTraffic == nil {
			c.pendingTraffic = make(map[int]panel.UserTraffic)
		}
		pending := c.pendingTraffic[traffic.UID]
		pending.UID = traffic.UID
		pending.Upload += traffic.Upload
		pending.Download += traffic.Download
		c.pendingTraffic[traffic.UID] = pending
	}
}

// Online heartbeats are independent of the accounting push interval.
func (c *Controller) reportOnlineUsersTask() error {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if c.closed {
		return nil
	}
	c.onlineReportMu.Lock()
	defer c.onlineReportMu.Unlock()
	userTraffic, err := c.server.GetUserTrafficSlice(c.tag, false)
	if err != nil {
		log.WithError(err).Warn("Read online traffic snapshot failed")
	}

	c.limiter.QualifyOnlineDevices(userTraffic)
	// Retain the legacy window only for the existing device-limit checks.
	// It no longer determines which live IPs are sent to Xboard.
	_, _ = c.limiter.GetOnlineDevice()
	data := c.limiter.ActiveDeviceSnapshot()

	// Include departed UIDs explicitly, retaining the successful baseline
	// across transient failures. HTTP alive replaces each included list.
	for uid := range c.reportedOnline {
		if _, ok := data[uid]; !ok {
			data[uid] = []string{}
		}
	}
	if len(data) == 0 {
		return nil
	}
	if err = c.apiClient.ReportNodeOnlineUsers(&data); err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Info("Report online users failed")
	} else {
		c.reportedOnline = make(map[int]struct{})
		for uid, ips := range data {
			if len(ips) > 0 {
				c.reportedOnline[uid] = struct{}{}
			}
		}
		log.WithField("tag", c.tag).Infof("Report %d active users", len(c.reportedOnline))
		log.WithField("tag", c.tag).Debugf("Online users: %+v", data)
	}
	return nil
}

func compareUserList(old, new []panel.UserInfo) (deleted, added []panel.UserInfo) {
	type userKey struct {
		id          int
		uuid        string
		speedLimit  int
		deviceLimit int
	}
	keyFor := func(user panel.UserInfo) userKey {
		return userKey{
			id:          user.Id,
			uuid:        user.Uuid,
			speedLimit:  user.SpeedLimit,
			deviceLimit: user.DeviceLimit,
		}
	}
	oldMap := make(map[userKey]int)
	for i, user := range old {
		oldMap[keyFor(user)] = i
	}

	for _, user := range new {
		key := keyFor(user)
		if _, exists := oldMap[key]; !exists {
			added = append(added, user)
		} else {
			delete(oldMap, key)
		}
	}

	for _, index := range oldMap {
		deleted = append(deleted, old[index])
	}

	return deleted, added
}
