// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ---------------------------------------------------------------------------
// 1. buildCustomSchedule — unit tests
// ---------------------------------------------------------------------------

func TestS287_BuildCustomSchedule_MaxAttempts1(t *testing.T) {
	// maxAttempts=1 means "one attempt, zero retries" -> empty schedule.
	sched := buildCustomSchedule(1, 60)
	if len(sched) != 0 {
		t.Errorf("maxAttempts=1: got %d entries, want 0", len(sched))
	}
}

func TestS287_BuildCustomSchedule_MaxAttempts3_Interval60(t *testing.T) {
	sched := buildCustomSchedule(3, 60)
	if len(sched) != 2 {
		t.Fatalf("maxAttempts=3: got %d entries, want 2", len(sched))
	}
	want := []time.Duration{60 * time.Second, 120 * time.Second}
	for i, d := range want {
		if sched[i] != d {
			t.Errorf("sched[%d] = %v, want %v", i, sched[i], d)
		}
	}
}

func TestS287_BuildCustomSchedule_MaxAttempts5_Interval10_Cap(t *testing.T) {
	sched := buildCustomSchedule(5, 10)
	if len(sched) != 4 {
		t.Fatalf("maxAttempts=5: got %d entries, want 4", len(sched))
	}
	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second}
	for i, d := range want {
		if sched[i] != d {
			t.Errorf("sched[%d] = %v, want %v", i, sched[i], d)
		}
	}
	// All must be under the 8h cap.
	for i, d := range sched {
		if d > 8*time.Hour {
			t.Errorf("sched[%d] = %v exceeds 8h cap", i, d)
		}
	}
}

func TestS287_BuildCustomSchedule_ZeroDefersToDefault(t *testing.T) {
	// maxAttempts=0 means "defer to module default" -> nil/empty schedule.
	sched := buildCustomSchedule(0, 0)
	if len(sched) != 0 {
		t.Errorf("maxAttempts=0: got %d entries, want 0 (nil)", len(sched))
	}
}

func TestS287_BuildCustomSchedule_LargeAttemptsCapped(t *testing.T) {
	// With a large initial interval, the doubling should cap at 8h.
	sched := buildCustomSchedule(10, 3600) // 1h base, 9 retries
	cap := 8 * time.Hour
	for i, d := range sched {
		if d > cap {
			t.Errorf("sched[%d] = %v exceeds 8h cap", i, d)
		}
	}
	// After 3600s -> 7200s -> 14400s -> 28800s (=8h) -> all subsequent = 8h
	if sched[3] != cap {
		t.Errorf("sched[3] = %v, want %v (cap)", sched[3], cap)
	}
	if sched[4] != cap {
		t.Errorf("sched[4] = %v, want %v (cap)", sched[4], cap)
	}
}

// ---------------------------------------------------------------------------
// 2. Auth type validation — via the subscription create/update API
// ---------------------------------------------------------------------------

func TestS287_AuthTypeValidation_BearerCreate(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	r := h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "bearer-sub", "event_types": []string{"finding.reported"},
		"endpoint":   "https://consumer.example.com/hook",
		"auth_type":  "bearer",
		"auth_value": "my-bearer-token-123",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create bearer subscription = %d %s", r.code, r.raw)
	}
	if r.body["auth_type"] != "bearer" {
		t.Errorf("auth_type = %v, want bearer", r.body["auth_type"])
	}
	// The auth_value must NOT be returned in the response.
	if _, leaked := r.body["auth_value"]; leaked {
		t.Error("auth_value must not be returned in the create response")
	}
	// But the hint should be present.
	if hint, ok := r.body["auth_value_hint"].(string); !ok || hint == "" {
		t.Error("auth_value_hint must be present for a bearer subscription")
	}
}

func TestS287_AuthTypeValidation_HeaderCreate(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	r := h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "header-sub", "event_types": []string{"finding.reported"},
		"endpoint":         "https://consumer.example.com/hook",
		"auth_type":        "header",
		"auth_header_name": "X-Custom-Auth",
		"auth_value":       "secret-header-val",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create header subscription = %d %s", r.code, r.raw)
	}
	if r.body["auth_type"] != "header" {
		t.Errorf("auth_type = %v, want header", r.body["auth_type"])
	}
	if r.body["auth_header_name"] != "X-Custom-Auth" {
		t.Errorf("auth_header_name = %v, want X-Custom-Auth", r.body["auth_header_name"])
	}
}

