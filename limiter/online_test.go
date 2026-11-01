package limiter

import (
	"sync"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
)

func onlineFixture(t *testing.T) *Limiter {
	t.Helper()
	Init()
	l := AddLimiter("online", &conf.LimitConfig{}, []panel.UserInfo{{Id: 7, Uuid: "u"}, {Id: 8, Uuid: "v"}}, nil)
	t.Cleanup(func() { DeleteLimiter("online") })
	return l
}

func TestActiveDevicesSurviveRepeatedReportWindows(t *testing.T) {
	l := onlineFixture(t)
	a := l.AcquireDevice("online|u", "198.51.100.1")
	b := l.AcquireDevice("online|u", "198.51.100.1")
	c := l.AcquireDevice("online|u", "2001:db8::1")
	for i := 0; i < 12; i++ {
		l.GetOnlineDevice()
		if len(l.ActiveDeviceSnapshot()[7]) != 2 {
			t.Fatal("live devices disappeared between reports")
		}
	}
	a.Close()
	a.Close()
	if len(l.ActiveDeviceSnapshot()[7]) != 2 {
		t.Fatal("one of two same-IP connections removed the IP")
	}
	b.Close()
	if len(l.ActiveDeviceSnapshot()[7]) != 1 {
		t.Fatal("last reference not released")
	}
	c.Close()
	if len(l.ActiveDeviceSnapshot()) != 0 || len(l.devices) != 0 {
		t.Fatal("closed entries retained")
	}
}

func TestOnlineThresholdLatchesUntilDisconnect(t *testing.T) {
	l := onlineFixture(t)
	l.OnlineMinTraffic = 1000
	d := l.AcquireDevice("online|u", "198.51.100.1")
	l.QualifyOnlineDevices([]panel.UserTraffic{{UID: 7, Upload: 50}})
	if len(l.ActiveDeviceSnapshot()) != 0 {
		t.Fatal("probe qualified")
	}
	l.QualifyOnlineDevices([]panel.UserTraffic{{UID: 7, Download: 2000}})
	l.QualifyOnlineDevices(nil)
	if len(l.ActiveDeviceSnapshot()[7]) != 1 {
		t.Fatal("quiet cycle removed qualified device")
	}
	d.Close()
	e := l.AcquireDevice("online|u", "198.51.100.1")
	defer e.Close()
	if len(l.ActiveDeviceSnapshot()) != 0 {
		t.Fatal("new connection inherited qualification")
	}
}

func TestKnownLiveIPCanOpenConnectionAfterReportWindow(t *testing.T) {
	l := onlineFixture(t)
	v, _ := l.UserLimitInfo.Load("online|u")
	v.(*UserLimitInfo).DeviceLimit = 1
	d := l.AcquireDevice("online|u", "198.51.100.1")
	defer d.Close()
	for i := 0; i < 3; i++ {
		l.GetOnlineDevice()
		if _, rejected := l.CheckLimit("online|u", "198.51.100.1", true, true); rejected {
			t.Fatal("existing live IP rejected after report window")
		}
	}
}

func TestDeletedUserAndStaleCloseCannotCorruptReplacement(t *testing.T) {
	l := onlineFixture(t)
	old := l.AcquireDevice("online|u", "198.51.100.1")
	l.UpdateUser("online", []panel.UserInfo{{Id: 7, Uuid: "u"}}, []panel.UserInfo{{Id: 7, Uuid: "u"}})
	current := l.AcquireDevice("online|u", "198.51.100.1")
	old.Close()
	if len(l.ActiveDeviceSnapshot()[7]) != 1 {
		t.Fatal("stale close removed replacement")
	}
	current.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				d := l.AcquireDevice("online|u", "198.51.100.1")
				l.ActiveDeviceSnapshot()
				d.Close()
			}
		}()
	}
	wg.Wait()
	if len(l.devices) != 0 {
		t.Fatal("concurrent close retained state")
	}
}
