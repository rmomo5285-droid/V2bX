package limiter

import (
	"testing"
	"time"
)

func TestRejectionAuditSuppressesAndSummarizesDuplicates(t *testing.T) {
	now := time.Unix(100, 0)
	audit := &rejectionAudit{
		states:      make(map[string]*rejectionAuditState),
		interval:    10 * time.Second,
		maxKeys:     10,
		nextCleanup: now.Add(10 * time.Second),
	}
	if allowed, suppressed := audit.allow("node|uuid", "panel_domain", now); !allowed || suppressed != 0 {
		t.Fatalf("first event = (%v, %d), want (true, 0)", allowed, suppressed)
	}
	if allowed, _ := audit.allow("node|uuid", "panel_domain", now.Add(time.Second)); allowed {
		t.Fatal("duplicate event was logged inside the interval")
	}
	if allowed, suppressed := audit.allow("node|uuid", "panel_domain", now.Add(10*time.Second)); !allowed || suppressed != 1 {
		t.Fatalf("summary event = (%v, %d), want (true, 1)", allowed, suppressed)
	}
}

func TestRejectionAuditBoundsKeys(t *testing.T) {
	now := time.Unix(100, 0)
	audit := &rejectionAudit{
		states:      make(map[string]*rejectionAuditState),
		interval:    10 * time.Second,
		maxKeys:     1,
		nextCleanup: now.Add(10 * time.Second),
	}
	if allowed, _ := audit.allow("first", "reason", now); !allowed {
		t.Fatal("first key was rejected")
	}
	if allowed, _ := audit.allow("second", "reason", now); allowed {
		t.Fatal("audit map accepted a key over capacity")
	}
}
