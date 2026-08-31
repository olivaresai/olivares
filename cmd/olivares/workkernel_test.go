// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestWorkEventSinkUsesRealDurableEventingIntake(t *testing.T) {
	ctx := context.Background()
	em := eventing.New()
	st, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", Debug: true,
	}, em.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var tenant model.TenantID
	if err := st.System(ctx, func(sc store.SystemScope) error {
		if _, err := sc.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sc.CreateOrg(ctx, model.Org{Name: "work-eventing", Slug: "work-eventing", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	em.UseData(api.NewModuleData(st))

	eventID, workItemID := model.NewID(), model.NewID()
	envelope := sessions.WorkEventEnvelope{
		TenantID: tenant, WorkspaceID: model.NewID(), EventID: eventID,
		AggregateKind: "sessions.work_item", AggregateID: workItemID, Sequence: 1,
		Type: "work.item.created", OccurredAt: model.NewTimestamp(time.Now().UTC()).String(),
		Payload: []byte(`{"command":"item.create","status":"draft","work_item_id":"` + workItemID.String() + `"}`),
	}
	sink := workEventSink{eventing: em}
	if err := sink.IngestDurable(ctx, envelope); err != nil {
		t.Fatalf("first durable intake: %v", err)
	}
	if err := sink.IngestDurable(ctx, envelope); err != nil {
		t.Fatalf("exact adapter replay: %v", err)
	}
	changed := envelope
	changed.Payload = []byte(`{"command":"item.create","status":"ready"}`)
	if err := sink.IngestDurable(ctx, changed); !errors.Is(err, eventing.ErrDurableEventIDConflict) {
		t.Fatalf("adapter event-id rebind = %v, want conflict", err)
	}
	invalid := envelope
	invalid.EventID = model.NewID()
	invalid.Payload = []byte(`{"unterminated"`)
	if err := sink.IngestDurable(ctx, invalid); !errors.Is(err, eventing.ErrInvalidDurableEvent) {
		t.Fatalf("adapter invalid JSON payload = %v, want ErrInvalidDurableEvent", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("eventing.event"))
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{{
			Column: "event_id", Op: model.OpEq, Value: eventID.String(),
		}}, Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].String("event_type") != envelope.Type ||
			rows[0].String("payload") != string(envelope.Payload) {
			t.Fatalf("durable Eventing rows = %#v, want one bound work event", rows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
