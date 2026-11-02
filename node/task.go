package node

import (
	"errors"
	"fmt"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/task"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	log "github.com/sirupsen/logrus"
)

func (c *Controller) startTasks(node *panel.NodeInfo) {
	// fetch node info task
	c.nodeInfoMonitorPeriodic = &task.Task{
		Interval: node.PullInterval,
		Execute:  c.nodeInfoMonitor,
	}
	// fetch user list task
	c.userReportPeriodic = &task.Task{
		Interval: node.PushInterval,
		Execute:  c.reportUserTrafficTask,
	}
	log.WithField("tag", c.tag).Info("Start monitor node status")
	// delay to start nodeInfoMonitor
	_ = c.nodeInfoMonitorPeriodic.Start(false)
	log.WithField("tag", c.tag).Info("Start report node status")
	_ = c.userReportPeriodic.Start(false)
	c.onlineIpReportPeriodic = &task.Task{Interval: 60 * time.Second, Execute: c.reportOnlineUsersTask}
	_ = c.onlineIpReportPeriodic.Start(false)
	c.syncRenewCertTask(node)
	if c.renewCertPeriodic != nil {
		log.WithField("tag", c.tag).Info("Start renew cert")
	}
	if c.LimitConfig.EnableDynamicSpeedLimit {
		c.traffic = make(map[string]int64)
		c.dynamicSpeedLimitPeriodic = &task.Task{
			Interval: time.Duration(c.LimitConfig.DynamicSpeedLimitConfig.Periodic) * time.Second,
			Execute:  c.SpeedChecker,
		}
		log.Printf("[%s: %d] Start dynamic speed limit", c.apiClient.NodeType, c.apiClient.NodeId)
	}
}

func (c *Controller) nodeInfoMonitor() (err error) {
	// Replacing a periodic Task must not overlap two polling generations.
	c.monitorMu.Lock()
	defer c.monitorMu.Unlock()
	c.stateMu.RLock()
	closed := c.closed
	c.stateMu.RUnlock()
	if closed {
		return nil
	}
	// get node info
	newN, err := c.apiClient.GetNodeInfo()
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get node info failed")
		return nil
	}
	// get user info
	newU, err := c.apiClient.GetUserList()
	if err != nil {
		if newN != nil {
			c.apiClient.InvalidateNodeCache()
		}
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get user list failed")
		return nil
	}
	// get user alive
	newA, err := c.apiClient.GetUserAlive()
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get alive list failed")
		// Preserve the last alive snapshot, but still apply user revocations.
		newA = nil
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.closed {
		return nil
	}
	if newN != nil {
		if err = c.reloadNode(newN, newU, newA); err != nil {
			c.apiClient.InvalidateNodeCache()
			c.apiClient.InvalidateUserCache()
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Error("Node reload failed")
		}
		return nil
	}
	// node no changed, check users
	if newU == nil {
		if newA != nil {
			c.limiter.SetAliveList(newA)
			c.aliveMap = cloneAliveMap(newA)
		}
		return nil
	}
	deleted, added, err := c.applyUserUpdate(newU)
	if err != nil {
		c.apiClient.InvalidateUserCache()
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Update users failed")
		return nil
	}
	if len(added)+len(deleted) != 0 {
		log.WithField("tag", c.tag).
			Infof("%d user deleted, %d user added", len(deleted), len(added))
	}
	if newA != nil {
		c.limiter.SetAliveList(newA)
		c.aliveMap = cloneAliveMap(newA)
	}
	return nil
}

