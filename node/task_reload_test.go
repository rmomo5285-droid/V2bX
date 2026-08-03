package node

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/task"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
)

type reloadTestCore struct {
	node               *panel.NodeInfo
	users              []panel.UserInfo
	failDeleteOnce     bool
	failAddNodeID      int
	failAddUsersNodeID int
	panicAddNodeID     int
	panicAddUsersID    int
	deleteCalls        int
	beforeFirstDelete  func()
	beforeAddUsers     func(*vCore.AddUsersParams)
}

func (c *reloadTestCore) Start() error { return nil }
func (c *reloadTestCore) Close() error { return nil }

func (c *reloadTestCore) AddNode(_ string, info *panel.NodeInfo, _ *conf.Options) error {
	c.node = info
	c.users = nil
	if info.Id == c.panicAddNodeID {
		panic("injected AddNode panic")
	}
	if info.Id == c.failAddNodeID {
		return errors.New("injected AddNode failure")
	}
	return nil
}

func (c *reloadTestCore) DelNode(_ string) error {
	c.deleteCalls++
	if c.deleteCalls == 1 && c.beforeFirstDelete != nil {
		c.beforeFirstDelete()
	}
	if c.failDeleteOnce && c.deleteCalls == 1 {
		return errors.New("injected DelNode failure")
	}
	c.node = nil
	c.users = nil
	return nil
}

func (c *reloadTestCore) AddUsers(params *vCore.AddUsersParams) (int, error) {
	if c.beforeAddUsers != nil {
		c.beforeAddUsers(params)
	}
	c.users = append([]panel.UserInfo(nil), params.Users...)
	if params.NodeInfo.Id == c.panicAddUsersID {
		panic("injected AddUsers panic")
	}
	if params.NodeInfo.Id == c.failAddUsersNodeID {
		if len(c.users) > 1 {
			c.users = c.users[:1]
		}
		return 0, errors.New("injected AddUsers failure")
	}
	return len(params.Users), nil
}

func (c *reloadTestCore) GetUserTrafficSlice(string, bool) ([]panel.UserTraffic, error) {
	return nil, nil
}

func (c *reloadTestCore) DelUsers([]panel.UserInfo, string, *panel.NodeInfo) error {
	return nil
}

func (c *reloadTestCore) Protocols() []string { return []string{"hysteria2"} }
func (c *reloadTestCore) Type() string        { return "test" }

