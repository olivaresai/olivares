// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/webhook"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The catalog endpoint publishes every bus and durable-intake type with its
// tier and receive permission (the public contract).
func TestEventTypesCatalogEndpoint(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	r := h.do("GET", "/v1/m/eventing/event-types", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("event-types = %d %s", r.code, r.raw)
	}
	items := r.body["event_types"].([]any)
	if len(items) != len(Catalog()) {
		t.Fatalf("catalog endpoint size = %d, want in-code catalog size %d", len(items), len(Catalog()))
	}
	tiers := map[string]string{}
	perms := map[string]string{}
	for _, it := range items {
		m := it.(map[string]any)
		tiers[m["type"].(string)] = m["stability"].(string)
		perms[m["type"].(string)] = m["permission"].(string)
	}
	for typ, want := range map[string]string{
		"edge.observed": "stable", "cost.sampled": "stable", "finding.reported": "stable",
		"guardrail.observed": "beta", "approval.requested": "beta", "approval.resolved": "beta", "policy.changed": "beta",
		"audit.recorded": "stable", "metric.sampled": "beta", "workflow.signal": "beta",
		"work.item.created": "beta", "work.item.transitioned": "beta", "work.owner.changed": "beta",
		"work.dependency.changed": "beta", "work.acceptance.changed": "beta", "work.decision.recorded": "beta",
		"work.message.available": "beta", "work.lease.acquired": "beta", "work.lease.ended": "beta",
		"work.protocol.reply.available":  "beta",
		"work.protocol.message.received": "beta",
		"work.binding.reserved":          "beta", "work.binding.observed": "beta", "work.binding.ambiguous": "beta",
		"work.binding.cancel_requested": "beta",
	} {
		if tiers[typ] != want {
			t.Errorf("tier[%s] = %q, want %q", typ, tiers[typ], want)
		}
	}
	if perms["guardrail.observed"] != "security:observed:read" {
		t.Errorf("guardrail.observed permission = %q, want security:observed:read", perms["guardrail.observed"])
	}
	// the raw sample can name a developer — the privileged drill-down
	// permission, never the viewer-tier aggregate read.
	if perms["metric.sampled"] != "adoption:developer:read" {
		t.Errorf("metric.sampled permission = %q, want adoption:developer:read", perms["metric.sampled"])
	}
	if perms["work.item.created"] != "sessions:work:read" {
		t.Errorf("work.item.created permission = %q, want sessions:work:read", perms["work.item.created"])
	}
	if perms["work.decision.recorded"] != "sessions:decision:read" {
		t.Errorf("work.decision.recorded permission = %q, want sessions:decision:read", perms["work.decision.recorded"])
	}
	if perms["work.message.available"] != "sessions:message:read" {
		t.Errorf("work.message.available permission = %q, want sessions:message:read", perms["work.message.available"])
	}
	if perms["work.protocol.reply.available"] != "sessions:message:read" {
		t.Errorf("work.protocol.reply.available permission = %q, want sessions:message:read", perms["work.protocol.reply.available"])
	}
	if perms["work.protocol.message.received"] != "sessions:message:read" {
		t.Errorf("work.protocol.message.received permission = %q, want sessions:message:read", perms["work.protocol.message.received"])
	}
	for _, typ := range []string{"work.lease.acquired", "work.lease.ended"} {
		if perms[typ] != "sessions:lease:read" {
			t.Errorf("%s permission = %q, want sessions:lease:read", typ, perms[typ])
		}
	}
	for _, typ := range []string{
		"work.binding.reserved", "work.binding.observed",
		"work.binding.ambiguous", "work.binding.cancel_requested",
	} {
		if perms[typ] != "sessions:work:read" {
			t.Errorf("%s permission = %q, want sessions:work:read", typ, perms[typ])
		}
	}
}