func (c *Controller) reloadNode(newN *panel.NodeInfo, newU []panel.UserInfo, newA map[int]int) error {
	if c.info == nil {
		return errors.New("reload node: current node info is unavailable")
	}

	oldInfo := c.info
	oldUsers := append([]panel.UserInfo(nil), c.userList...)
	if newU == nil {
		newU = append([]panel.UserInfo(nil), oldUsers...)
	}

	// Compile without publishing so the active node keeps its current rules.
	oldRules := c.limiter.SnapshotRules()
	newRules, err := limiter.CompileRuleSnapshot(&newN.Rules)
	if err != nil {
		return fmt.Errorf("validate new rules: %w", err)
	}

	log.WithField("tag", c.tag).Info("Node changed, reload")
	c.collectUserTraffic()
	if err := safeCoreCall("delete current node", func() error {
		return c.server.DelNode(c.tag)
	}); err != nil {
		return c.rollbackNodeReload(
			oldInfo,
			oldUsers,
			oldRules,
			true,
			fmt.Errorf("delete current node: %w", err),
		)
	}

	if newN.Security == panel.Tls {
		if err := c.requestCert(); err != nil {
			return c.rollbackNodeReload(
				oldInfo,
				oldUsers,
				oldRules,
				false,
				fmt.Errorf("request certificate: %w", err),
			)
		}
	}

	c.limiter.RestoreRules(newRules)
	if err := safeCoreCall("add replacement node", func() error {
		return c.server.AddNode(c.tag, newN, c.Options)
	}); err != nil {
		return c.rollbackNodeReload(
			oldInfo,
			oldUsers,
			oldRules,
			true,
			fmt.Errorf("add replacement node: %w", err),
		)
	}
	deleted, added := compareUserList(oldUsers, newU)
	staged := c.limiter.StageNewUsers(c.tag, added)
	if err := safeCoreCall("add replacement users", func() error {
		_, err := c.server.AddUsers(&vCore.AddUsersParams{
			Tag:      c.tag,
			Users:    newU,
			NodeInfo: newN,
		})
		return err
	}); err != nil {
		c.limiter.RollbackStagedUsers(c.tag, staged)
		return c.rollbackNodeReload(
			oldInfo,
			oldUsers,
			oldRules,
			true,
			fmt.Errorf("add replacement users: %w", err),
		)
	}

	if len(deleted) > 0 || len(added) > 0 {
		c.limiter.UpdateUser(c.tag, added, deleted)
	}
	if newA != nil {
		c.limiter.SetAliveList(newA)
		c.aliveMap = cloneAliveMap(newA)
	}
	c.info = newN
	c.userList = newU
	c.traffic = make(map[string]int64)

	if err := c.syncReloadTasks(newN); err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Update periodic tasks failed")
	}
	log.WithField("tag", c.tag).Infof("Added %d new users", len(c.userList))
	return nil
}

func (c *Controller) applyUserUpdate(newUsers []panel.UserInfo) (deleted, added []panel.UserInfo, err error) {
	deleted, added = compareUserList(c.userList, newUsers)
	if len(deleted) == 0 && len(added) == 0 {
		c.userList = append([]panel.UserInfo(nil), newUsers...)
		return deleted, added, nil
	}

	staged := c.limiter.StageNewUsers(c.tag, added)
	rollbackStaged := func() {
		c.limiter.RollbackStagedUsers(c.tag, staged)
	}

	if len(deleted) > 0 {
		c.collectUserTraffic()
		if err := safeCoreCall("delete users", func() error {
			return c.server.DelUsers(deleted, c.tag, c.info)
		}); err != nil {
			restoreErr := restoreCoreUsers(c.server, c.tag, c.info, deleted)
			rollbackStaged()
			return deleted, added, errors.Join(
				fmt.Errorf("delete users: %w", err),
				restoreErr,
			)
		}
	}

	if len(added) > 0 {
		if err := safeCoreCall("add users", func() error {
			_, err := c.server.AddUsers(&vCore.AddUsersParams{
				Tag:      c.tag,
				NodeInfo: c.info,
				Users:    added,
			})
			return err
		}); err != nil {
			cleanupErr := safeCoreCall("remove partially added users", func() error {
				return c.server.DelUsers(added, c.tag, c.info)
			})
			restoreErr := restoreCoreUsers(c.server, c.tag, c.info, deleted)
			rollbackStaged()
			return deleted, added, errors.Join(
				fmt.Errorf("add users: %w", err),
				wrapOptionalError("remove partially added users", cleanupErr),
				restoreErr,
			)
		}
	}

	c.limiter.UpdateUser(c.tag, added, deleted)
	if c.LimitConfig.EnableDynamicSpeedLimit {
		for i := range deleted {
			delete(c.traffic, deleted[i].Uuid)
		}
	}
	c.userList = append([]panel.UserInfo(nil), newUsers...)
	return deleted, added, nil
}

func restoreCoreUsers(server vCore.Core, tag string, info *panel.NodeInfo, users []panel.UserInfo) error {
	if len(users) == 0 {
		return nil
	}
	if err := safeCoreCall("restore users", func() error {
		_, err := server.AddUsers(&vCore.AddUsersParams{
			Tag:      tag,
			NodeInfo: info,
			Users:    users,
		})
		return err
	}); err != nil {
		return fmt.Errorf("restore users: %w", err)
	}
	return nil
}

func wrapOptionalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func (c *Controller) syncReloadTasks(nodeInfo *panel.NodeInfo) error {
	var errs []error
	if nodeInfo.PullInterval != 0 {
		if err := replacePeriodicTask(
			&c.nodeInfoMonitorPeriodic,
			nodeInfo.PullInterval,
			c.nodeInfoMonitor,
		); err != nil {
			errs = append(errs, fmt.Errorf("replace node monitor task: %w", err))
		}
	}
	if nodeInfo.PushInterval != 0 {
		if err := replacePeriodicTask(
			&c.userReportPeriodic,
			nodeInfo.PushInterval,
			c.reportUserTrafficTask,
		); err != nil {
			errs = append(errs, fmt.Errorf("replace user report task: %w", err))
		}
	}
	c.syncRenewCertTask(nodeInfo)
	return errors.Join(errs...)
}

func replacePeriodicTask(current **task.Task, interval time.Duration, execute func() error) error {
	if *current != nil && (*current).Interval == interval {
		return nil
	}
	old := *current
	if old != nil {
		old.Close()
	}
	next := &task.Task{Interval: interval, Execute: execute}
	if err := next.Start(false); err != nil {
		return err
	}
	*current = next
	return nil
}

func (c *Controller) syncRenewCertTask(nodeInfo *panel.NodeInfo) {
	shouldRun := needsPeriodicCertRenewal(nodeInfo, c.CertConfig)
	if !shouldRun {
		if c.renewCertPeriodic != nil {
			c.renewCertPeriodic.Close()
			c.renewCertPeriodic = nil
		}
		return
	}
	if c.renewCertPeriodic != nil {
		return
	}
	c.renewCertPeriodic = &task.Task{
		Interval: 24 * time.Hour,
		Execute:  c.renewCertTask,
	}
	_ = c.renewCertPeriodic.Start(true)
}

func needsPeriodicCertRenewal(nodeInfo *panel.NodeInfo, certConfig *conf.CertConfig) bool {
	if nodeInfo == nil || nodeInfo.Security != panel.Tls || certConfig == nil {
		return false
	}
	switch certConfig.CertMode {
	case "none", "", "file", "self":
		return false
	default:
		return true
	}
}

func (c *Controller) rollbackNodeReload(
	oldInfo *panel.NodeInfo,
	oldUsers []panel.UserInfo,
	oldRules limiter.RuleSnapshot,
	removeReplacement bool,
	cause error,
) error {
	errs := []error{cause}
	if removeReplacement {
		if err := safeCoreCall("remove failed replacement", func() error {
			return c.server.DelNode(c.tag)
		}); err != nil {
			errs = append(errs, fmt.Errorf("remove failed replacement: %w", err))
		}
	}

	c.limiter.RestoreRules(oldRules)
	if err := safeCoreCall("restore previous node", func() error {
		return c.server.AddNode(c.tag, oldInfo, c.Options)
	}); err != nil {
		errs = append(errs, fmt.Errorf("restore previous node: %w", err))
		return errors.Join(errs...)
	}
	if err := safeCoreCall("restore previous users", func() error {
		_, err := c.server.AddUsers(&vCore.AddUsersParams{
			Tag:      c.tag,
			Users:    oldUsers,
			NodeInfo: oldInfo,
		})
		return err
	}); err != nil {
		errs = append(errs, fmt.Errorf("restore previous users: %w", err))
	}
	return errors.Join(errs...)
}

func safeCoreCall(operation string, call func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%s panic: %v", operation, recovered)
		}
	}()
	return call()
}

func cloneAliveMap(source map[int]int) map[int]int {
	clone := make(map[int]int, len(source))
	for uid, alive := range source {
		clone[uid] = alive
	}
	return clone
}

func (c *Controller) SpeedChecker() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.closed {
		return nil
	}
	for u, t := range c.traffic {
		if t >= c.LimitConfig.DynamicSpeedLimitConfig.Traffic {
			err := c.limiter.UpdateDynamicSpeedLimit(c.tag, u,
				c.LimitConfig.DynamicSpeedLimitConfig.SpeedLimit,
				time.Now().Add(time.Duration(c.LimitConfig.DynamicSpeedLimitConfig.ExpireTime)*time.Minute))
			log.WithField("err", err).Error("Update dynamic speed limit failed")
			delete(c.traffic, u)
		}
	}
	return nil
}