func TestReloadNodeRestoresPreviousStateAtEveryFailureBoundary(t *testing.T) {
	tests := []struct {
		name              string
		configure         func(*reloadTestCore, *conf.Options, *panel.NodeInfo)
		wantErrorFragment string
	}{
		{
			name: "delete node",
			configure: func(core *reloadTestCore, _ *conf.Options, _ *panel.NodeInfo) {
				core.failDeleteOnce = true
			},
			wantErrorFragment: "delete current node",
		},
		{
			name: "certificate",
			configure: func(_ *reloadTestCore, options *conf.Options, replacement *panel.NodeInfo) {
				replacement.Security = panel.Tls
				options.CertConfig.CertMode = "file"
			},
			wantErrorFragment: "request certificate",
		},
		{
			name: "add node",
			configure: func(core *reloadTestCore, _ *conf.Options, replacement *panel.NodeInfo) {
				core.failAddNodeID = replacement.Id
			},
			wantErrorFragment: "add replacement node",
		},
		{
			name: "add node panic",
			configure: func(core *reloadTestCore, _ *conf.Options, replacement *panel.NodeInfo) {
				core.panicAddNodeID = replacement.Id
			},
			wantErrorFragment: "add replacement node",
		},
		{
			name: "add users",
			configure: func(core *reloadTestCore, _ *conf.Options, replacement *panel.NodeInfo) {
				core.failAddUsersNodeID = replacement.Id
			},
			wantErrorFragment: "add replacement users",
		},
		{
			name: "add users panic",
			configure: func(core *reloadTestCore, _ *conf.Options, replacement *panel.NodeInfo) {
				core.panicAddUsersID = replacement.Id
			},
			wantErrorFragment: "add replacement users",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limiter.Init()
			tag := "reload-" + strings.ReplaceAll(test.name, " ", "-")
			oldUsers := []panel.UserInfo{{Id: 1, Uuid: "old-user", DeviceLimit: 2}}
			newUsers := []panel.UserInfo{{Id: 2, Uuid: "new-user", DeviceLimit: 3}}
			oldNode := &panel.NodeInfo{
				Id:   1,
				Type: "hysteria2",
				Rules: panel.Rules{
					Regexp:   []string{`^old\.example$`},
					Protocol: []string{"bittorrent"},
				},
			}
			replacement := &panel.NodeInfo{
				Id:   2,
				Type: "hysteria2",
				Rules: panel.Rules{
					Regexp:   []string{`^new\.example$`},
					Protocol: []string{"http"},
				},
			}
			options := &conf.Options{CertConfig: conf.NewCertConfig()}
			core := &reloadTestCore{
				node:  oldNode,
				users: append([]panel.UserInfo(nil), oldUsers...),
			}
			test.configure(core, options, replacement)

			l := limiter.AddLimiter(tag, &options.LimitConfig, oldUsers, map[int]int{1: 4})
			t.Cleanup(func() { limiter.DeleteLimiter(tag) })
			if err := l.UpdateRule(&oldNode.Rules); err != nil {
				t.Fatalf("initialize old rules: %v", err)
			}
			core.beforeFirstDelete = func() {
				if !l.CheckDomainRule("old.example") || l.CheckDomainRule("new.example") {
					t.Error("replacement rules were published before deleting the old node")
				}
			}
			controller := &Controller{
				server:   core,
				tag:      tag,
				limiter:  l,
				userList: append([]panel.UserInfo(nil), oldUsers...),
				aliveMap: map[int]int{1: 4},
				info:     oldNode,
				Options:  options,
			}

			err := controller.reloadNode(replacement, newUsers, map[int]int{2: 9})
			if err == nil || !strings.Contains(err.Error(), test.wantErrorFragment) {
				t.Fatalf("reload error = %v, want fragment %q", err, test.wantErrorFragment)
			}
			if core.node != oldNode {
				t.Fatalf("active node was not restored: got %#v, want old node", core.node)
			}
			if !reflect.DeepEqual(core.users, oldUsers) {
				t.Fatalf("active users were not restored: got %#v, want %#v", core.users, oldUsers)
			}
			if controller.info != oldNode || !reflect.DeepEqual(controller.userList, oldUsers) {
				t.Fatal("controller committed replacement state after failed reload")
			}
			if !l.CheckDomainRule("old.example") || l.CheckDomainRule("new.example") {
				t.Fatal("routing rules were not restored")
			}
			if !l.CheckProtocolRule("bittorrent") || l.CheckProtocolRule("http") {
				t.Fatal("protocol rules were not restored")
			}
			if uid, ok := l.UIDForUUID("old-user"); !ok || uid != 1 {
				t.Fatalf("old limiter user missing: uid=%d ok=%v", uid, ok)
			}
			if _, ok := l.UIDForUUID("new-user"); ok {
				t.Fatal("replacement limiter user leaked into rolled-back state")
			}
			if got := l.AliveCount(1); got != 4 {
				t.Fatalf("old alive state changed: got %d, want 4", got)
			}
			if got := l.AliveCount(2); got != 0 {
				t.Fatalf("replacement alive state leaked: got %d", got)
			}
		})
	}
}

func TestReloadNodeCommitsReplacementState(t *testing.T) {
	limiter.Init()
	const tag = "reload-success"
	oldUsers := []panel.UserInfo{{Id: 1, Uuid: "old-user"}}
	newUsers := []panel.UserInfo{{Id: 2, Uuid: "new-user"}}
	oldNode := &panel.NodeInfo{
		Id:    1,
		Type:  "hysteria2",
		Rules: panel.Rules{Regexp: []string{`^old\.example$`}},
	}
	replacement := &panel.NodeInfo{
		Id:    2,
		Type:  "hysteria2",
		Rules: panel.Rules{Regexp: []string{`^new\.example$`}, Protocol: []string{"http"}},
	}
	options := &conf.Options{CertConfig: conf.NewCertConfig()}
	core := &reloadTestCore{node: oldNode, users: append([]panel.UserInfo(nil), oldUsers...)}
	l := limiter.AddLimiter(tag, &options.LimitConfig, oldUsers, map[int]int{1: 4})
	t.Cleanup(func() { limiter.DeleteLimiter(tag) })
	if err := l.UpdateRule(&oldNode.Rules); err != nil {
		t.Fatalf("initialize old rules: %v", err)
	}
	controller := &Controller{
		server:   core,
		tag:      tag,
		limiter:  l,
		userList: append([]panel.UserInfo(nil), oldUsers...),
		aliveMap: map[int]int{1: 4},
		info:     oldNode,
		Options:  options,
	}

	if err := controller.reloadNode(replacement, newUsers, map[int]int{2: 9}); err != nil {
		t.Fatalf("reload replacement: %v", err)
	}
	if core.node != replacement || !reflect.DeepEqual(core.users, newUsers) {
		t.Fatalf("replacement was not installed: node=%#v users=%#v", core.node, core.users)
	}
	if controller.info != replacement || !reflect.DeepEqual(controller.userList, newUsers) {
		t.Fatal("controller did not commit replacement state")
	}
	if !l.CheckDomainRule("new.example") || l.CheckDomainRule("old.example") {
		t.Fatal("replacement routing rules were not committed")
	}
	if !l.CheckProtocolRule("http") {
		t.Fatal("replacement protocol rules were not committed")
	}
	if uid, ok := l.UIDForUUID("new-user"); !ok || uid != 2 {
		t.Fatalf("replacement limiter user missing: uid=%d ok=%v", uid, ok)
	}
	if _, ok := l.UIDForUUID("old-user"); ok {
		t.Fatal("old limiter user remained after successful reload")
	}
	if got := l.AliveCount(2); got != 9 {
		t.Fatalf("replacement alive state = %d, want 9", got)
	}
}

