// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"errors"
	"testing"
	"time"
)

func TestTaskLedgerCapTTLAndSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	ledger := newTaskLedger(2, clock)

	for _, id := range []string{"t1", "t2"} {
		if _, err := ledger.insert(TaskRecord{TaskID: id, Subject: "agent-a", Tenant: "t", Tool: "search"}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	if _, err := ledger.insert(TaskRecord{TaskID: "t3", Subject: "agent-a", Tenant: "t", Tool: "search"}); !errors.Is(err, errTaskSubjectCap) {
		t.Fatalf("third active task must hit subject cap, got %v", err)
	}

	if _, err := ledger.insert(TaskRecord{TaskID: "t4", Subject: "agent-b", Tenant: "t", Tool: "search"}); err != nil {
		t.Fatalf("different subject should have its own cap: %v", err)
	}
	active := ledger.active(func(rec TaskRecord) bool { return rec.Tenant == "t" })
	if len(active) != 3 {
		t.Fatalf("active snapshot = %d, want 3", len(active))
	}
	active[0].Status = taskStatusCanceled
	got, ok := ledger.get(active[0].TaskID)
	if !ok || got.Status == taskStatusCanceled {
		t.Fatalf("snapshot must be a copy, got ok=%v rec=%+v", ok, got)
	}

	ttl := int64(1000)
	if _, err := ledger.insert(TaskRecord{
		TaskID: "ttl", Subject: "agent-c", Tenant: "t", Tool: "search",
		CreatedAt: now, TTLMs: &ttl,
	}); err != nil {
		t.Fatalf("insert ttl: %v", err)
	}
	now = now.Add(time.Second)
	if _, ok := ledger.get("ttl"); ok {
		t.Fatal("expired task must be evicted lazily on read")
	}
}
