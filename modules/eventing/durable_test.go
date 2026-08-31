// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

func durableFixture(tenant model.TenantID) event.Event {
	return event.Event{
		ID:     "11111111-2222-7333-8444-555555555555",
		Type:   typeWorkItemCreated,
		Tenant: tenant.String(),
		Source: "olivares.sessions",
		Time:   time.Date(2026, 8, 9, 18, 30, 0, 123456000, time.UTC),
		Payload: json.RawMessage(
			`{"work_item_id":"aaaaaaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee","state":"draft"}`,
		),
	}
}

// This is the immutable v1 payload emitted by sessions' DirectNotice publisher
// (TestDirectNoticeEventPayloadV1GoldenReplay). Keeping the Eventing-side fixture
// byte-exact proves the source WorkOutbox envelope crosses the durable intake
// without reinterpretation.
const directNoticeEventPayloadV1Golden = `{"ack_quorum":1,"audience_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","channel_id":"018f0000-0000-7000-8000-000000000001","command":"message.publish.direct","delivery_count":1,"event_sequence":1,"fulfillment":{"acknowledged":0,"quorum":1,"required":1,"state":"pending","unmet":0,"viable":1},"message_id":"018f0000-0000-7000-8000-000000000002","message_kind":"notice","payload_digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","plan_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","required_count":1,"result_id":"018f0000-0000-7000-8000-000000000002","result_kind":"sessions.message","schema_version":1,"state":"published","version":2}`

func directNoticeDurableFixture(tenant model.TenantID) event.Event {
	return event.Event{
		ID:      "018f0000-0000-7000-8000-000000000003",
		Type:    typeWorkMessageAvailable,
		Tenant:  tenant.String(),
		Source:  "olivares.sessions",
		Time:    time.Date(2026, 8, 16, 12, 0, 0, 123456000, time.UTC),
		Payload: json.RawMessage(directNoticeEventPayloadV1Golden),
	}
}

var sessionsProductionWorkEventPermissions = map[event.Type]string{
	typeWorkItemCreated:              "sessions:work:read",
	typeWorkItemTransitioned:         "sessions:work:read",
	typeWorkOwnerChanged:             "sessions:work:read",
	typeWorkDependencyChanged:        "sessions:work:read",
	typeWorkAcceptanceChanged:        "sessions:work:read",
	typeWorkMessageAvailable:         "sessions:message:read",
	typeWorkMessageAcknowledged:      "sessions:message:read",
	typeWorkMessageRetracted:         "sessions:message:read",
	typeWorkMessageExpired:           "sessions:message:read",
	typeWorkMessageOverdue:           "sessions:message:read",
	typeWorkMessageRerouted:          "sessions:message:read",
	typeWorkMessageEscalated:         "sessions:message:read",
	typeWorkProtocolReplyAvailable:   "sessions:message:read",
	typeWorkProtocolMessageReceived:  "sessions:message:read",
	typeWorkHandoffCarrierAvailable:  "sessions:message:read",
	typeWorkDecisionRecorded:         "sessions:decision:read",
	typeWorkDecisionRequestResponded: "sessions:decision-request:read",
	typeWorkDecisionRequestExpired:   "sessions:decision-request:read",
	typeWorkHandoffOffered:           "sessions:handoff:read",
	typeWorkHandoffAccepted:          "sessions:handoff:read",
	typeWorkHandoffRejected:          "sessions:handoff:read",
	typeWorkHandoffWithdrawn:         "sessions:handoff:read",
	typeWorkHandoffExpired:           "sessions:handoff:read",
	typeWorkLeaseAcquired:            "sessions:lease:read",
	typeWorkLeaseEnded:               "sessions:lease:read",
	typeWorkBindingReserved:          "sessions:work:read",
	typeWorkBindingObserved:          "sessions:work:read",
	typeWorkBindingAmbiguous:         "sessions:work:read",
	typeWorkBindingCancelRequested:   "sessions:work:read",
}

func eventRows(t *testing.T, h *harness, tenant model.TenantID) []model.Record {
	t.Helper()
	var out []model.Record
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: listCap})
		out = rows
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIngestDurableFailsClosedWhenUnwired(t *testing.T) {
	err := New().IngestDurable(context.Background(), durableFixture("00000000-0000-7000-8000-000000000001"))
	if !errors.Is(err, ErrDurableIntakeUnavailable) {
		t.Fatalf("unwired intake = %v, want ErrDurableIntakeUnavailable", err)
	}
}