func TestS287_AuthTypeValidation_HeaderMissingName(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	r := h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "header-no-name", "event_types": []string{"finding.reported"},
		"endpoint":   "https://consumer.example.com/hook",
		"auth_type":  "header",
		"auth_value": "secret-header-val",
		// auth_header_name is MISSING
	}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("header without auth_header_name = %d %s, want 400", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "auth_header_name") {
		t.Errorf("error should mention auth_header_name: %s", r.raw)
	}
}

func TestS287_AuthTypeValidation_BearerMissingValue(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	r := h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "bearer-no-val", "event_types": []string{"finding.reported"},
		"endpoint":  "https://consumer.example.com/hook",
		"auth_type": "bearer",
		// auth_value is MISSING
	}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("bearer without auth_value = %d %s, want 400", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "auth_value") {
		t.Errorf("error should mention auth_value: %s", r.raw)
	}
}

func TestS287_AuthTypeValidation_InvalidAuthType(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	r := h.do("POST", "/v1/m/eventing/subscriptions", editor, map[string]any{
		"name": "bad-type", "event_types": []string{"finding.reported"},
		"endpoint":   "https://consumer.example.com/hook",
		"auth_type":  "oauth2",
		"auth_value": "something",
	}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("invalid auth_type = %d %s, want 400", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "auth_type") {
		t.Errorf("error should mention auth_type: %s", r.raw)
	}
}

func TestS287_AuthTypeValidation_UpdateNoneToBearer(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	// Create with auth_type=none (default).
	id, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "start-none", "event_types": []string{"finding.reported"},
		"endpoint": "https://consumer.example.com/hook",
	})

	// Update to bearer with auth_value.
	r := h.do("PUT", "/v1/m/eventing/subscriptions/"+id, editor, map[string]any{
		"name": "start-none", "event_types": []string{"finding.reported"},
		"endpoint":   "https://consumer.example.com/hook",
		"auth_type":  "bearer",
		"auth_value": "new-token-456",
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("update none->bearer = %d %s", r.code, r.raw)
	}
	if r.body["auth_type"] != "bearer" {
		t.Errorf("updated auth_type = %v, want bearer", r.body["auth_type"])
	}
	if hint, ok := r.body["auth_value_hint"].(string); !ok || hint == "" {
		t.Error("auth_value_hint must be set after updating to bearer")
	}
}

// ---------------------------------------------------------------------------
// 3. Auth header dispatch — via the test harness
// ---------------------------------------------------------------------------

func TestS287_DispatchBearerHeader(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "bearer-dispatch", "event_types": []string{"finding.reported"},
		"endpoint":   rc.srv.URL,
		"auth_type":  "bearer",
		"auth_value": "tok-abc-789",
	})

	h.publishFinding(tenant, "module:security", "guardrail", "Bearer test event")
	waitFor(t, "bearer delivery", func() bool { return rc.count() >= 1 })

	req := rc.all()[0]
	authz := req.header.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer ") {
		t.Fatalf("Authorization header = %q, want Bearer prefix", authz)
	}
	if authz != "Bearer tok-abc-789" {
		t.Errorf("Authorization = %q, want 'Bearer tok-abc-789'", authz)
	}
	// HMAC signature must ALSO be present (always applied).
	if req.header.Get(headerSignature) == "" {
		t.Error("HMAC signature header must still be present alongside bearer auth")
	}
}

func TestS287_DispatchCustomHeader(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "custom-header", "event_types": []string{"finding.reported"},
		"endpoint":         rc.srv.URL,
		"auth_type":        "header",
		"auth_header_name": "X-Webhook-Token",
		"auth_value":       "custom-secret-xyz",
	})

	h.publishFinding(tenant, "module:security", "guardrail", "Custom header test")
	waitFor(t, "custom header delivery", func() bool { return rc.count() >= 1 })

	req := rc.all()[0]
	got := req.header.Get("X-Webhook-Token")
	if got != "custom-secret-xyz" {
		t.Errorf("X-Webhook-Token = %q, want 'custom-secret-xyz'", got)
	}
	// No Authorization header for auth_type=header.
	if authz := req.header.Get("Authorization"); authz != "" {
		t.Errorf("Authorization header should be absent for auth_type=header, got %q", authz)
	}
}