// Authoring validation: endpoint scheme, unknown types, the role ceiling, and
// the static role-vs-type check (privileged types never authored below
// editor) all reject with clear 400s.
func TestSubscriptionValidation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	base := func() map[string]any {
		return map[string]any{
			"name": "s1", "event_types": []string{"finding.reported"},
			"endpoint": "https://consumer.example.com/hook",
		}
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"plain http refused", func(m map[string]any) { m["endpoint"] = "http://consumer.example.com/hook" }, "https"},
		{"credentials in url refused", func(m map[string]any) { m["endpoint"] = "https://u:p@consumer.example.com/h" }, "credentials"},
		{"unknown type refused", func(m map[string]any) { m["event_types"] = []string{"nonsense.type"} }, "unknown event type"},
		{"role above caller refused", func(m map[string]any) { m["role"] = "admin" }, "role ceiling"},
		{"viewer role cannot take privileged types", func(m map[string]any) {
			m["role"], m["event_types"] = "viewer", []string{"edge.observed"}
		}, "cannot receive"},
		{"viewer role cannot take observed text", func(m map[string]any) {
			m["role"], m["event_types"] = "viewer", []string{"guardrail.observed"}
		}, "cannot receive"},
		{"empty types refused", func(m map[string]any) { m["event_types"] = []string{} }, "at least one"},
	}
	for _, tc := range cases {
		in := base()
		tc.mutate(in)
		r := h.do("POST", "/v1/m/eventing/subscriptions", editor, in, tenantHdr(tenant))
		if r.code != http.StatusBadRequest || !strings.Contains(r.raw, tc.want) {
			t.Errorf("%s: code=%d body=%s (want 400 containing %q)", tc.name, r.code, r.raw, tc.want)
		}
	}
	// An editor authoring an editor-role subscription for a privileged type is fine.
	if r := h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "edges", "event_types": []string{"edge.observed"}, "role": "editor",
		"endpoint": "https://consumer.example.com/hook",
	}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("editor+edge.observed = %d %s", r.code, r.raw)
	}
}

// The secret lifecycle: returned exactly once at create, never readable later,
// rotated on demand (new cleartext once, new hint), and never stored in clear.
func TestSecretLifecycle(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	id, secret := h.createSubscription(editor, tenant, map[string]any{
		"name": "s1", "event_types": []string{"finding.reported"},
		"endpoint": "https://consumer.example.com/hook",
	})
	if !strings.HasPrefix(secret, "olvw_") || len(secret) < 20 {
		t.Fatalf("secret shape = %q, want olvw_<hex>", secret)
	}
	r := h.do("GET", "/v1/m/eventing/subscriptions/"+id, editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("get = %d %s", r.code, r.raw)
	}
	if _, leaked := r.body["secret"]; leaked {
		t.Error("GET must never return the secret")
	}
	if hint := r.body["secret_hint"].(string); len(hint) != 12 || strings.Contains(secret, hint) {
		t.Errorf("secret_hint = %q: want a 12-char fingerprint, never a substring of the secret", hint)
	}
	// The cleartext never lands in the store (the sealed form is opaque).
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(id))
		if err != nil {
			return err
		}
		if strings.Contains(rec.String(colSubSecret), secret) {
			t.Error("the stored sealed secret must not contain the cleartext")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	r = h.do("POST", "/v1/m/eventing/subscriptions/"+id+"/rotate-secret", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("rotate = %d %s", r.code, r.raw)
	}
	rotated := r.body["secret"].(string)
	if rotated == secret || !strings.HasPrefix(rotated, "olvw_") {
		t.Errorf("rotate must mint a fresh secret (old=%q new=%q)", secret, rotated)
	}
	got := h.auditActions(tenant)
	want := map[string]bool{"eventing.subscription.create": false, "eventing.subscription.rotate_secret": false}
	for _, a := range got {
		if _, ok := want[a]; ok {
			want[a] = true
		}
	}
	for a, seen := range want {
		if !seen {
			t.Errorf("audit action %s not recorded (got %v)", a, got)
		}
	}
}

