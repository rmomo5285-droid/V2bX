package limiter

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
)

func TestLimiterUserStateUsesSnapshotsAndSynchronizedAccess(t *testing.T) {
	const tag = "concurrent-user-state"
	Init()
	initialAlive := map[int]int{1: 2}
	l := AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "stable"}}, initialAlive)
	t.Cleanup(func() { DeleteLimiter(tag) })

	initialAlive[1] = 99
	if got := l.AliveCount(1); got != 2 {
		t.Fatalf("AddLimiter retained caller's alive map: got %d, want 2", got)
	}

	replacementAlive := map[int]int{1: 3}
	l.SetAliveList(replacementAlive)
	replacementAlive[1] = 100
	if got := l.AliveCount(1); got != 3 {
		t.Fatalf("SetAliveList retained caller's alive map: got %d, want 3", got)
	}

	const iterations = 1000
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			l.SetAliveList(map[int]int{1: i})
		}
	}()
	go func() {
		defer wg.Done()
		temporary := panel.UserInfo{Id: 2, Uuid: "temporary"}
		for i := 0; i < iterations; i++ {
			l.UpdateUser(tag, []panel.UserInfo{temporary}, nil)
			l.UpdateUser(tag, nil, []panel.UserInfo{temporary})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = l.AliveCount(1)
			_, _ = l.UIDForUUID("temporary")
		}
	}()
	wg.Wait()

	if uid, ok := l.UIDForUUID("stable"); !ok || uid != 1 {
		t.Fatalf("stable UUID mapping changed: uid=%d ok=%v", uid, ok)
	}
	if _, ok := l.UIDForUUID("temporary"); ok {
		t.Fatal("temporary UUID mapping remained after deletion")
	}
}

func TestDynamicSpeedLimitUpdatesAreAtomicAndReplaceBuckets(t *testing.T) {
	const tag = "dynamic-speed"
	Init()
	l := AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{
		Id:         1,
		Uuid:       "user",
		SpeedLimit: 10,
	}}, nil)
	t.Cleanup(func() { DeleteLimiter(tag) })

	assertRate := func(want float64) {
		t.Helper()
		bucket, reject := l.CheckLimit("dynamic-speed|user", "203.0.113.1", true, false)
		if reject || bucket == nil {
			t.Fatalf("CheckLimit returned bucket=%v reject=%v", bucket, reject)
		}
		if got := bucket.Rate(); math.Abs(got-want) > 0.5 {
			t.Fatalf("bucket rate = %f, want %f", got, want)
		}
	}

	if err := l.UpdateDynamicSpeedLimit(tag, "user", 4, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("set first dynamic limit: %v", err)
	}
	assertRate(500000)
	if err := l.UpdateDynamicSpeedLimit(tag, "user", 2, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("replace dynamic limit: %v", err)
	}
	assertRate(250000)
	if err := l.UpdateDynamicSpeedLimit(tag, "user", 2, time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("expire dynamic limit: %v", err)
	}
	assertRate(1250000)
}

func TestExpiredDynamicOnlyLimitDoesNotDeleteAuthorizedUser(t *testing.T) {
	const tag = "dynamic-only"
	Init()
	l := AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 7, Uuid: "user"}}, nil)
	t.Cleanup(func() { DeleteLimiter(tag) })
	if err := l.UpdateDynamicSpeedLimit(tag, "user", 2, time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("set expired dynamic limit: %v", err)
	}
	if bucket, reject := l.CheckLimit("dynamic-only|user", "203.0.113.2", true, false); reject || bucket != nil {
		t.Fatalf("authorized user after expiry: bucket=%v reject=%v", bucket, reject)
	}
	if uid, ok := l.UIDForUUID("user"); !ok || uid != 7 {
		t.Fatalf("authorized user was deleted after dynamic expiry: uid=%d ok=%v", uid, ok)
	}
}

func TestStageNewUsersRollsBackOnlyNewMappings(t *testing.T) {
	const tag = "stage-users"
	Init()
	l := AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "existing"}}, nil)
	t.Cleanup(func() { DeleteLimiter(tag) })

	staged := l.StageNewUsers(tag, []panel.UserInfo{
		{Id: 1, Uuid: "existing", SpeedLimit: 99},
		{Id: 2, Uuid: "new"},
	})
	if len(staged) != 1 || staged[0].Uuid != "new" {
		t.Fatalf("staged users = %#v, want only new user", staged)
	}
	if uid, ok := l.UIDForUUID("new"); !ok || uid != 2 {
		t.Fatalf("new user was not pre-authorized: uid=%d ok=%v", uid, ok)
	}
	l.RollbackStagedUsers(tag, staged)
	if _, ok := l.UIDForUUID("new"); ok {
		t.Fatal("staged user remained after rollback")
	}
	if uid, ok := l.UIDForUUID("existing"); !ok || uid != 1 {
		t.Fatalf("existing user changed during rollback: uid=%d ok=%v", uid, ok)
	}
}

func TestGetOnlineDeviceKeepsOldUserMapStable(t *testing.T) {
	const tag = "online-map"
	Init()
	l := AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "user"}}, nil)
	t.Cleanup(func() { DeleteLimiter(tag) })
	oldMap := l.OldUserOnline
	if _, reject := l.CheckLimit("online-map|user", "203.0.113.3", true, true); reject {
		t.Fatal("known user was rejected")
	}
	if _, err := l.GetOnlineDevice(); err != nil {
		t.Fatalf("GetOnlineDevice: %v", err)
	}
	if l.OldUserOnline != oldMap {
		t.Fatal("GetOnlineDevice replaced OldUserOnline and reintroduced a pointer race")
	}
}
