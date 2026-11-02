package sing

import (
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/format"
	"testing"
)

func TestSameUUIDHasIndependentNodeAccounting(t *testing.T) {
	b := &Sing{hookServer: &HookServer{}, users: &UserMap{uidMap: map[string]int{}}, nodeReportMinTrafficBytes: map[string]int64{}}
	for i, tag := range []string{"first", "second"} {
		b.users.uidMap[format.UserTag(tag, "same-uuid")] = i + 1
		counter.LoadOrCreate(&b.hookServer.counter, tag).Tx("same-uuid", 100)
	}
	delete(b.users.uidMap, format.UserTag("first", "same-uuid"))
	rows, err := b.GetUserTrafficSlice("second", true)
	if err != nil || len(rows) != 1 || rows[0].UID != 2 || rows[0].Upload != 100 {
		t.Fatalf("another node lost accounting: %v, %v", rows, err)
	}
}