// Without a wired SecretSealer the platform refuses to hold a secret:
// subscription creation fails closed with an actionable 503.
func TestSealerRequired(t *testing.T) {
	h := newHarness(t, WithSecretSealer(nopSealer{}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	r := h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "s1", "event_types": []string{"finding.reported"},
		"endpoint": "https://consumer.example.com/hook",
	}, tenantHdr(tenant))
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("create without sealer = %d %s, want 503", r.code, r.raw)
	}
}

// The happy path: a published finding is captured, delivered once to the
// consumer with a valid HMAC signature, the envelope (Go field names +
// Seq), and the idempotency headers; the delivery row finishes delivered.
func TestDeliverySignedHappyPath(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	_, secret := h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})

	h.publishFinding(tenant, "module:security", "guardrail", "Prompt injection blocked")
	waitFor(t, "delivery to reach the receiver", func() bool { return rc.count() >= 1 })

	req := rc.all()[0]
	ts := req.header.Get(headerTimestamp)
	sig := req.header.Get(headerSignature)
	if ts == "" || sig == "" {
		t.Fatalf("missing signature headers: ts=%q sig=%q", ts, sig)
	}
	if !webhook.Verify(secret, ts, sig, req.body) {
		t.Error("delivery signature must verify against the subscription secret (scheme)")
	}
	if webhook.Verify("olvw_wrong", ts, sig, req.body) {
		t.Error("signature must not verify under a different secret")
	}
	if req.header.Get(headerEvent) == "" || req.header.Get(headerDelivery) == "" {
		t.Error("idempotency headers X-Olivares-Event / X-Olivares-Delivery must be present")
	}
	if got := req.header.Get(headerEventType); got != "finding.reported" {
		t.Errorf("X-Olivares-Event-Type = %q", got)
	}

	var env struct {
		ID, Type, Tenant, Source string
		Seq                      int64
		Payload                  map[string]any
	}
	if err := json.Unmarshal(req.body, &env); err != nil {
		t.Fatalf("envelope not JSON: %v", err)
	}
	if env.Type != "finding.reported" || env.Tenant != tenant.String() || env.Source != "module:security" || env.Seq != 1 {
		t.Errorf("envelope = %+v", env)
	}
	if env.ID != req.header.Get(headerEvent) {
		t.Error("envelope ID must equal the X-Olivares-Event idempotency key")
	}
	if env.Payload["Title"] != "Prompt injection blocked" || env.Payload["Kind"] != "guardrail" {
		t.Errorf("payload uses the SDK Go field names: %v", env.Payload)
	}

	waitFor(t, "delivery row to finish", func() bool {
		ds := h.deliveries(editor, tenant, "status=delivered")
		return len(ds) == 1
	})
	d := h.deliveries(editor, tenant, "")[0]
	if d["status"] != "delivered" || d["attempts"].(float64) != 1 || d["origin"] != "live" {
		t.Errorf("delivery row = %v", d)
	}
}

// Failures retry on the ladder and exhaust into the DLQ; a redeliver after the
// consumer heals drains it.
func TestRetryBackoffAndDLQ(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	rc.setStatus(http.StatusInternalServerError)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})
	h.publishFinding(tenant, "module:security", "guardrail", "t")
	// Wait for the worker's OUTCOME WRITE (not just the receiver seeing the
	// request): a manual dispatch pass against a still-"delivering" row would
	// no-op and the clock advance would be wasted.
	h.waitAttempts(editor, tenant, 1)

	// Two scheduled retries (the harness ladder), then dead. dispatch() is
	// synchronous, so each pass's outcome is committed when it returns.
	for i := 0; i < 2; i++ {
		h.clk.advance(time.Second)
		h.dispatch(tenant)
	}
	waitFor(t, "delivery to dead-letter", func() bool {
		return len(h.deliveries(editor, tenant, "status=dead")) == 1
	})
	dead := h.deliveries(editor, tenant, "status=dead")[0]
	if dead["attempts"].(float64) != 3 { // initial + 2 retries
		t.Errorf("attempts = %v, want 3", dead["attempts"])
	}
	if dead["last_status"] != "http_500" {
		t.Errorf("last_status = %v, want http_500", dead["last_status"])
	}
	if got := rc.count(); got != 3 {
		t.Errorf("receiver saw %d attempts, want 3", got)
	}

	// DLQ drain: heal the consumer, redeliver the dead row.
	rc.setStatus(http.StatusOK)
	id := dead["id"].(string)
	if r := h.do("POST", "/v1/m/eventing/deliveries/"+id+"/redeliver", admin, nil, tenantHdr(tenant)); r.code != http.StatusAccepted {
		t.Fatalf("redeliver = %d %s", r.code, r.raw)
	}
	waitFor(t, "redelivery to land", func() bool {
		return len(h.deliveries(editor, tenant, "status=delivered")) == 1
	})
	// The idempotency key is stable across the whole story.
	reqs := rc.all()
	first, last := reqs[0].header.Get(headerEvent), reqs[len(reqs)-1].header.Get(headerEvent)
	if first == "" || first != last {
		t.Errorf("X-Olivares-Event changed across retries/redelivery: %q vs %q", first, last)
	}
}