func TestS287_DispatchNoAuthHeader(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "no-auth", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
		// auth_type defaults to "none"
	})

	h.publishFinding(tenant, "module:security", "guardrail", "No auth test")
	waitFor(t, "no-auth delivery", func() bool { return rc.count() >= 1 })

	req := rc.all()[0]
	if authz := req.header.Get("Authorization"); authz != "" {
		t.Errorf("auth_type=none must not set Authorization header, got %q", authz)
	}
	// HMAC signature is always present.
	if req.header.Get(headerSignature) == "" {
		t.Error("HMAC signature must always be present even with auth_type=none")
	}
}

// ---------------------------------------------------------------------------
// 4. Per-subscription retry policy — via the test harness
// ---------------------------------------------------------------------------

func TestS287_PerSubscriptionRetry_MaxAttempts(t *testing.T) {
	// Default harness retry schedule = [5ms, 5ms] → 3 total attempts.
	// Per-subscription max_attempts=2, initial_interval_seconds=5 → 2 total
	// attempts with a 5s custom backoff, then dead.
	h := newHarness(t)
	rc := newReceiver(t)
	rc.setStatus(http.StatusInternalServerError)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "retry-2", "event_types": []string{"finding.reported"},
		"endpoint":                 rc.srv.URL,
		"max_attempts":             2,
		"initial_interval_seconds": 5,
	})

	h.publishFinding(tenant, "module:security", "guardrail", "retry test")
	// Wait for the initial attempt outcome to commit.
	h.waitAttempts(editor, tenant, 1)

	// One retry pass: advance past the 5s interval (±20% jitter → max 6s)
	// to exhaust the ladder (max_attempts=2 → 1 retry, then dead).
	h.clk.advance(10 * time.Second)
	h.dispatch(tenant)

	waitFor(t, "dead-letter after 2 attempts", func() bool {
		ds := h.deliveries(editor, tenant, "status=dead")
		return len(ds) == 1
	})
	dead := h.deliveries(editor, tenant, "status=dead")[0]
	if dead["attempts"].(float64) != 2 {
		t.Errorf("attempts = %v, want 2 (not the default 3)", dead["attempts"])
	}
	if got := rc.count(); got != 2 {
		t.Errorf("receiver saw %d attempts, want 2", got)
	}
}

func TestS287_PerSubscriptionRetry_InitialInterval(t *testing.T) {
	// Per-subscription initial_interval_seconds=120 → next retry at ~120s
	// instead of the harness's 5ms default.
	h := newHarness(t)
	rc := newReceiver(t)
	rc.setStatus(http.StatusInternalServerError)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "interval-120", "event_types": []string{"finding.reported"},
		"endpoint":                 rc.srv.URL,
		"initial_interval_seconds": 120,
	})

	h.publishFinding(tenant, "module:security", "guardrail", "interval test")
	h.waitAttempts(editor, tenant, 1)

	// At 30s: not due yet (120s interval with ±20% jitter → min 96s).
	h.clk.advance(30 * time.Second)
	h.dispatch(tenant)
	if rc.count() != 1 {
		t.Errorf("30s: receiver saw %d, want 1 (not due yet)", rc.count())
	}

	// Verify the next_attempt_at is roughly 120s from now (±20% jitter = 96-144s).
	ds := h.deliveries(editor, tenant, "")
	if len(ds) != 1 {
		t.Fatalf("expected 1 delivery row, got %d", len(ds))
	}
	// The row should still be queued (not yet due).
	if ds[0]["status"] != "queued" {
		t.Errorf("status = %v after 30s, want queued", ds[0]["status"])
	}

	// At 150s (total): definitely due now (96s min with jitter).
	h.clk.advance(120 * time.Second)
	h.dispatch(tenant)
	waitFor(t, "second attempt after interval", func() bool {
		ds := h.deliveries(editor, tenant, "")
		return len(ds) == 1 && ds[0]["attempts"].(float64) >= 2
	})
	if rc.count() != 2 {
		t.Errorf("after 150s: receiver saw %d, want 2", rc.count())
	}
}

