package node

import (
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
)

func TestCompareUserListDetectsEveryEnforcementField(t *testing.T) {
	base := panel.UserInfo{Id: 1, Uuid: "user-a", SpeedLimit: 10, DeviceLimit: 2}
	tests := []struct {
		name string
		user panel.UserInfo
	}{
		{name: "id", user: panel.UserInfo{Id: 2, Uuid: "user-a", SpeedLimit: 10, DeviceLimit: 2}},
		{name: "uuid", user: panel.UserInfo{Id: 1, Uuid: "user-b", SpeedLimit: 10, DeviceLimit: 2}},
		{name: "speed", user: panel.UserInfo{Id: 1, Uuid: "user-a", SpeedLimit: 20, DeviceLimit: 2}},
		{name: "device", user: panel.UserInfo{Id: 1, Uuid: "user-a", SpeedLimit: 10, DeviceLimit: 3}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deleted, added := compareUserList([]panel.UserInfo{base}, []panel.UserInfo{test.user})
			if len(deleted) != 1 || deleted[0] != base || len(added) != 1 || added[0] != test.user {
				t.Fatalf("unexpected diff: deleted=%#v added=%#v", deleted, added)
			}
		})
	}
}

func TestCompareUserListDoesNotBuildAmbiguousStringKeys(t *testing.T) {
	old := []panel.UserInfo{{Id: 1, Uuid: "user1", SpeedLimit: 23}}
	newUsers := []panel.UserInfo{{Id: 1, Uuid: "user12", SpeedLimit: 3}}
	deleted, added := compareUserList(old, newUsers)
	if len(deleted) != 1 || len(added) != 1 {
		t.Fatalf("ambiguous users were treated as equal: deleted=%#v added=%#v", deleted, added)
	}
}
