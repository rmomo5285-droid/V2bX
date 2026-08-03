package limiter

import (
	"testing"
	"time"

	"github.com/InazumaV/V2bX/conf"
)

func TestAbuseGuardBlocksDistinctIPScan(t *testing.T) {
	guard := newAbuseGuard(&conf.AbuseGuardConfig{
		Enable:                  true,
		WindowSeconds:           10,
		MaxClientConnections:    100,
		MaxOutboundConnections:  100,
		MaxDistinctDestinations: 100,
		MaxDistinctIPTargets:    2,
		MaxDistinctPorts:        100,
		BlockSeconds:            60,
		MaxTrackedUsers:         10,
	})
	now := time.Unix(1000, 0)
	user := "node|uuid"

	if decision := guard.checkOutbound(user, "192.0.2.1:22", now); decision.Reject {
		t.Fatalf("first target rejected: %#v", decision)
	}
	if decision := guard.checkOutbound(user, "192.0.2.2:22", now); decision.Reject {
		t.Fatalf("second target rejected: %#v", decision)
	}
	decision := guard.checkOutbound(user, "192.0.2.3:22", now)
	if !decision.Reject || decision.Reason != "distinct_ip_scan" {
		t.Fatalf("third IP decision = %#v", decision)
	}
	if decision := guard.checkOutbound(user, "example.com:443", now.Add(time.Second)); !decision.Reject {
		t.Fatal("blocked user was allowed before the block expired")
	}
}

func TestAbuseGuardResetsAfterBlock(t *testing.T) {
	guard := newAbuseGuard(&conf.AbuseGuardConfig{
		Enable:                  true,
		WindowSeconds:           1,
		MaxClientConnections:    1,
		MaxOutboundConnections:  100,
		MaxDistinctDestinations: 100,
		MaxDistinctIPTargets:    100,
		MaxDistinctPorts:        100,
		BlockSeconds:            2,
		MaxTrackedUsers:         10,
	})
	now := time.Unix(2000, 0)
	user := "node|uuid"
	if decision := guard.checkClientConnection(user, now); decision.Reject {
		t.Fatalf("first connection rejected: %#v", decision)
	}
	if decision := guard.checkClientConnection(user, now); !decision.Reject {
		t.Fatal("connection burst was not blocked")
	}
	if decision := guard.checkClientConnection(user, now.Add(3*time.Second)); decision.Reject {
		t.Fatalf("connection remained blocked after expiry: %#v", decision)
	}
}

func TestAbuseGuardRejectsNewStateAtCapacity(t *testing.T) {
	guard := newAbuseGuard(&conf.AbuseGuardConfig{
		Enable:          true,
		MaxTrackedUsers: 1,
	})
	now := time.Unix(3000, 0)
	if decision := guard.checkClientConnection("first", now); decision.Reject {
		t.Fatalf("first user rejected: %#v", decision)
	}
	decision := guard.checkClientConnection("second", now)
	if !decision.Reject || decision.Reason != "tracker_capacity" {
		t.Fatalf("capacity decision = %#v", decision)
	}
}

func TestUDPFlowGuardBlocksSameSessionTargetFanout(t *testing.T) {
	guard := newAbuseGuard(&conf.AbuseGuardConfig{
		Enable:                  true,
		MaxDistinctDestinations: 10,
		MaxDistinctIPTargets:    2,
		MaxDistinctPorts:        2,
	})
	flow := (&Limiter{abuse: guard}).NewUDPFlowGuard()
	if decision := flow.Check("192.0.2.1:53"); decision.Reject {
		t.Fatalf("first target rejected: %+v", decision)
	}
	if decision := flow.Check("192.0.2.2:53"); decision.Reject {
		t.Fatalf("second target rejected: %+v", decision)
	}
	decision := flow.Check("192.0.2.3:53")
	if !decision.Reject || decision.Reason != "udp_flow_ip_scan" {
		t.Fatalf("third IP decision = %+v, want udp_flow_ip_scan", decision)
	}
	if decision := flow.Check("192.0.2.1:54"); !decision.Reject {
		t.Fatal("blocked UDP flow became usable again")
	}
}

func TestSplitDestination(t *testing.T) {
	tests := []struct {
		input string
		host  string
		port  string
	}{
		{"Example.COM:443", "example.com", "443"},
		{"[2001:db8::1]:53", "2001:db8::1", "53"},
		{"192.0.2.1", "192.0.2.1", ""},
	}
	for _, test := range tests {
		host, port := splitDestination(test.input)
		if host != test.host || port != test.port {
			t.Fatalf("splitDestination(%q) = %q, %q; want %q, %q", test.input, host, port, test.host, test.port)
		}
	}
}