func TestReloadNodeRejectsInvalidRulesBeforeDeletingCurrentNode(t *testing.T) {
	limiter.Init()
	const tag = "reload-invalid-rules"
	oldUsers := []panel.UserInfo{{Id: 1, Uuid: "old-user"}}
	oldNode := &panel.NodeInfo{
		Id:    1,
		Type:  "hysteria2",
		Rules: panel.Rules{Regexp: []string{`^old\.example$`}},
	}
	replacement := &panel.NodeInfo{
		Id:    2,
		Type:  "hysteria2",
		Rules: panel.Rules{Regexp: []string{"["}},
	}
	options := &conf.Options{CertConfig: conf.NewCertConfig()}
	core := &reloadTestCore{node: oldNode, users: append([]panel.UserInfo(nil), oldUsers...)}
	l := limiter.AddLimiter(tag, &options.LimitConfig, oldUsers, map[int]int{1: 4})
	t.Cleanup(func() { limiter.DeleteLimiter(tag) })
	if err := l.UpdateRule(&oldNode.Rules); err != nil {
		t.Fatalf("initialize old rules: %v", err)
	}
	controller := &Controller{
		server:   core,
		tag:      tag,
		limiter:  l,
		userList: append([]panel.UserInfo(nil), oldUsers...),
		aliveMap: map[int]int{1: 4},
		info:     oldNode,
		Options:  options,
	}

	err := controller.reloadNode(replacement, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "validate new rules") {
		t.Fatalf("reload error = %v, want invalid rule error", err)
	}
	if core.deleteCalls != 0 || core.node != oldNode || !reflect.DeepEqual(core.users, oldUsers) {
		t.Fatal("invalid rules changed the active core state")
	}
	if !l.CheckDomainRule("old.example") {
		t.Fatal("invalid rules changed the active limiter rules")
	}
}

func TestApplyUserUpdateStagesNewUserBeforeCorePublication(t *testing.T) {
	limiter.Init()
	const tag = "stage-before-core"
	oldUsers := []panel.UserInfo{{Id: 1, Uuid: "old-user"}}
	newUsers := []panel.UserInfo{{Id: 1, Uuid: "old-user"}, {Id: 2, Uuid: "new-user"}}
	nodeInfo := &panel.NodeInfo{Id: 1, Type: "hysteria2"}
	options := &conf.Options{CertConfig: conf.NewCertConfig()}
	l := limiter.AddLimiter(tag, &options.LimitConfig, oldUsers, nil)
	t.Cleanup(func() { limiter.DeleteLimiter(tag) })
	core := &reloadTestCore{node: nodeInfo, users: append([]panel.UserInfo(nil), oldUsers...)}
	core.beforeAddUsers = func(params *vCore.AddUsersParams) {
		if len(params.Users) == 1 && params.Users[0].Uuid == "new-user" {
			if uid, ok := l.UIDForUUID("new-user"); !ok || uid != 2 {
				t.Errorf("new user was not staged before core AddUsers: uid=%d ok=%v", uid, ok)
			}
		}
	}
	controller := &Controller{
		server:   core,
		tag:      tag,
		limiter:  l,
		userList: append([]panel.UserInfo(nil), oldUsers...),
		info:     nodeInfo,
		Options:  options,
	}

	deleted, added, err := controller.applyUserUpdate(newUsers)
	if err != nil {
		t.Fatalf("apply user update: %v", err)
	}
	if len(deleted) != 0 || len(added) != 1 || added[0].Uuid != "new-user" {
		t.Fatalf("unexpected diff: deleted=%#v added=%#v", deleted, added)
	}
	if uid, ok := l.UIDForUUID("new-user"); !ok || uid != 2 {
		t.Fatalf("new user missing after commit: uid=%d ok=%v", uid, ok)
	}
}