func TestIngestDurableRejectsMalformedEnvelopeWithoutWriting(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "durable-invalid")
	valid := durableFixture(tenant)

	var nilPayload *struct{}
	tests := []struct {
		name string
		edit func(*event.Event)
	}{
		{name: "missing id", edit: func(e *event.Event) { e.ID = "" }},
		{name: "id whitespace", edit: func(e *event.Event) { e.ID = " " + e.ID }},
		{name: "id too long", edit: func(e *event.Event) { e.ID = strings.Repeat("x", maxDurableEventIDLen+1) }},
		{name: "unknown type", edit: func(e *event.Event) { e.Type = "work.unknown" }},
		{name: "invalid tenant", edit: func(e *event.Event) { e.Tenant = "not-a-tenant" }},
		{name: "system tenant", edit: func(e *event.Event) { e.Tenant = model.SystemTenantID.String() }},
		{name: "missing source", edit: func(e *event.Event) { e.Source = "" }},
		{name: "source whitespace", edit: func(e *event.Event) { e.Source = " olives" }},
		{name: "source too long", edit: func(e *event.Event) { e.Source = strings.Repeat("s", maxSourceLen+1) }},
		{name: "missing occurrence time", edit: func(e *event.Event) { e.Time = time.Time{} }},
		{name: "nil payload interface", edit: func(e *event.Event) { e.Payload = nil }},
		{name: "typed nil payload", edit: func(e *event.Event) { e.Payload = nilPayload }},
		{name: "JSON null payload", edit: func(e *event.Event) { e.Payload = json.RawMessage("null") }},
		{name: "malformed raw JSON", edit: func(e *event.Event) { e.Payload = json.RawMessage("{") }},
		{name: "unserializable payload", edit: func(e *event.Event) { e.Payload = math.NaN() }},
		{name: "oversize payload", edit: func(e *event.Event) { e.Payload = strings.Repeat("x", maxPayloadBytes) }},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := valid
			e.ID = model.NewID().String()
			tc.edit(&e)
			if err := h.mod.IngestDurable(context.Background(), e); !errors.Is(err, ErrInvalidDurableEvent) {
				t.Fatalf("IngestDurable = %v, want ErrInvalidDurableEvent", err)
			}
			if got := len(eventRows(t, h, tenant)); got != 0 {
				t.Fatalf("case %d wrote %d event rows, want zero", i, got)
			}
		})
	}
}

func TestIngestDurablePersistsWithoutSubscriptionAndBindsID(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "durable-unmatched")
	e := durableFixture(tenant)

	if err := h.mod.IngestDurable(context.Background(), e); err != nil {
		t.Fatalf("first intake: %v", err)
	}
	if got := len(eventRows(t, h, tenant)); got != 1 {
		t.Fatalf("event rows = %d, want 1: durable acknowledgement must retain an unmatched event", got)
	}
	if got := len(h.deliveryRows(tenant)); got != 0 {
		t.Fatalf("delivery rows = %d, want 0 without a matching subscription", got)
	}

	changed := e
	changed.Payload = json.RawMessage(`{"work_item_id":"different","state":"draft"}`)
	if err := h.mod.IngestDurable(context.Background(), changed); !errors.Is(err, ErrDurableEventIDConflict) {
		t.Fatalf("same id with changed content = %v, want ErrDurableEventIDConflict", err)
	}
	if got := len(eventRows(t, h, tenant)); got != 1 {
		t.Fatalf("conflicting replay changed row count to %d, want 1", got)
	}
}

