// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"strings"
	"testing"
	"time"
)

// IngestAudit enqueues a delivery per matching audit.recorded subscription, is
// idempotent on the audit event id (a re-walk after a crash enqueues nothing new),
// and is storage-frugal (no subscription ⇒ nothing enqueued).
func TestIngestAuditEnqueuesIdempotentAndFrugal(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	rc := newReceiver(t)
	ctx := context.Background()

	occurred := time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC)
	payload := []byte(`{"id":"led-1","tenant_id":"` + tenant.String() + `","seq":7,"action":"agent.create","hash":"ab","prev_hash":"","sig":""}`)
	in := AuditIntake{EventID: "led-1", Seq: 7, OccurredAt: occurred, Source: "olivares.audit", Payload: payload}

	// No audit.recorded subscription yet: storage-frugal — nothing enqueued.
	if n, err := h.mod.IngestAudit(ctx, tenant, in); err != nil || n != 0 {
		t.Fatalf("frugal intake: n=%d err=%v, want 0/nil", n, err)
	}

	// Subscribe to the ledger feed (generic webhook for this test).
	h.createSubscription(admin, tenant, map[string]any{
		"name": "ledger", "event_types": []string{"audit.recorded"},
		"endpoint": rc.srv.URL, "role": "admin",
	})

	n, err := h.mod.IngestAudit(ctx, tenant, in)
	if err != nil || n != 1 {
		t.Fatalf("first intake: n=%d err=%v, want 1/nil", n, err)
	}
	// Re-walk of the SAME record (crash recovery / pump+tee overlap): idempotent.
	if n, err := h.mod.IngestAudit(ctx, tenant, in); err != nil || n != 0 {
		t.Fatalf("re-ingest must dedup: n=%d err=%v, want 0/nil", n, err)
	}

	h.dispatch(tenant)
	waitFor(t, "ledger delivery", func() bool { return rc.count() == 1 })
	body := string(rc.all()[0].body)
	if !strings.Contains(body, `"Type":"audit.recorded"`) {
		t.Fatalf("delivery type = %q", body)
	}
	if !strings.Contains(body, "led-1") {
		t.Fatalf("delivery must carry the ledger record: %q", body)
	}

	// Exactly one event row + one delivery row for the ledger record.
	rows := h.deliveryRows(tenant)
	if len(rows) != 1 {
		t.Fatalf("delivery rows = %d, want 1 (idempotent intake)", len(rows))
	}
}

// IngestAudit rejects a malformed intake (missing id/payload) fail-closed.
func TestIngestAuditValidates(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	ctx := context.Background()
	if _, err := h.mod.IngestAudit(ctx, tenant, AuditIntake{Seq: 1, Payload: []byte("x")}); err == nil {
		t.Fatal("missing event id must error")
	}
	if _, err := h.mod.IngestAudit(ctx, tenant, AuditIntake{EventID: "x"}); err == nil {
		t.Fatal("missing payload must error")
	}
}