// denyPerm emulates an ABAC restriction: it forwards to the real authorizer
// but denies one permission outright.
type denyPerm struct {
	inner Authz
	perm  auth.Permission
}

func (d denyPerm) Allowed(ctx context.Context, p auth.Principal, perm auth.Permission, tenant model.TenantID) bool {
	if perm == d.perm {
		return false
	}
	return d.inner.Allowed(ctx, p, perm, tenant)
}

// The deny-closed per-event RBAC filter: a policy restriction at delivery time
// marks the delivery denied and NOTHING reaches the consumer — even though the
// subscription was validly authored.
func TestRBACDeniedAtDeliveryTime(t *testing.T) {
	h := newHarness(t, WithAuthorizer(denyPerm{inner: auth.NewAuthorizer(nil), perm: "accessgraph:read"}))
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "edges", "event_types": []string{"edge.observed"}, "role": "editor",
		"endpoint": rc.srv.URL,
	})
	h.publishEdge(tenant, "connector:pg")
	// Assert against the store: the same deny also (correctly) hides
	// edge.observed rows from the list endpoint's caller-side filter.
	waitFor(t, "delivery to be denied", func() bool {
		rows := h.deliveryRows(tenant)
		return len(rows) == 1 && rows[0].String(colDelStatus) == statusDenied
	})
	if rc.count() != 0 {
		t.Errorf("receiver saw %d deliveries, want 0 (deny-closed)", rc.count())
	}
	if got := h.deliveryRows(tenant)[0].String(colDelLastStatus); got != outcomeDenied {
		t.Errorf("last_status = %v", got)
	}
}

// Without a wired authorizer NOTHING is delivered: captures park as queued
// (recoverable once wired) — never an unauthorized send.
func TestNoAuthorizerParksDeliveries(t *testing.T) {
	h := newHarness(t, func(m *Module) { m.authz = nil })
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})
	h.publishFinding(tenant, "module:security", "guardrail", "t")
	// The list endpoints are (correctly) 503 without an authorizer, so assert
	// against the store.
	waitFor(t, "capture to enqueue", func() bool {
		return len(h.deliveryRows(tenant)) == 1
	})
	h.dispatch(tenant) // explicit pass: still parked
	if rc.count() != 0 {
		t.Errorf("receiver saw %d deliveries, want 0", rc.count())
	}
	if got := h.deliveryRows(tenant)[0].String(colDelStatus); got != statusQueued {
		t.Errorf("delivery status = %v, want queued (parked)", got)
	}
	// And the read surface is an actionable 503, not an empty 200.
	if r := h.do("GET", "/v1/m/eventing/deliveries", editor, nil, tenantHdr(tenant)); r.code != http.StatusServiceUnavailable {
		t.Errorf("deliveries without authorizer = %d, want 503", r.code)
	}
}