func TestIngestDurableDeduplicatesExactReplayAndRejectsEveryRebindAxis(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "durable-dedup")
	editor := h.roleToken(admin, tenant, "durable@example.com", "editor")
	h.createSubscription(editor, tenant, map[string]any{
		"name": "work", "event_types": []string{string(typeWorkItemCreated), string(typeWorkItemTransitioned)},
		"endpoint": rc.srv.URL,
	})

	e := durableFixture(tenant)
	if err := h.mod.IngestDurable(context.Background(), e); err != nil {
		t.Fatalf("first intake: %v", err)
	}
	if err := h.mod.IngestDurable(context.Background(), e); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	waitFor(t, "one durable delivery", func() bool { return rc.count() == 1 })
	if got := len(eventRows(t, h, tenant)); got != 1 {
		t.Fatalf("event rows after replay = %d, want 1", got)
	}
	if got := len(h.deliveryRows(tenant)); got != 1 {
		t.Fatalf("delivery rows after replay = %d, want 1", got)
	}

	rebinds := []struct {
		name string
		edit func(*event.Event)
	}{
		{name: "type", edit: func(e *event.Event) { e.Type = typeWorkItemTransitioned }},
		{name: "source", edit: func(e *event.Event) { e.Source = "olivares.other" }},
		{name: "time", edit: func(e *event.Event) { e.Time = e.Time.Add(time.Nanosecond) }},
		{name: "payload", edit: func(e *event.Event) { e.Payload = json.RawMessage(`{"work_item_id":"different"}`) }},
	}
	for _, tc := range rebinds {
		t.Run(tc.name, func(t *testing.T) {
			changed := e
			tc.edit(&changed)
			if err := h.mod.IngestDurable(context.Background(), changed); !errors.Is(err, ErrDurableEventIDConflict) {
				t.Fatalf("rebind = %v, want ErrDurableEventIDConflict", err)
			}
		})
	}
	if got := len(eventRows(t, h, tenant)); got != 1 {
		t.Fatalf("rebind attempts changed event rows to %d, want 1", got)
	}
	if got := len(h.deliveryRows(tenant)); got != 1 {
		t.Fatalf("rebind attempts changed delivery rows to %d, want 1", got)
	}

	// The collision control is not deny-all: a genuinely new event ID with the
	// same content is a distinct command event and is captured and delivered.
	fresh := e
	fresh.ID = "66666666-7777-7888-8999-000000000000"
	if err := h.mod.IngestDurable(context.Background(), fresh); err != nil {
		t.Fatalf("new id with same payload: %v", err)
	}
	waitFor(t, "second genuine durable delivery", func() bool { return rc.count() == 2 })
	if got := len(eventRows(t, h, tenant)); got != 2 {
		t.Fatalf("event rows after a genuine second event = %d, want 2", got)
	}
}

func TestIngestDurableAcceptsDirectNoticeWorkOutboxEventAndDeduplicatesReplay(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "durable-direct-notice")
	editor := h.roleToken(admin, tenant, "direct-notice@example.com", "editor")
	h.createSubscription(editor, tenant, map[string]any{
		"name": "direct-notice", "event_types": []string{string(typeWorkMessageAvailable)},
		"endpoint": rc.srv.URL,
	})

	e := directNoticeDurableFixture(tenant)
	if err := h.mod.IngestDurable(context.Background(), e); err != nil {
		t.Fatalf("DirectNotice WorkOutbox intake: %v", err)
	}
	if err := h.mod.IngestDurable(context.Background(), e); err != nil {
		t.Fatalf("exact DirectNotice WorkOutbox replay: %v", err)
	}
	waitFor(t, "one DirectNotice delivery", func() bool { return rc.count() == 1 })

	rows := eventRows(t, h, tenant)
	if len(rows) != 1 || rows[0].String(colEvEventID) != e.ID ||
		rows[0].String(colEvType) != string(typeWorkMessageAvailable) ||
		rows[0].String(colEvSource) != e.Source ||
		rows[0].String(colEvPayload) != directNoticeEventPayloadV1Golden {
		t.Fatalf("captured DirectNotice event = %#v, want one byte-exact WorkOutbox fact", rows)
	}
	if got := len(h.deliveryRows(tenant)); got != 1 {
		t.Fatalf("DirectNotice deliveries after exact replay = %d, want 1", got)
	}

	var subject struct {
		SchemaVersion int64  `json:"schema_version"`
		Command       string `json:"command"`
		ResultKind    string `json:"result_kind"`
		ResultID      string `json:"result_id"`
		MessageID     string `json:"message_id"`
		MessageKind   string `json:"message_kind"`
		State         string `json:"state"`
		EventSequence int64  `json:"event_sequence"`
	}
	if err := json.Unmarshal([]byte(rows[0].String(colEvPayload)), &subject); err != nil {
		t.Fatalf("decode captured DirectNotice subject: %v", err)
	}
	if subject.SchemaVersion != 1 || subject.Command != "message.publish.direct" ||
		subject.ResultKind != "sessions.message" || subject.ResultID != subject.MessageID ||
		subject.MessageKind != "notice" || subject.State != "published" || subject.EventSequence != 1 {
		t.Fatalf("captured DirectNotice subject/schema = %#v", subject)
	}

	// Verification mutant: the stable WorkOutbox event ID binds the Message
	// subject too. Re-labeling only message_id cannot be accepted as a replay.
	rebound := e
	rebound.Payload = json.RawMessage(strings.Replace(
		directNoticeEventPayloadV1Golden,
		`"message_id":"018f0000-0000-7000-8000-000000000002"`,
		`"message_id":"018f0000-0000-7000-8000-000000000099"`, 1,
	))
	if err := h.mod.IngestDurable(context.Background(), rebound); !errors.Is(err, ErrDurableEventIDConflict) {
		t.Fatalf("DirectNotice subject rebind = %v, want ErrDurableEventIDConflict", err)
	}
	if got := len(eventRows(t, h, tenant)); got != 1 {
		t.Fatalf("DirectNotice subject rebind changed event rows to %d, want 1", got)
	}
}

