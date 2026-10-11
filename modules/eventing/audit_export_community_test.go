// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package eventing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

func TestCommunityAuditIntakeUnavailableAndStoredDeliveriesPark(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-edition")
	rc := newReceiver(t)
	h.createSubscription(admin, tenant, map[string]any{"name": "ledger", "event_types": []string{"audit.recorded"}, "endpoint": rc.srv.URL, "role": "admin"})
	in := AuditIntake{EventID: "historical-ledger", Seq: 1, Source: "olivares.audit", Payload: []byte(`{"seq":1}`), OccurredAt: time.Now().UTC()}
	if n, err := h.mod.IngestAudit(context.Background(), tenant, in); n != 0 || err == nil || !strings.Contains(err.Error(), "Business") {
		t.Errorf("intake = %d %v", n, err)
	}
	// Populate the ordinary queue with a historical audit delivery, as on an edition switch.
	if _, err := h.mod.captureOnce(context.Background(), tenant, event.Event{Type: typeAuditRecorded, Source: in.Source}, in.EventID, in.OccurredAt, in.Payload); err != nil {
		t.Fatal(err)
	}
	h.dispatch(tenant)
	if rc.count() != 0 {
		t.Fatalf("Community sent %d ledger payloads", rc.count())
	}
	rows := h.deliveryRows(tenant)
	if len(rows) != 1 {
		t.Fatalf("stored rows = %v", rows)
	}
	if rows[0].String(colDelStatus) != statusQueued || rows[0].Int(colDelAttempts) != 0 || rows[0].String(colDelLastStatus) != "audit_export_unavailable" {
		t.Fatalf("historical delivery was consumed: %v", rows[0])
	}
	// Retention remains authoritative: an expired payload is terminal, not rescheduled.
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		events, _, err := repo.List(context.Background(), model.Query{})
		if err != nil {
			return err
		}
		for _, ev := range events {
			if err := repo.Delete(context.Background(), model.ID(ev.String(model.ColID))); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.clk.advance(disabledRecheck + time.Second)
	h.dispatch(tenant)
	rows = h.deliveryRows(tenant)
	if len(rows) != 1 || rows[0].String(colDelStatus) != statusDead || rows[0].String(colDelLastStatus) != outcomeEventExpired || rows[0].Int(colDelAttempts) != 0 {
		t.Fatalf("expired historical event did not terminate: %v", rows)
	}

}