// Replay from a cursor re-enqueues matching retained events as NEW deliveries
// (origin=replay) with the SAME idempotency keys, honoring the subscription's
// type filter.
func TestReplayFromCursor(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	// Two subscriptions so BOTH types are captured; replay targets only subA.
	subA, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})
	rcB := newReceiver(t)
	h.createSubscription(editor, tenant, map[string]any{
		"name": "edges", "event_types": []string{"edge.observed"}, "role": "editor",
		"endpoint": rcB.srv.URL,
	})

	h.publishFinding(tenant, "module:security", "guardrail", "one")
	h.publishEdge(tenant, "connector:pg")
	h.publishFinding(tenant, "module:security", "guardrail", "two")
	waitFor(t, "live deliveries", func() bool { return rc.count() == 2 && rcB.count() == 1 })

	r := h.do("POST", "/v1/m/eventing/subscriptions/"+subA+"/replay", admin,
		map[string]any{"from_seq": 1}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("replay = %d %s", r.code, r.raw)
	}
	if got := r.body["replayed"].(float64); got != 2 {
		t.Errorf("replayed = %v, want 2 (the edge event is filtered by type)", got)
	}
	waitFor(t, "replayed deliveries", func() bool { return rc.count() == 4 })
	if rcB.count() != 1 {
		t.Errorf("subscription B must not receive subscription A's replay")
	}
	if got := len(h.deliveries(editor, tenant, "subscription="+subA)); got != 4 {
		t.Errorf("subscription A delivery rows = %d, want 4 (2 live + 2 replay)", got)
	}
	// Replays carry the SAME event ids (idempotency) and a replay origin.
	ids := map[string]int{}
	for _, req := range rc.all() {
		ids[req.header.Get(headerEvent)]++
	}
	if len(ids) != 2 {
		t.Errorf("distinct event ids = %d, want 2 (each delivered twice)", len(ids))
	}
	for id, n := range ids {
		if n != 2 {
			t.Errorf("event %s delivered %d times, want 2", id, n)
		}
	}
}

// The pull-side event log: per-type RBAC filters the CALLER (deny-closed), and
// an explicitly requested forbidden type is an explicit 403.
func TestEventsListRBACFiltered(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "both", "event_types": []string{"finding.reported", "edge.observed"}, "role": "editor",
		"endpoint": rc.srv.URL,
	})
	h.publishFinding(tenant, "module:security", "guardrail", "f1")
	h.publishEdge(tenant, "connector:pg")
	waitFor(t, "both captured", func() bool { return rc.count() == 2 })

	re := h.do("GET", "/v1/m/eventing/events", editor, nil, tenantHdr(tenant))
	if re.code != http.StatusOK || len(re.body["items"].([]any)) != 2 {
		t.Fatalf("editor events = %d items=%v", re.code, re.body["items"])
	}
	rv := h.do("GET", "/v1/m/eventing/events", viewer, nil, tenantHdr(tenant))
	if rv.code != http.StatusOK {
		t.Fatalf("viewer events = %d %s", rv.code, rv.raw)
	}
	items := rv.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["type"] != "finding.reported" {
		t.Errorf("viewer must see only finding.reported: %v", items)
	}
	if r := h.do("GET", "/v1/m/eventing/events?type=edge.observed", viewer, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("explicit forbidden type = %d, want 403", r.code)
	}
	if r := h.do("GET", "/v1/m/eventing/events?type=edge.observed", editor, nil, tenantHdr(tenant)); r.code != http.StatusOK || len(r.body["items"].([]any)) != 1 {
		t.Errorf("editor explicit type = %d items=%v", r.code, r.body["items"])
	}
}