func TestApplyUserUpdateRollsBackStagedUserWhenCoreFails(t *testing.T) {
	limiter.Init()
	const tag = "stage-core-failure"
	oldUsers := []panel.UserInfo{{Id: 1, Uuid: "old-user"}}
	newUsers := []panel.UserInfo{{Id: 1, Uuid: "old-user"}, {Id: 2, Uuid: "new-user"}}
	nodeInfo := &panel.NodeInfo{Id: 1, Type: "hysteria2"}
	options := &conf.Options{CertConfig: conf.NewCertConfig()}
	l := limiter.AddLimiter(tag, &options.LimitConfig, oldUsers, nil)
	t.Cleanup(func() { limiter.DeleteLimiter(tag) })
	core := &reloadTestCore{
		node:               nodeInfo,
		users:              append([]panel.UserInfo(nil), oldUsers...),
		failAddUsersNodeID: nodeInfo.Id,
	}
	controller := &Controller{
		server:   core,
		tag:      tag,
		limiter:  l,
		userList: append([]panel.UserInfo(nil), oldUsers...),
		info:     nodeInfo,
		Options:  options,
	}

	_, _, err := controller.applyUserUpdate(newUsers)
	if err == nil || !strings.Contains(err.Error(), "add users") {
		t.Fatalf("apply error = %v, want AddUsers failure", err)
	}
	if _, ok := l.UIDForUUID("new-user"); ok {
		t.Fatal("staged user remained after core AddUsers failed")
	}
	if !reflect.DeepEqual(controller.userList, oldUsers) {
		t.Fatalf("controller committed failed user update: %#v", controller.userList)
	}
}

func TestSyncReloadTasksReplacesTaskObjectsAndUsesPushInterval(t *testing.T) {
	oldMonitor := &task.Task{Interval: time.Hour, Execute: func() error { return nil }}
	oldReport := &task.Task{Interval: time.Hour, Execute: func() error { return nil }}
	controller := &Controller{
		nodeInfoMonitorPeriodic: oldMonitor,
		userReportPeriodic:      oldReport,
		Options:                 &conf.Options{CertConfig: conf.NewCertConfig()},
	}
	if err := controller.syncReloadTasks(&panel.NodeInfo{
		PullInterval: 2 * time.Hour,
		PushInterval: 3 * time.Hour,
	}); err != nil {
		t.Fatalf("sync reload tasks: %v", err)
	}
	t.Cleanup(func() {
		controller.nodeInfoMonitorPeriodic.Close()
		controller.userReportPeriodic.Close()
	})
	if controller.nodeInfoMonitorPeriodic == oldMonitor {
		t.Fatal("node monitor reused a closed Task object")
	}
	if controller.userReportPeriodic == oldReport {
		t.Fatal("user reporter reused a closed Task object")
	}
	if got := controller.nodeInfoMonitorPeriodic.Interval; got != 2*time.Hour {
		t.Fatalf("pull interval = %s, want 2h", got)
	}
	if got := controller.userReportPeriodic.Interval; got != 3*time.Hour {
		t.Fatalf("push interval = %s, want 3h", got)
	}
}

func TestPeriodicCertificateRenewalTracksTLSMode(t *testing.T) {
	tests := []struct {
		name     string
		node     *panel.NodeInfo
		cert     *conf.CertConfig
		wantTask bool
	}{
		{name: "plain node", node: &panel.NodeInfo{}, cert: &conf.CertConfig{CertMode: "dns"}},
		{name: "external certificate", node: &panel.NodeInfo{Security: panel.Tls}, cert: &conf.CertConfig{CertMode: "file"}},
		{name: "self signed", node: &panel.NodeInfo{Security: panel.Tls}, cert: &conf.CertConfig{CertMode: "self"}},
		{name: "acme http", node: &panel.NodeInfo{Security: panel.Tls}, cert: &conf.CertConfig{CertMode: "http"}, wantTask: true},
		{name: "acme dns", node: &panel.NodeInfo{Security: panel.Tls}, cert: &conf.CertConfig{CertMode: "dns"}, wantTask: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := needsPeriodicCertRenewal(test.node, test.cert); got != test.wantTask {
				t.Fatalf("needsPeriodicCertRenewal = %v, want %v", got, test.wantTask)
			}
		})
	}

	existing := &task.Task{Interval: 24 * time.Hour, Execute: func() error { return nil }}
	controller := &Controller{
		renewCertPeriodic: existing,
		Options:           &conf.Options{CertConfig: &conf.CertConfig{CertMode: "dns"}},
	}
	controller.syncRenewCertTask(&panel.NodeInfo{Security: panel.None})
	if controller.renewCertPeriodic != nil {
		t.Fatal("renewal task remained active after TLS was disabled")
	}
}