func TestIngestDurableAcceptsEverySessionsProductionWorkEventType(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "durable-sessions-vocabulary")
	wantTypes := make(map[string]bool, len(sessionsProductionWorkEventPermissions))

	for typ := range sessionsProductionWorkEventPermissions {
		typ := typ
		t.Run(string(typ), func(t *testing.T) {
			e := durableFixture(tenant)
			e.ID = model.NewID().String()
			e.Type = typ
			e.Payload = map[string]any{"schema_version": int64(1), "event_type": typ}
			if err := h.mod.IngestDurable(context.Background(), e); err != nil {
				t.Fatalf("IngestDurable(%q) = %v", typ, err)
			}
			wantTypes[string(typ)] = true
		})
	}

	rows := eventRows(t, h, tenant)
	if len(rows) != len(wantTypes) {
		t.Fatalf("durable sessions event rows = %d, want %d", len(rows), len(wantTypes))
	}
	for _, row := range rows {
		typ := row.String(colEvType)
		if !wantTypes[typ] {
			t.Errorf("durable intake stored unexpected sessions event type %q", typ)
		}
		delete(wantTypes, typ)
	}
	if len(wantTypes) != 0 {
		t.Fatalf("durable intake omitted sessions event types: %v", wantTypes)
	}
}

func TestIngestDurableConcurrentDifferentContentHasOneWinner(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "durable-race")
	a := durableFixture(tenant)
	b := a
	b.Payload = json.RawMessage(`{"work_item_id":"bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb","state":"draft"}`)

	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i, e := range []event.Event{a, b} {
		i, e := i, e
		go func() {
			defer wg.Done()
			<-start
			errs[i] = h.mod.IngestDurable(context.Background(), e)
		}()
	}
	close(start)
	wg.Wait()

	winners, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrDurableEventIDConflict):
			conflicts++
		default:
			t.Fatalf("unexpected racer result: %v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("race results: winners=%d conflicts=%d, want exactly one of each", winners, conflicts)
	}
	if got := len(eventRows(t, h, tenant)); got != 1 {
		t.Fatalf("race stored %d event rows, want 1", got)
	}
}

func TestIngestDurableEventIDBindingIsTenantScoped(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "durable-tenant-a")
	tenantB := h.createOrg(admin, "durable-tenant-b")

	a := durableFixture(tenantA)
	b := durableFixture(tenantB)
	b.Source = "olivares.other"
	b.Payload = json.RawMessage(`{"work_item_id":"tenant-b","state":"ready"}`)

	if err := h.mod.IngestDurable(context.Background(), a); err != nil {
		t.Fatalf("tenant A intake: %v", err)
	}
	if err := h.mod.IngestDurable(context.Background(), b); err != nil {
		t.Fatalf("same event id in tenant B: %v", err)
	}
	if got := len(eventRows(t, h, tenantA)); got != 1 {
		t.Fatalf("tenant A event rows = %d, want 1", got)
	}
	if got := len(eventRows(t, h, tenantB)); got != 1 {
		t.Fatalf("tenant B event rows = %d, want 1", got)
	}
}

func TestWorkEventCatalogIsDurableOnly(t *testing.T) {
	want := sessionsProductionWorkEventPermissions

	busTypes := map[event.Type]bool{}
	for _, typ := range catalogTypes() {
		busTypes[typ] = true
	}
	seen := map[event.Type]bool{}
	for _, info := range Catalog() {
		permission, isWork := want[info.Type]
		if !isWork {
			continue
		}
		seen[info.Type] = true
		if !info.Internal {
			t.Errorf("work type %q is not Internal; bus capture could acknowledge it outside the source outbox", info.Type)
		}
		if busTypes[info.Type] {
			t.Errorf("work type %q appears in the bus subscription set", info.Type)
		}
		if string(info.Permission) != permission {
			t.Errorf("work type %q permission = %q, want %q", info.Type, info.Permission, permission)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("catalog contains %d/%d required durable work types", len(seen), len(want))
	}
}