// Capture is storage-frugal and tenant-scoped: no matching enabled
// subscription, no capture; another tenant's events never land here.
func TestCaptureRequiresMatchingSubscription(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")
	editorA := h.roleToken(admin, tenantA, "a@x.io", "editor")

	// Before any subscription exists: nothing is captured.
	h.publishFinding(tenantA, "module:security", "guardrail", "lost")
	// The bus hands events to handlers asynchronously, so without this barrier
	// "lost" can be handled AFTER the subscription below exists — and then it IS
	// captured, which is the opposite of what this test asserts. Measured twice on
	// CI (runs 30652967521 and 30661040132), failing both ways: once as "captured
	// events = 2, want 1", once as a timeout waiting for the delivery.
	h.busBarrier()
	h.createSubscription(editorA, tenantA, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})
	// Tenant B publishes (no subscription there) and A publishes (captured).
	h.publishFinding(tenantB, "module:security", "guardrail", "other-tenant")
	h.publishFinding(tenantA, "module:security", "guardrail", "mine")
	waitFor(t, "tenant A delivery", func() bool { return rc.count() == 1 })

	r := h.do("GET", "/v1/m/eventing/events", editorA, nil, tenantHdr(tenantA))
	items := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("captured events = %d, want 1 (pre-subscription and other-tenant events never captured)", len(items))
	}
	// Replay cannot reach back before capture.
	subID := h.deliveries(editorA, tenantA, "")[0]["subscription"].(string)
	rr := h.do("POST", "/v1/m/eventing/subscriptions/"+subID+"/replay", admin, map[string]any{"from_seq": 1}, tenantHdr(tenantA))
	if rr.body["replayed"].(float64) != 1 {
		t.Errorf("replayed = %v, want 1", rr.body["replayed"])
	}
}

// A disabled subscription parks its queued deliveries (never consumes them);
// re-enabling resumes the stream.
func TestDisabledSubscriptionParksAndResumes(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	rc.setStatus(http.StatusServiceUnavailable)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	id, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})
	h.publishFinding(tenant, "module:security", "guardrail", "t")
	h.waitAttempts(editor, tenant, 1) // outcome committed, not just request seen

	// Disable, then run the due pass: the delivery defers instead of attempting.
	if r := h.do("PUT", "/v1/m/eventing/subscriptions/"+id, editor, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL, "enabled": false,
	}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("disable = %d %s", r.code, r.raw)
	}
	attempts := rc.count()
	h.clk.advance(time.Second)
	h.dispatch(tenant)
	if rc.count() != attempts {
		t.Errorf("a disabled subscription must not be attempted (saw %d, had %d)", rc.count(), attempts)
	}
	if d := h.deliveries(editor, tenant, "")[0]; d["status"] != "queued" || d["last_status"] != "subscription_disabled" {
		t.Errorf("delivery = %v, want queued/subscription_disabled", d)
	}

	// Re-enable + heal: the parked delivery resumes past the recheck window.
	rc.setStatus(http.StatusOK)
	if r := h.do("PUT", "/v1/m/eventing/subscriptions/"+id, editor, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL, "enabled": true,
	}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("re-enable = %d %s", r.code, r.raw)
	}
	h.clk.advance(disabledRecheck + time.Minute)
	h.dispatch(tenant)
	waitFor(t, "parked delivery to land", func() bool {
		return len(h.deliveries(editor, tenant, "status=delivered")) == 1
	})
}

// The retention sweep prunes captured events and finished deliveries past the
// window — and with them the replay horizon.
func TestPruneExpired(t *testing.T) {
	h := newHarness(t, WithRetention(time.Hour))
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	subID, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})
	h.publishFinding(tenant, "module:security", "guardrail", "t")
	waitFor(t, "delivered", func() bool {
		return len(h.deliveries(editor, tenant, "status=delivered")) == 1
	})

	h.clk.advance(2 * time.Hour)
	pruned, err := h.mod.PruneExpired(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 2 { // 1 event row + 1 delivered delivery row (never the cursor)
		t.Errorf("pruned = %d, want 2", pruned)
	}
	if items := h.do("GET", "/v1/m/eventing/events", editor, nil, tenantHdr(tenant)).body["items"].([]any); len(items) != 0 {
		t.Errorf("events after prune = %d, want 0", len(items))
	}
	rr := h.do("POST", "/v1/m/eventing/subscriptions/"+subID+"/replay", admin, map[string]any{"from_seq": 1}, tenantHdr(tenant))
	if rr.body["replayed"].(float64) != 0 {
		t.Errorf("replay after prune = %v, want 0", rr.body["replayed"])
	}

	// The monotonic cursor SURVIVES a full prune: the next captured event
	// continues the sequence (the eventing_cursor allocator, not max(seq) over
	// the now-empty log) — a consumer's saved cursor stays meaningful.
	h.publishFinding(tenant, "module:security", "guardrail", "after-prune")
	waitFor(t, "post-prune capture", func() bool {
		items := h.do("GET", "/v1/m/eventing/events", editor, nil, tenantHdr(tenant)).body["items"].([]any)
		return len(items) == 1
	})
	item := h.do("GET", "/v1/m/eventing/events", editor, nil, tenantHdr(tenant)).body["items"].([]any)[0].(map[string]any)
	if got := item["seq"].(float64); got != 2 {
		t.Errorf("post-prune seq = %v, want 2 (the cursor must never regress)", got)
	}
}

