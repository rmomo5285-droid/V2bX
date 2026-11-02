package node

import (
	"fmt"
	"sync"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/task"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	log "github.com/sirupsen/logrus"
)

type Controller struct {
	monitorMu                 sync.Mutex
	stateMu                   sync.RWMutex
	closed                    bool
	nodeAdded                 bool
	onlineReportMu            sync.Mutex
	trafficReportMu           sync.Mutex
	pendingTraffic            map[int]panel.UserTraffic
	server                    vCore.Core
	apiClient                 *panel.Client
	tag                       string
	limiter                   *limiter.Limiter
	traffic                   map[string]int64
	userList                  []panel.UserInfo
	aliveMap                  map[int]int
	reportedOnline            map[int]struct{}
	info                      *panel.NodeInfo
	nodeInfoMonitorPeriodic   *task.Task
	userReportPeriodic        *task.Task
	renewCertPeriodic         *task.Task
	dynamicSpeedLimitPeriodic *task.Task
	onlineIpReportPeriodic    *task.Task
	*conf.Options
}

// NewController return a Node controller with default parameters.
func NewController(server vCore.Core, api *panel.Client, config *conf.Options) *Controller {
	controller := &Controller{
		server:    server,
		Options:   config,
		apiClient: api,
	}
	return controller
}

// Start implement the Start() function of the service interface
func (c *Controller) Start() (startErr error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.closed {
		return fmt.Errorf("controller is closed")
	}
	defer func() {
		if startErr != nil {
			if c.nodeAdded {
				_ = c.server.DelNode(c.tag)
				c.nodeAdded = false
			}
			if c.limiter != nil {
				limiter.DeleteLimiter(c.tag)
				c.limiter = nil
			}
			c.apiClient.CloseRequests()
			c.closed = true
		}
	}()
	// First fetch Node Info
	var err error
	node, err := c.apiClient.GetNodeInfo()
	if err != nil {
		return fmt.Errorf("get node info error: %s", err)
	}
	// Update user
	c.userList, err = c.apiClient.GetUserList()
	if err != nil {
		return fmt.Errorf("get user list error: %s", err)
	}
	c.aliveMap, err = c.apiClient.GetUserAlive()
	if err != nil {
		return fmt.Errorf("failed to get user alive list: %s", err)
	}
	if len(c.Options.Name) == 0 {
		c.tag = c.buildNodeTag(node)
	} else {
		c.tag = c.Options.Name
	}

	// add limiter
	if _, err := limiter.GetLimiter(c.tag); err == nil {
		return fmt.Errorf("duplicate node tag %q", c.tag)
	}
	l := limiter.AddLimiter(c.tag, &c.LimitConfig, c.userList, c.aliveMap)
	c.limiter = l
	l.NodeID = node.Id
	l.OnlineMinTraffic = c.Options.DeviceOnlineMinTraffic * 1000
	// add rule limiter
	if err = l.UpdateRule(&node.Rules); err != nil {
		return fmt.Errorf("update rule error: %s", err)
	}
	c.limiter = l
	if node.Security == panel.Tls {
		err = c.requestCert()
		if err != nil {
			return fmt.Errorf("request cert error: %s", err)
		}
	}
	// Add new tag
	c.nodeAdded = true // Also clean up a partially constructed inbound on failure.
	err = safeCoreCall("add initial node", func() error { return c.server.AddNode(c.tag, node, c.Options) })
	if err != nil {
		return fmt.Errorf("add new node error: %s", err)
	}
	added := 0
	if len(c.userList) > 0 {
		err = safeCoreCall("add initial users", func() error {
			added, err = c.server.AddUsers(&vCore.AddUsersParams{Tag: c.tag, Users: c.userList, NodeInfo: node})
			return err
		})
		if err != nil {
			return fmt.Errorf("add users error: %s", err)
		}
	}
	log.WithField("tag", c.tag).Infof("Added %d new users", added)
	c.info = node
	c.startTasks(node)
	return nil
}

// Close implement the Close() function of the service interface
func (c *Controller) Close() error {
	c.apiClient.CloseRequests()
	c.monitorMu.Lock()
	defer c.monitorMu.Unlock()
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.nodeInfoMonitorPeriodic != nil {
		c.nodeInfoMonitorPeriodic.Close()
	}
	if c.userReportPeriodic != nil {
		c.userReportPeriodic.Close()
	}
	if c.renewCertPeriodic != nil {
		c.renewCertPeriodic.Close()
	}
	if c.dynamicSpeedLimitPeriodic != nil {
		c.dynamicSpeedLimitPeriodic.Close()
	}
	if c.onlineIpReportPeriodic != nil {
		c.onlineIpReportPeriodic.Close()
	}
	var err error
	if c.nodeAdded || c.info != nil {
		err = safeCoreCall("delete closing node", func() error { return c.server.DelNode(c.tag) })
	}
	limiter.DeleteLimiter(c.tag)
	c.nodeAdded = false
	if err != nil {
		return fmt.Errorf("del node error: %s", err)
	}
	return nil
}

func (c *Controller) buildNodeTag(node *panel.NodeInfo) string {
	return fmt.Sprintf("[%s]-%s:%d", c.apiClient.APIHost, node.Type, node.Id)
}