// ---------------------------------------------------------------------------
// 5. Rotate auth — POST /subscriptions/{id}/rotate-auth
// ---------------------------------------------------------------------------

func TestS287_RotateAuth_BearerSubscription(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	id, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "rotate-bearer", "event_types": []string{"finding.reported"},
		"endpoint":   "https://consumer.example.com/hook",
		"auth_type":  "bearer",
		"auth_value": "original-token",
	})

	// Get the original hint.
	r := h.do("GET", "/v1/m/eventing/subscriptions/"+id, editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("get = %d %s", r.code, r.raw)
	}
	origHint := r.body["auth_value_hint"].(string)

	// Rotate the auth value.
	r = h.do("POST", "/v1/m/eventing/subscriptions/"+id+"/rotate-auth", editor,
		map[string]any{"auth_value": "rotated-token-new"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("rotate-auth = %d %s", r.code, r.raw)
	}
	newHint := r.body["auth_value_hint"].(string)
	if newHint == origHint {
		t.Error("rotate-auth must produce a new hint")
	}
	if newHint == "" {
		t.Error("rotate-auth must return a non-empty auth_value_hint")
	}

	// Verify the sealed value changed in the store.
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(id))
		if err != nil {
			return err
		}
		sealed := rec.String(colSubAuthValSealed)
		if sealed == "" {
			t.Error("rotated sealed auth value must not be empty")
		}
		if strings.Contains(sealed, "rotated-token-new") {
			t.Error("sealed auth value must not contain the cleartext")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Verify the rotated credential is actually used in dispatch.
	rc := newReceiver(t)
	// Update the endpoint to point at our receiver.
	r = h.do("PUT", "/v1/m/eventing/subscriptions/"+id, editor, map[string]any{
		"name": "rotate-bearer", "event_types": []string{"finding.reported"},
		"endpoint":   rc.srv.URL,
		"auth_type":  "bearer",
		"auth_value": "rotated-token-new",
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("update endpoint = %d %s", r.code, r.raw)
	}
	h.publishFinding(tenant, "module:security", "guardrail", "post-rotate event")
	waitFor(t, "post-rotate delivery", func() bool { return rc.count() >= 1 })
	req := rc.all()[0]
	if authz := req.header.Get("Authorization"); authz != "Bearer rotated-token-new" {
		t.Errorf("post-rotate Authorization = %q, want 'Bearer rotated-token-new'", authz)
	}
}

func TestS287_RotateAuth_NoneSubscription_400(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	// Create with auth_type=none (default).
	id, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "rotate-none", "event_types": []string{"finding.reported"},
		"endpoint": "https://consumer.example.com/hook",
	})

	r := h.do("POST", "/v1/m/eventing/subscriptions/"+id+"/rotate-auth", editor,
		map[string]any{"auth_value": "some-token"}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("rotate-auth on none subscription = %d %s, want 400", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "no auth credential") {
		t.Errorf("error should mention no auth credential: %s", r.raw)
	}
}

// ---------------------------------------------------------------------------
// 6. Dead-letter list — GET /dead-letters
// ---------------------------------------------------------------------------