// An empty events page returns the caller's OWN cursor, never 0 (next_seq=0
// would reset a polling consumer to the start of the log).
func TestEventsListEmptyPageKeepsCursor(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	r := h.do("GET", "/v1/m/eventing/events?since_seq=42", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("events = %d %s", r.code, r.raw)
	}
	if got := r.body["next_seq"].(float64); got != 42 {
		t.Errorf("empty-page next_seq = %v, want 42 (the caller's own cursor)", got)
	}
}

// Tenant isolation on the management surface: another tenant's subscription is
// indistinguishable from absent.
func TestTenantIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")
	editorA := h.roleToken(admin, tenantA, "a@x.io", "editor")
	editorB := h.roleToken(admin, tenantB, "b@x.io", "editor")

	id, _ := h.createSubscription(editorA, tenantA, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": "https://consumer.example.com/hook",
	})
	if r := h.do("GET", "/v1/m/eventing/subscriptions/"+id, editorB, nil, tenantHdr(tenantB)); r.code != http.StatusNotFound {
		t.Errorf("cross-tenant get = %d, want 404", r.code)
	}
	if items := h.do("GET", "/v1/m/eventing/subscriptions", editorB, nil, tenantHdr(tenantB)).body["items"].([]any); len(items) != 0 {
		t.Errorf("cross-tenant list = %d items, want 0", len(items))
	}
	// Route-level RBAC: a viewer cannot author, an editor cannot delete.
	viewerA := h.roleToken(admin, tenantA, "v@x.io", "viewer")
	if r := h.do("POST", "/v1/m/eventing/subscriptions", viewerA, map[string]any{
		"name": "x", "event_types": []string{"finding.reported"}, "endpoint": "https://c.example.com/h",
	}, tenantHdr(tenantA)); r.code != http.StatusForbidden {
		t.Errorf("viewer create = %d, want 403", r.code)
	}
	if r := h.do("DELETE", "/v1/m/eventing/subscriptions/"+id, editorA, nil, tenantHdr(tenantA)); r.code != http.StatusForbidden {
		t.Errorf("editor delete = %d, want 403 (admin-tier)", r.code)
	}
}

// A crashed claim (delivering, stale) is rescued and redelivered — the
// at-least-once half after a node dies mid-flight.
func TestStaleClaimRescue(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})
	h.publishFinding(tenant, "module:security", "guardrail", "t")
	waitFor(t, "delivered", func() bool {
		return len(h.deliveries(editor, tenant, "status=delivered")) == 1
	})

	// Simulate the crash: force the row back to a stale "delivering" claim.
	var id model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 1})
		if err != nil {
			return err
		}
		rec := recs[0]
		id = model.ID(rec.String(model.ColID))
		rec[colDelStatus] = statusDelivering
		rec[colDelLastAt] = model.NewTimestamp(h.clk.Now().Time().Add(-time.Hour)).String()
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = id
	h.dispatch(tenant)
	waitFor(t, "stale claim rescue", func() bool { return rc.count() == 2 })
	if d := h.deliveries(editor, tenant, "")[0]; d["status"] != "delivered" {
		t.Errorf("rescued delivery = %v, want delivered", d["status"])
	}
}