func TestS287_DeadLetterList_FiltersStatusDead(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	// Create a subscription, send one event that will be delivered successfully.
	subID, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "dl-test", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})

	h.publishFinding(tenant, "module:security", "guardrail", "will-deliver")
	waitFor(t, "first delivery", func() bool {
		return len(h.deliveries(editor, tenant, "status=delivered")) == 1
	})

	// Now make the receiver fail and send another event -> will exhaust retries.
	rc.setStatus(http.StatusInternalServerError)
	h.publishFinding(tenant, "module:security", "guardrail", "will-die")
	waitFor(t, "second event queued", func() bool {
		ds := h.deliveries(editor, tenant, "")
		return len(ds) == 2
	})
	// Wait for the first attempt's OUTCOME TO COMMIT — not for the attempt to be in flight.
	// A "delivering" row holds a lease a worker took and has not yet resolved, and scanDue
	// takes only queued rows that are due plus leases that have gone STALE, never a fresh one.
	// So a dispatch pass issued while the row is still "delivering" is a no-op: at least one
	// of the two clock advances below is spent for nothing and the ladder can end short of
	// dead. That is how this test used to fail under CPU contention, always at the dead-letter
	// wait, and no deadline would have saved it — once a rung is lost the row's next_at sits
	// ahead of an injected clock nobody advances again. waitAttempts encodes the same rule for
	// the single-row tests; this tenant holds two rows, so the predicate is named separately
	// and pinned by TestFirstAttemptCommittedRejectsALeaseInFlight.
	waitFor(t, "second event first attempt to commit", func() bool {
		return firstAttemptCommitted(h.deliveries(editor, tenant, ""))
	})

	// Exhaust the retry ladder (harness schedule = [5ms, 5ms]).
	for i := 0; i < 2; i++ {
		h.clk.advance(time.Second)
		h.dispatch(tenant)
	}
	waitFor(t, "dead-letter", func() bool {
		return len(h.deliveries(editor, tenant, "status=dead")) == 1
	})

	// GET /dead-letters should return ONLY the dead row.
	r := h.do("GET", "/v1/m/eventing/dead-letters", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("dead-letters = %d %s", r.code, r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("dead-letters items = %d, want 1", len(items))
	}
	dl := items[0].(map[string]any)
	if dl["status"] != "dead" {
		t.Errorf("dead-letter status = %v, want dead", dl["status"])
	}

	// Verify ?subscription filter works.
	r = h.do("GET", "/v1/m/eventing/dead-letters?subscription="+subID, editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("dead-letters?subscription = %d %s", r.code, r.raw)
	}
	items = r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("dead-letters?subscription items = %d, want 1", len(items))
	}

	// A non-matching subscription filter returns empty.
	r = h.do("GET", "/v1/m/eventing/dead-letters?subscription=00000000-0000-0000-0000-000000000000", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("dead-letters?subscription(none) = %d %s", r.code, r.raw)
	}
	items = r.body["items"].([]any)
	if len(items) != 0 {
		t.Errorf("dead-letters?subscription(none) items = %d, want 0", len(items))
	}
}

func TestS287_DeadLetterList_EmptyWhenNoDeadLetters(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "ok-sub", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})

	// Successfully deliver an event.
	h.publishFinding(tenant, "module:security", "guardrail", "ok-event")
	waitFor(t, "delivered", func() bool {
		return len(h.deliveries(editor, tenant, "status=delivered")) == 1
	})

	// Dead-letters should be empty.
	r := h.do("GET", "/v1/m/eventing/dead-letters", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("dead-letters = %d %s", r.code, r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) != 0 {
		t.Errorf("dead-letters items = %d, want 0 (no dead letters)", len(items))
	}
}

// ---------------------------------------------------------------------------
// Integration: auth credential sealed at rest, never in cleartext.
// ---------------------------------------------------------------------------

func TestS287_AuthValueSealedAtRest(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	cleartext := "super-secret-bearer-token"
	id, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "sealed-test", "event_types": []string{"finding.reported"},
		"endpoint":   "https://consumer.example.com/hook",
		"auth_type":  "bearer",
		"auth_value": cleartext,
	})

	// Verify the cleartext does NOT appear in the store row.
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(id))
		if err != nil {
			return err
		}
		sealed := rec.String(colSubAuthValSealed)
		if sealed == "" {
			t.Error("auth_value_sealed must not be empty for a bearer subscription")
		}
		if strings.Contains(sealed, cleartext) {
			t.Error("the stored sealed auth value must NOT contain the cleartext")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Verify GET never returns auth_value.
	r := h.do("GET", "/v1/m/eventing/subscriptions/"+id, editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("get = %d %s", r.code, r.raw)
	}
	if _, leaked := r.body["auth_value"]; leaked {
		t.Error("GET must never return auth_value")
	}
	if _, leaked := r.body["auth_value_sealed"]; leaked {
		t.Error("GET must never return auth_value_sealed")
	}
}
