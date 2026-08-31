// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// s496_routine_policy_test.go pins the routine-governance controls to real
// enforcement. Before wired the gate the five controls were persisted
// intent with no consumer, so 19 of the original 21 tests here failed with a
// 201/200 that should have been a refusal (the literal run is kept at
// an internal design note (not shipped)). The other two are
// POSITIVE controls — a compliant declaration must still land — and passed
// before and after; they exist so a control that denies everything cannot be
// mistaken for a control that works.

// fakeRoutineGate is a programmable RoutinePolicyGate.
type fakeRoutineGate struct {
	pol RoutinePolicy
	err error
	// seen records the scope the module asked about, so a test can prove the
	// policy is resolved from the ROUTINE's frozen owner scope and not from
	// whoever is calling.
	seen []RoutineScope
}

func (g *fakeRoutineGate) Resolve(_ context.Context, _ model.TenantID, s RoutineScope) (RoutinePolicy, error) {
	g.seen = append(g.seen, s)
	return g.pol, g.err
}

// fakeTargetEnv is a programmable TargetEnvironmentResolver.
type fakeTargetEnv struct {
	tc  TargetEnvironment
	err error
}

func (r fakeTargetEnv) Resolve(context.Context, string, string) (TargetEnvironment, error) {
	return r.tc, r.err
}

// routineHarness spins the module with a routine gate (and optionally a target
// environment resolver) and returns an authenticated admin + tenant.
type routineHarness struct {
	h      *harness
	mod    *Module
	tok    string
	tenant model.TenantID
	hdr    map[string]string
}

func newRoutineHarness(t *testing.T, opts ...Option) *routineHarness {
	t.Helper()
	h, mod := newHarness(t, opts...)
	tok := h.adminLogin()
	tenant := h.createOrg(tok, "acme")
	return &routineHarness{h: h, mod: mod, tok: tok, tenant: tenant, hdr: tenantHdr(tenant)}
}

func (r *routineHarness) create(body map[string]any) resp {
	return r.h.do("POST", "/v1/m/orchestration/schedules", r.tok, body, r.hdr)
}

// cronSchedule is a valid, policy-free schedule declaration.
func cronSchedule(name, spec string, interval int64) map[string]any {
	return map[string]any{
		"name": name, "subject_kind": "agent", "subject_ref": "agent-1",
		"trigger_kind": "cron", "cadence_spec": spec, "expected_interval_seconds": interval,
	}
}

// --- max_cadence_seconds (the minimum-interval floor) ------------------------

func TestS496CadenceFloorDeniesCreateBelowFloor(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, MinIntervalSec: 3600, PolicyRefs: []string{"p1"}}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	r := rh.create(cronSchedule("too-fast", "*/5 * * * *", 300))
	if r.code != http.StatusUnprocessableEntity {
		t.Fatalf("create below the cadence floor = %d %s, want 422", r.code, r.raw)
	}
	if r.body["code"] != codeRoutineFloor {
		t.Fatalf("denial code = %v, want %s", r.body["code"], codeRoutineFloor)
	}
	// A compliant declaration still lands.
	if r := rh.create(cronSchedule("ok", "0 * * * *", 3600)); r.code != http.StatusCreated {
		t.Fatalf("compliant create = %d %s, want 201", r.code, r.raw)
	}
}

// An undeclared interval (0 disables the cadence-miss check) must NOT be a way
// to sidestep a floor.
func TestS496CadenceFloorDeniesUndeclaredInterval(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, MinIntervalSec: 3600}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	r := rh.create(cronSchedule("no-interval", "0 * * * *", 0))
	if r.code != http.StatusUnprocessableEntity || r.body["code"] != codeRoutineFloor {
		t.Fatalf("create with interval 0 under a floor = %d %s, want 422 %s", r.code, r.raw, codeRoutineFloor)
	}
}

// The declared interval and the cron text are independent fields, so they can
// disagree. A truthful-looking interval with a one-minute cron is the classic
// evasion and must be caught by parsing the cron.
func TestS496CadenceFloorCatchesLyingCronText(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, MinIntervalSec: 3600}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	r := rh.create(cronSchedule("liar", "* * * * *", 3600))
	if r.code != http.StatusUnprocessableEntity || r.body["code"] != codeRoutineFloor {
		t.Fatalf("interval=3600 with a per-minute cron = %d %s, want 422 %s", r.code, r.raw, codeRoutineFloor)
	}
}

// An unparseable cadence under a floor must DENY: "parse it if you can,
// otherwise allow" makes any unsupported spelling the bypass.
func TestS496CadenceFloorDeniesUnparseableCron(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, MinIntervalSec: 3600}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	r := rh.create(cronSchedule("weird", "@hourly", 3600))
	if r.code != http.StatusUnprocessableEntity || r.body["code"] != codeRoutineCron {
		t.Fatalf("unparseable cadence under a floor = %d %s, want 422 %s", r.code, r.raw, codeRoutineCron)
	}
}

// --- allowed_cron_patterns ---------------------------------------------------

func TestS496CronAllowlistDeniesUnlistedPattern(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{
		InForce: true, CronAllowlistInForce: true, CronAllowed: []string{"0 * * * *", "0 2 * * *"},
	}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	if r := rh.create(cronSchedule("unlisted", "*/5 * * * *", 300)); r.code != http.StatusUnprocessableEntity ||
		r.body["code"] != codeRoutineCron {
		t.Fatalf("unlisted cron = %d %s, want 422 %s", r.code, r.raw, codeRoutineCron)
	}
	// An allow-listed pattern lands, and re-spacing it is NOT a bypass in
	// either direction (canonical comparison).
	if r := rh.create(cronSchedule("listed", "0    *  * * *", 3600)); r.code != http.StatusCreated {
		t.Fatalf("allow-listed cron (re-spaced) = %d %s, want 201", r.code, r.raw)
	}
}

// An authored EMPTY allowlist is a deny-all the operator wrote; it must not
// collapse into "unconstrained".
func TestS496EmptyCronAllowlistDeniesEveryCron(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, CronAllowlistInForce: true, CronAllowed: nil}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	if r := rh.create(cronSchedule("any", "0 * * * *", 3600)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("empty allowlist admitted a cron = %d %s, want 422", r.code, r.raw)
	}
}

// trigger_kind must not be the bypass: with a cron allowlist in force, a
// manual/event routine has no enumerated cadence at all.
func TestS496CronAllowlistDeniesNonCronTrigger(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, CronAllowlistInForce: true, CronAllowed: []string{"0 * * * *"}}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	r := rh.create(map[string]any{
		"name": "manual-escape", "subject_kind": "agent", "subject_ref": "agent-1",
		"trigger_kind": "manual",
	})
	if r.code != http.StatusUnprocessableEntity || r.body["code"] != codeRoutineCron {
		t.Fatalf("manual trigger under a cron allowlist = %d %s, want 422 %s", r.code, r.raw, codeRoutineCron)
	}
}

// --- blocked_environments ----------------------------------------------------

func TestS496BlockedEnvironmentDeniesCreate(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, BlockedEnvs: []string{"prod"}}}
	env := fakeTargetEnv{tc: TargetEnvironment{RouteFound: true, Environment: "prod"}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate), WithTargetEnvironmentResolver(env))

	r := rh.create(cronSchedule("in-prod", "0 * * * *", 3600))
	if r.code != http.StatusUnprocessableEntity || r.body["code"] != codeRoutineEnv {
		t.Fatalf("create into a blocked environment = %d %s, want 422 %s", r.code, r.raw, codeRoutineEnv)
	}
}

func TestS496BlockedEnvironmentAdmitsUnblocked(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, BlockedEnvs: []string{"prod"}}}
	env := fakeTargetEnv{tc: TargetEnvironment{RouteFound: true, Environment: "staging"}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate), WithTargetEnvironmentResolver(env))

	if r := rh.create(cronSchedule("in-staging", "0 * * * *", 3600)); r.code != http.StatusCreated {
		t.Fatalf("create into an unblocked environment = %d %s, want 201", r.code, r.raw)
	}
}

// A subject with NO dispatcher route may be DECLARED (it cannot actuate at all,
// so refusing the declaration buys nothing and would make the control unusable
// on a deployment with no dispatcher provisioned) — but it must not FIRE.
func TestS496UnroutedSubjectDeclarableButNotFireable(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, BlockedEnvs: []string{"prod"}}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate),
		WithApprovalGate(fakeGate{status: StatusApproved})) // no target resolver wired

	created := rh.create(cronSchedule("unrouted", "0 * * * *", 3600))
	if created.code != http.StatusCreated {
		t.Fatalf("declaring a routine for an unrouted subject = %d %s, want 201", created.code, created.raw)
	}
	id := created.body["id"].(string)

	r := rh.h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", rh.tok,
		map[string]any{"approval_ref": "appr-1"}, rh.hdr)
	if r.code != http.StatusForbidden || r.body["code"] != codeRoutineEnv {
		t.Fatalf("firing an unrouted subject under a blocked-env policy = %d %s, want 403 %s", r.code, r.raw, codeRoutineEnv)
	}
}

// A route that EXISTS but carries no environment dimension is a different arm
// from "no route at all": an absent environment is not an implicitly safe one,
// so it is refused even at declaration.
func TestS496RoutedButEnvironmentlessSubjectDeniedAtDeclaration(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, BlockedEnvs: []string{"prod"}}}
	env := fakeTargetEnv{tc: TargetEnvironment{RouteFound: true, Environment: ""}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate), WithTargetEnvironmentResolver(env))

	r := rh.create(cronSchedule("no-env-dimension", "0 * * * *", 3600))
	if r.code != http.StatusUnprocessableEntity || r.body["code"] != codeRoutineEnv {
		t.Fatalf("routed subject with no environment = %d %s, want 422 %s", r.code, r.raw, codeRoutineEnv)
	}
}

// With no blocked list authored, the resolver is never consulted — an existing
// deployment is completely unaffected.
func TestS496NoBlockedListLeavesUnroutedSubjectsAlone(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, MinIntervalSec: 60}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	if r := rh.create(cronSchedule("no-env-policy", "0 * * * *", 3600)); r.code != http.StatusCreated {
		t.Fatalf("create with no blocked-env policy = %d %s, want 201", r.code, r.raw)
	}
}

// --- max_active_routines -----------------------------------------------------

func TestS496MaxActiveCapDeniesOverCapCreate(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{
		InForce:    true,
		ActiveCaps: []RoutineActiveCap{{ScopeKind: "tenant", Max: 1}},
	}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	if r := rh.create(cronSchedule("first", "0 * * * *", 3600)); r.code != http.StatusCreated {
		t.Fatalf("first create = %d %s, want 201", r.code, r.raw)
	}
	r := rh.create(cronSchedule("second", "0 * * * *", 3600))
	if r.code != http.StatusUnprocessableEntity || r.body["code"] != codeRoutineActiveCap {
		t.Fatalf("create over the active cap = %d %s, want 422 %s", r.code, r.raw, codeRoutineActiveCap)
	}
}

// Pausing releases capacity; re-activating consumes it again.
func TestS496MaxActiveCapReleasedByPause(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{
		InForce:    true,
		ActiveCaps: []RoutineActiveCap{{ScopeKind: "tenant", Max: 1}},
	}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	first := rh.create(cronSchedule("first", "0 * * * *", 3600))
	if first.code != http.StatusCreated {
		t.Fatalf("first create = %d %s", first.code, first.raw)
	}
	id := first.body["id"].(string)
	if r := rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+id, rh.tok,
		map[string]any{"desired_status": "paused"}, rh.hdr); r.code != http.StatusOK {
		t.Fatalf("pause = %d %s", r.code, r.raw)
	}
	if r := rh.create(cronSchedule("second", "0 * * * *", 3600)); r.code != http.StatusCreated {
		t.Fatalf("create after freeing capacity = %d %s, want 201", r.code, r.raw)
	}
	// Re-activating the first would now exceed the cap.
	r := rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+id, rh.tok,
		map[string]any{"desired_status": "active"}, rh.hdr)
	if r.code != http.StatusUnprocessableEntity || r.body["code"] != codeRoutineActiveCap {
		t.Fatalf("re-activate over the cap = %d %s, want 422 %s", r.code, r.raw, codeRoutineActiveCap)
	}
}

// --- require_approval --------------------------------------------------------

func TestS496RequireApprovalGatesCreate(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, RequireApproval: true}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate), WithApprovalGate(fakeGate{status: StatusApproved}))

	// Phase 1: no approval_ref ⇒ the declaration is NOT persisted.
	r := rh.create(cronSchedule("needs-approval", "0 * * * *", 3600))
	if r.code != http.StatusAccepted {
		t.Fatalf("phase-1 create under require_approval = %d %s, want 202", r.code, r.raw)
	}
	ref, _ := r.body["approval_ref"].(string)
	if ref == "" {
		t.Fatalf("phase-1 response carried no approval_ref: %s", r.raw)
	}
	if l := rh.h.do("GET", "/v1/m/orchestration/schedules", rh.tok, nil, rh.hdr); l.code == http.StatusOK {
		if items, ok := l.body["items"].([]any); ok && len(items) != 0 {
			t.Fatalf("phase 1 persisted a schedule; want none until approved: %s", l.raw)
		}
	}

	// Phase 2: with the approved reference the declaration lands.
	body := cronSchedule("needs-approval", "0 * * * *", 3600)
	body["approval_ref"] = ref
	if r := rh.create(body); r.code != http.StatusCreated {
		t.Fatalf("phase-2 create = %d %s, want 201", r.code, r.raw)
	}
}

// A rejected/pending decision must NOT create the routine.
func TestS496RequireApprovalDeniesUnapproved(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, RequireApproval: true}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate), WithApprovalGate(fakeGate{status: StatusRejected}))

	body := cronSchedule("rejected", "0 * * * *", 3600)
	body["approval_ref"] = "appr-1"
	r := rh.create(body)
	if r.code != http.StatusForbidden {
		t.Fatalf("phase-2 create with a rejected approval = %d %s, want 403", r.code, r.raw)
	}
}

// Activating a previously-paused routine is a way INTO the active population,
// so it consumes an approval too — otherwise "create paused, then activate" is
// the loophole.
func TestS496RequireApprovalGatesReactivation(t *testing.T) {
	open := &fakeRoutineGate{pol: RoutinePolicy{}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(open), WithApprovalGate(fakeGate{status: StatusApproved}))
	first := rh.create(cronSchedule("later-governed", "0 * * * *", 3600))
	if first.code != http.StatusCreated {
		t.Fatalf("create = %d %s", first.code, first.raw)
	}
	id := first.body["id"].(string)
	if r := rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+id, rh.tok,
		map[string]any{"desired_status": "paused"}, rh.hdr); r.code != http.StatusOK {
		t.Fatalf("pause = %d %s", r.code, r.raw)
	}
	// The operator now authors require_approval.
	open.pol = RoutinePolicy{InForce: true, RequireApproval: true}

	r := rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+id, rh.tok,
		map[string]any{"desired_status": "active"}, rh.hdr)
	if r.code != http.StatusAccepted {
		t.Fatalf("re-activation under require_approval = %d %s, want 202", r.code, r.raw)
	}
}

// --- fire-time enforcement ---------------------------------------------------

// The floor binds what a routine's cadence REALLY is: how often something
// drives it through a fire path. It also closes grandfathering — this schedule
// was declared before the policy existed.
func TestS496CadenceFloorDeniesTooSoonFire(t *testing.T) {
	open := &fakeRoutineGate{pol: RoutinePolicy{}}
	clock := newManualClock()
	rh := newRoutineHarness(t, WithRoutinePolicyGate(open), WithClock(clock),
		WithApprovalGate(fakeGate{status: StatusApproved}))

	first := rh.create(cronSchedule("grandfathered", "* * * * *", 60))
	if first.code != http.StatusCreated {
		t.Fatalf("create = %d %s", first.code, first.raw)
	}
	id := first.body["id"].(string)

	// One fire lands (no policy yet).
	if r := rh.h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", rh.tok,
		map[string]any{"approval_ref": "appr-1"}, rh.hdr); r.code != http.StatusOK {
		t.Fatalf("first fire = %d %s, want 200", r.code, r.raw)
	}

	// The operator authors an hourly floor. The next fire, one minute later, is
	// too soon.
	open.pol = RoutinePolicy{InForce: true, MinIntervalSec: 3600, PolicyRefs: []string{"p1"}}
	clock.advance(time.Minute)
	r := rh.h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", rh.tok,
		map[string]any{"approval_ref": "appr-2"}, rh.hdr)
	if r.code != http.StatusTooManyRequests || r.body["code"] != codeRoutineFloor {
		t.Fatalf("fire inside the floor = %d %s, want 429 %s", r.code, r.raw, codeRoutineFloor)
	}

	// Past the floor it fires again.
	clock.advance(time.Hour)
	if r := rh.h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", rh.tok,
		map[string]any{"approval_ref": "appr-3"}, rh.hdr); r.code != http.StatusOK {
		t.Fatalf("fire past the floor = %d %s, want 200", r.code, r.raw)
	}
}

func TestS496BlockedEnvironmentDeniesFire(t *testing.T) {
	open := &fakeRoutineGate{pol: RoutinePolicy{}}
	envr := fakeTargetEnv{tc: TargetEnvironment{RouteFound: true, Environment: "prod"}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(open), WithTargetEnvironmentResolver(envr),
		WithApprovalGate(fakeGate{status: StatusApproved}))

	first := rh.create(cronSchedule("prod-routine", "0 * * * *", 3600))
	if first.code != http.StatusCreated {
		t.Fatalf("create = %d %s", first.code, first.raw)
	}
	id := first.body["id"].(string)

	open.pol = RoutinePolicy{InForce: true, BlockedEnvs: []string{"prod"}}
	r := rh.h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", rh.tok,
		map[string]any{"approval_ref": "appr-1"}, rh.hdr)
	if r.code != http.StatusForbidden || r.body["code"] != codeRoutineEnv {
		t.Fatalf("fire into a blocked environment = %d %s, want 403 %s", r.code, r.raw, codeRoutineEnv)
	}
}

// --- fail-closed -------------------------------------------------------------

func TestS496GateErrorFailsClosed(t *testing.T) {
	gate := &fakeRoutineGate{err: context.DeadlineExceeded}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	r := rh.create(cronSchedule("unreadable", "0 * * * *", 3600))
	if r.code != http.StatusServiceUnavailable || r.body["code"] != codeRoutineUnreadable {
		t.Fatalf("create with an unreadable policy = %d %s, want 503 %s", r.code, r.raw, codeRoutineUnreadable)
	}
}

// An enabled policy on an axis the routine cannot answer for is INDETERMINATE,
// never "no policy matched" — that would be the silent bypass.
func TestS496IndeterminateScopeFailsClosed(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{Indeterminate: true, IndeterminateAxis: "workspace"}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	r := rh.create(cronSchedule("indeterminate", "0 * * * *", 3600))
	if r.code != http.StatusServiceUnavailable || r.body["code"] != codeRoutineUnreadable {
		t.Fatalf("create under an indeterminate policy = %d %s, want 503 %s", r.code, r.raw, codeRoutineUnreadable)
	}
}

// --- scope provenance --------------------------------------------------------

// The policy must be resolved from the ROUTINE's frozen owner scope, so an
// admin acting on someone else's routine cannot escape the owner's policy.
func TestS496PolicyResolvedFromFrozenOwnerScope(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate), WithApprovalGate(fakeGate{status: StatusApproved}))

	first := rh.create(cronSchedule("owned", "0 * * * *", 3600))
	if first.code != http.StatusCreated {
		t.Fatalf("create = %d %s", first.code, first.raw)
	}
	id := first.body["id"].(string)
	ownerAtCreate := gate.seen[len(gate.seen)-1]
	if ownerAtCreate.UserRef == "" {
		t.Fatalf("create did not resolve an owner user ref: %+v", ownerAtCreate)
	}

	// A DIFFERENT principal patches it; the scope asked about must still be the
	// declaring owner's.
	other := rh.h.roleToken(rh.tok, rh.tenant, "other@x.io", "admin")
	before := len(gate.seen)
	if r := rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+id, other,
		map[string]any{"expected_interval_seconds": 7200}, rh.hdr); r.code != http.StatusOK {
		t.Fatalf("patch by another principal = %d %s", r.code, r.raw)
	}
	if len(gate.seen) <= before {
		t.Fatal("patch did not consult the routine-policy gate at all")
	}
	if got := gate.seen[len(gate.seen)-1]; got.UserRef != ownerAtCreate.UserRef {
		t.Fatalf("patch resolved policy for %q, want the declaring owner %q", got.UserRef, ownerAtCreate.UserRef)
	}
}

// --- the two seams the first test pass left uncovered ---------------------

// The DAG schedule-fire step is a SECOND actuation path. A control wired only
// into handleFire is bypassed by embedding the same schedule in a workflow —
// the seam class taught this repo to look for.
func TestS496BlockedEnvironmentDeniesWorkflowScheduleFire(t *testing.T) {
	g := newRoutedGate()
	fired := false
	open := &fakeRoutineGate{pol: RoutinePolicy{}}
	h, _ := newHarness(t,
		WithApprovalGate(g),
		WithDispatcher(recordingDispatcher{fired: &fired}),
		WithRoutinePolicyGate(open),
		WithTargetEnvironmentResolver(fakeTargetEnv{tc: TargetEnvironment{RouteFound: true, Environment: "prod"}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	schedID := h.createAgentSchedule(admin, tenant, "nightly", "agent-1")
	wf := h.createWorkflow(admin, tenant, "via-dag", []map[string]any{
		step("fire", "schedule-fire", map[string]any{"schedule_id": schedID}),
	})

	// The operator blocks prod AFTER the workflow was declared.
	open.pol = RoutinePolicy{InForce: true, BlockedEnvs: []string{"prod"}, PolicyRefs: []string{"p1"}}

	r2 := h.runToPhase2(g, admin, tenant, wf["id"].(string))
	run := r2.body["run"].(map[string]any)
	if st := runStepStatuses(run); st["fire"] != stepStatusBlocked {
		t.Fatalf("workflow schedule-fire step = %v, want blocked by routine policy (run=%v)", st, run["status"])
	}
	if fired {
		t.Fatal("the dispatcher was reached THROUGH THE DAG despite the routine-policy denial")
	}
}

// The cadence floor must bite on the DAG path too, not just the direct fire.
func TestS496CadenceFloorDeniesWorkflowScheduleFire(t *testing.T) {
	g := newRoutedGate()
	fired := false
	clock := newManualClock()
	open := &fakeRoutineGate{pol: RoutinePolicy{}}
	h, _ := newHarness(t,
		WithClock(clock), WithApprovalGate(g),
		WithDispatcher(recordingDispatcher{fired: &fired}),
		WithRoutinePolicyGate(open),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	schedID := h.createAgentSchedule(admin, tenant, "nightly", "agent-1")
	wf := h.createWorkflow(admin, tenant, "dag-floor", []map[string]any{
		step("fire", "schedule-fire", map[string]any{"schedule_id": schedID}),
	})
	wfID := wf["id"].(string)

	// Run once with no policy: the step fires and stamps last_fired_at.
	r1 := h.runToPhase2(g, admin, tenant, wfID)
	if st := runStepStatuses(r1.body["run"].(map[string]any)); st["fire"] != stepStatusDispatched {
		t.Fatalf("seed run step = %v, want dispatched", st)
	}
	if !fired {
		t.Fatal("seed run never reached the dispatcher")
	}
	fired = false

	// The operator authors an hourly floor; one minute later the DAG tries again.
	open.pol = RoutinePolicy{InForce: true, MinIntervalSec: 3600, PolicyRefs: []string{"p1"}}
	clock.advance(time.Minute)

	r2 := h.runToPhase2(g, admin, tenant, wfID)
	if st := runStepStatuses(r2.body["run"].(map[string]any)); st["fire"] != stepStatusBlocked {
		t.Fatalf("workflow step inside the floor = %v, want blocked", st)
	}
	if fired {
		t.Fatal("the dispatcher was reached through the DAG inside the cadence floor")
	}
}

// Restore re-applies desired_status, cadence_spec and the interval from an old
// revision, so it is a THIRD declaration path: gating only create and patch
// would leave "restore an old revision" as the way around every control.
func TestS496RestoreCannotReintroduceAViolatingShape(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	first := rh.create(cronSchedule("restorable", "0 * * * *", 3600))
	if first.code != http.StatusCreated {
		t.Fatalf("create = %d %s", first.code, first.raw)
	}
	id := first.body["id"].(string)

	// Move it to a slower cadence; the hourly shape survives as revision 1.
	if r := rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+id, rh.tok, map[string]any{
		"expected_interval_seconds": 7200, "cadence_spec": "0 */2 * * *",
	}, rh.hdr); r.code != http.StatusOK {
		t.Fatalf("patch = %d %s", r.code, r.raw)
	}

	// The operator now authors a two-hour floor: revision 1 violates it.
	gate.pol = RoutinePolicy{InForce: true, MinIntervalSec: 7200, PolicyRefs: []string{"p1"}}

	revs := rh.h.do("GET", "/v1/m/orchestration/schedules/"+id+"/revisions", rh.tok, nil, rh.hdr)
	if revs.code != http.StatusOK {
		t.Fatalf("revisions = %d %s", revs.code, revs.raw)
	}
	hourly := ""
	for _, it := range revs.body["items"].([]any) {
		m := it.(map[string]any)
		var snap map[string]any
		if err := json.Unmarshal([]byte(mustJSON(m["snapshot"])), &snap); err != nil {
			t.Fatal(err)
		}
		if iv, ok := snap["expected_interval_seconds"].(float64); ok && iv == 3600 {
			hourly = m["id"].(string)
		}
	}
	if hourly == "" {
		t.Fatalf("no hourly revision found: %s", revs.raw)
	}

	r := rh.h.do("POST", "/v1/m/orchestration/schedules/"+id+"/restore", rh.tok,
		map[string]any{"revision_id": hourly}, rh.hdr)
	if r.code != http.StatusUnprocessableEntity || r.body["code"] != codeRoutineFloor {
		t.Fatalf("restore of a shape below the floor = %d %s, want 422 %s", r.code, r.raw, codeRoutineFloor)
	}

	// The schedule kept the compliant shape.
	got := rh.h.do("GET", "/v1/m/orchestration/schedules/"+id, rh.tok, nil, rh.hdr)
	if iv := got.body["expected_interval_seconds"].(float64); iv != 7200 {
		t.Fatalf("interval after the refused restore = %v, want 7200 (unchanged)", iv)
	}
}

// --- the approval must be BOUND and SINGLE-USE --------------------------------

// An approval that echoes a DIFFERENT plan hash must not authorize the
// declaration: without the binding, an approval opened for one routine shape
// authorizes any other.
func TestS496RequireApprovalRejectsUnboundApproval(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, RequireApproval: true}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate),
		WithApprovalGate(fakeGate{status: StatusApproved, planHash: "some-other-plan"}))

	body := cronSchedule("unbound", "0 * * * *", 3600)
	body["approval_ref"] = "appr-1"
	if r := rh.create(body); r.code != http.StatusForbidden {
		t.Fatalf("approved decision bound to a DIFFERENT plan = %d %s, want 403", r.code, r.raw)
	}
}

// A gate that echoes an EMPTY plan hash (a partial/buggy gate) must not
// authorize either — the recomputed hash is always a non-empty SHA-256.
func TestS496RequireApprovalRejectsEmptyPlanHash(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, RequireApproval: true}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate),
		WithApprovalGate(fakeGate{status: StatusApproved, emptyHash: true}))

	body := cronSchedule("emptyhash", "0 * * * *", 3600)
	body["approval_ref"] = "appr-1"
	if r := rh.create(body); r.code != http.StatusForbidden {
		t.Fatalf("approved decision echoing an EMPTY plan hash = %d %s, want 403", r.code, r.raw)
	}
}

// The plan hash must actually BIND the declaration, the owner scope and the
// policy posture — otherwise "re-submit the identical declaration" is not a
// constraint at all. (The gate-side substitution guard lives in the composition
// root's statusScoped, which compares against the STORED approval; the fake
// gate here echoes whatever it is handed, so the binding itself is asserted
// directly.)
func TestS496CreatePlanHashBindsDeclarationOwnerAndPolicy(t *testing.T) {
	tenant := model.TenantID("019f0000-0000-7000-8000-00000000beef")
	base := createScheduleRequest{
		Name: "r", SubjectKind: "agent", SubjectRef: "agent-1",
		TriggerKind: "cron", CadenceSpec: "0 * * * *", ExpectedIntervalSeconds: 3600,
	}
	scope := RoutineScope{UserRef: "u1", WorkspaceRef: "ws1"}
	pol := RoutinePolicy{Digest: "d1"}
	const owner = "user:019f0000-0000-7000-8000-0000000000a1"
	ref := createPlanHash(tenant, base, 2, owner, scope, pol)
	if ref == "" {
		t.Fatal("empty plan hash")
	}
	if createPlanHash(tenant, base, 2, owner, scope, pol) != ref {
		t.Fatal("plan hash is not deterministic")
	}

	for name, mutate := range map[string]func(*createScheduleRequest, *RoutineScope, *RoutinePolicy){
		"cadence":   func(c *createScheduleRequest, _ *RoutineScope, _ *RoutinePolicy) { c.CadenceSpec = "*/5 * * * *" },
		"interval":  func(c *createScheduleRequest, _ *RoutineScope, _ *RoutinePolicy) { c.ExpectedIntervalSeconds = 300 },
		"subject":   func(c *createScheduleRequest, _ *RoutineScope, _ *RoutinePolicy) { c.SubjectRef = "agent-2" },
		"name":      func(c *createScheduleRequest, _ *RoutineScope, _ *RoutinePolicy) { c.Name = "other" },
		"trigger":   func(c *createScheduleRequest, _ *RoutineScope, _ *RoutinePolicy) { c.TriggerKind = "manual" },
		"owner":     func(_ *createScheduleRequest, s *RoutineScope, _ *RoutinePolicy) { s.UserRef = "u2" },
		"workspace": func(_ *createScheduleRequest, s *RoutineScope, _ *RoutinePolicy) { s.WorkspaceRef = "ws2" },
		"policy":    func(_ *createScheduleRequest, _ *RoutineScope, p *RoutinePolicy) { p.Digest = "d2" },
	} {
		c, sc, p := base, scope, pol
		mutate(&c, &sc, &p)
		if got := createPlanHash(tenant, c, 2, owner, sc, p); got == ref {
			t.Errorf("changing %s did not change the plan hash — the approval would carry over", name)
		}
	}
	if createPlanHash(model.TenantID("019f0000-0000-7000-8000-00000000cafe"), base, 2, owner, scope, pol) == ref {
		t.Error("the plan hash does not bind the tenant")
	}
	// Two service tokens share an empty user AND workspace, so the owner actor
	// is the only thing separating their declarations.
	if createPlanHash(tenant, base, 2, "token:019f0000-0000-7000-8000-0000000000b2", scope, pol) == ref {
		t.Error("the plan hash does not bind the owner actor — another principal could land an approved declaration as its owner")
	}
	if createPlanHash(tenant, base, 3, owner, scope, pol) == ref {
		t.Error("the plan hash does not bind grace_factor")
	}
}

// uniqueRefGate is an approving gate that mints a DISTINCT reference per
// request, so a test can tell a spent approval from a fresh one (the shared
// fakeGate always answers "appr-1").
type uniqueRefGate struct{ n int }

func (g *uniqueRefGate) Request(_ context.Context, req ApprovalRequest) (GateDecision, error) {
	g.n++
	return GateDecision{ApprovalRef: "appr-" + strconv.Itoa(g.n), Status: StatusApproved, PlanHash: req.PlanHash}, nil
}

func (g *uniqueRefGate) Status(_ context.Context, chk ApprovalCheck) (GateDecision, error) {
	return GateDecision{ApprovalRef: chk.ApprovalRef, Status: StatusApproved, PlanHash: chk.PlanHash}, nil
}

// THE REPLAY. ApprovalGate.Status is a pure READ: it keeps reporting "approved"
// for as long as the stored row says so. Pausing a routine changes none of the
// fields the activation plan hash binds, so without a single-use claim ONE
// human decision re-activates a routine an operator keeps pausing, forever.
func TestS496ActivationApprovalIsSingleUse(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate), WithApprovalGate(&uniqueRefGate{}))

	first := rh.create(cronSchedule("replayable", "0 * * * *", 3600))
	if first.code != http.StatusCreated {
		t.Fatalf("create = %d %s", first.code, first.raw)
	}
	id := first.body["id"].(string)
	pause := func() {
		t.Helper()
		if r := rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+id, rh.tok,
			map[string]any{"desired_status": "paused"}, rh.hdr); r.code != http.StatusOK {
			t.Fatalf("pause = %d %s", r.code, r.raw)
		}
	}
	activate := func(ref string) resp {
		t.Helper()
		body := map[string]any{"desired_status": "active"}
		if ref != "" {
			body["approval_ref"] = ref
		}
		return rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+id, rh.tok, body, rh.hdr)
	}

	pause()
	gate.pol = RoutinePolicy{InForce: true, RequireApproval: true}

	p1 := activate("")
	if p1.code != http.StatusAccepted {
		t.Fatalf("phase 1 = %d %s, want 202", p1.code, p1.raw)
	}
	ref := p1.body["approval_ref"].(string)
	if r := activate(ref); r.code != http.StatusOK {
		t.Fatalf("first activation = %d %s, want 200", r.code, r.raw)
	}

	// The operator pauses it again (an incident). The SAME approval must not
	// bring it back.
	pause()
	if r := activate(ref); r.code != http.StatusConflict {
		t.Fatalf("REPLAY of a spent activation approval = %d %s, want 409", r.code, r.raw)
	}

	// A fresh decision does work.
	p2 := activate("")
	if p2.code != http.StatusAccepted {
		t.Fatalf("second phase 1 = %d %s, want 202", p2.code, p2.raw)
	}
	if r := activate(p2.body["approval_ref"].(string)); r.code != http.StatusOK {
		t.Fatalf("activation with a NEW approval = %d %s, want 200", r.code, r.raw)
	}
}

// blockingDispatcher parks inside Fire until released, so a test can hold one
// fire OPEN — past its cadence reservation, before its settle — and prove what
// a concurrent second fire sees.
type blockingDispatcher struct {
	entered chan struct{}
	release chan struct{}
	calls   int32
}

func (d *blockingDispatcher) Fire(context.Context, FireRequest) (DispatchResult, error) {
	atomic.AddInt32(&d.calls, 1)
	d.entered <- struct{}{}
	<-d.release
	return DispatchResult{Ref: "dispatched"}, nil
}

// TWO APPROVED FIRES, ONE SLOT. `last_fired_at` only advances AFTER a dispatch
// settles, so on its own the floor is a check-then-act: a second approved fire
// that starts while the first is still in flight reads the same old stamp and
// actuates inside the prohibited interval. The reservation is committed under
// the admission fence BEFORE the effect, so the second caller sees it.
//
// The first fire is held open inside the dispatcher for the whole of the
// second request — which is exactly the window the reservation exists to close,
// and which a sequential test cannot reach.
func TestS496ConcurrentFiresCannotBothPassTheFloor(t *testing.T) {
	clock := newManualClock()
	open := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, MinIntervalSec: 3600}}
	disp := &blockingDispatcher{entered: make(chan struct{}), release: make(chan struct{})}
	rh := newRoutineHarness(t, WithClock(clock), WithRoutinePolicyGate(open),
		WithApprovalGate(&uniqueRefGate{}), WithDispatcher(disp))

	created := rh.create(cronSchedule("one-slot", "0 * * * *", 3600))
	if created.code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.code, created.raw)
	}
	id := created.body["id"].(string)
	fire := func(ref string) resp {
		return rh.h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", rh.tok,
			map[string]any{"approval_ref": ref}, rh.hdr)
	}

	// Fire A: parks inside the dispatcher, reservation already committed.
	aDone := make(chan resp, 1)
	go func() { aDone <- fire("race-a") }()
	select {
	case <-disp.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("fire A never reached the dispatcher")
	}

	// A watchdog releases the parked dispatcher shortly after B is issued. In a
	// CORRECT build B is refused before it ever dispatches and returns at once;
	// in a broken one B parks too, and the watchdog lets both through so this
	// test fails on the assertions below instead of hanging.
	var once sync.Once
	release := func() { once.Do(func() { close(disp.release) }) }
	go func() { time.Sleep(2 * time.Second); release() }()

	// Fire B, while A is still in flight and last_fired_at is still unset.
	b := fire("race-b")
	release()
	a := <-aDone

	if a.code != http.StatusOK {
		t.Fatalf("fire A = %d %s, want 200", a.code, a.raw)
	}
	if b.code != http.StatusTooManyRequests || b.body["code"] != codeRoutineFloor {
		t.Fatalf("concurrent fire B = %d %s, want 429 %s — both fires passed the floor",
			b.code, b.raw, codeRoutineFloor)
	}
	if n := atomic.LoadInt32(&disp.calls); n != 1 {
		t.Fatalf("the dispatcher actuated %d times for one cadence slot; want exactly 1", n)
	}
}

// --- round-2 regressions: the scope-recovery fix broke the cap count ---------

// MF-1. The gate matches a cap on a RECOVERED scope (owner from owner_actor,
// absent workspace = the tenant default), so the count must use the SAME key.
// Counting the raw column instead makes a legacy row governed by a cap yet
// invisible to it, and the cap admits past its limit forever.
func TestS496ActiveCapCountsRowsItGoverns(t *testing.T) {
	const owner = "019f0000-0000-7000-8000-0000000000a1"
	gate := &fakeRoutineGate{pol: RoutinePolicy{
		InForce:          true,
		ActiveCaps:       []RoutineActiveCap{{ScopeKind: "user", ScopeRef: owner, Max: 1}},
		EffectiveUserRef: owner,
	}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	// A row with NO owner_user_ref, exactly like every pre routine: its
	// owner is recoverable only from owner_actor.
	if err := rh.h.st.Mutate(context.Background(), rh.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		_, cerr := repo.Create(context.Background(), model.Record{
			colSchedName: "legacy", colSubjectKind: "agent", colSubjectRef: "agent-1",
			colTriggerKind: "cron", colExpectedIvl: int64(3600), colGraceFactor: int64(2),
			colDesiredStat: "active", colOwnerActor: "user:" + owner, colOwnerActorK: "user",
		})
		return cerr
	}); err != nil {
		t.Fatal(err)
	}

	// The cap of 1 is already filled by that legacy row.
	r := rh.create(cronSchedule("second", "0 * * * *", 3600))
	if r.code != http.StatusUnprocessableEntity || r.body["code"] != codeRoutineActiveCap {
		t.Fatalf("create with the cap already filled by a legacy row = %d %s, want 422 %s — the cap does not count the rows it governs",
			r.code, r.raw, codeRoutineActiveCap)
	}
}

// MF-3. A retry of an already-claimed fire REPLAYS its recorded outcome; the
// cadence floor must not pre-empt that with a 429, or the D-05 single-use
// contract is false whenever a floor is in force.
func TestS496ReplayedFireIsNotRefusedByTheFloor(t *testing.T) {
	clock := newManualClock()
	pol := &fakeRoutineGate{pol: RoutinePolicy{InForce: true, MinIntervalSec: 3600}}
	rh := newRoutineHarness(t, WithClock(clock), WithRoutinePolicyGate(pol),
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(&countingDispatcher{ref: "dispatched"}))

	created := rh.create(cronSchedule("replayed", "0 * * * *", 3600))
	if created.code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.code, created.raw)
	}
	id := created.body["id"].(string)
	fire := func() resp {
		return rh.h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", rh.tok,
			map[string]any{"approval_ref": "appr-1"}, rh.hdr)
	}
	if r := fire(); r.code != http.StatusOK {
		t.Fatalf("first fire = %d %s, want 200", r.code, r.raw)
	}
	// The SAME approval_ref again: a retry, not a new fire.
	again := fire()
	if again.code == http.StatusTooManyRequests {
		t.Fatalf("the replay was refused by the cadence floor instead of replaying: %s", again.raw)
	}
	if again.code != http.StatusOK {
		t.Fatalf("replay = %d %s, want the recorded outcome (200)", again.code, again.raw)
	}
	if d, _ := again.body["detail"].(string); !strings.Contains(d, "replay") {
		t.Fatalf("replay did not return the recorded outcome: %s", again.raw)
	}
}

// A declaration refused for CAPACITY must not burn the human approval. The cap
// and the single-use claim commit in the same transaction, so claiming first
// leaves the claim row behind on a 422: the operator frees capacity, retries
// with the same reference, and is told 409 "already used" for an activation
// that never happened. Pinned on ALL THREE activation paths because the order
// silently diverged on restore once already.
func TestS496CapacityDenialDoesNotBurnTheApproval(t *testing.T) {
	for _, path := range []string{"create", "patch", "restore"} {
		t.Run(path, func(t *testing.T) {
			cap1 := RoutinePolicy{
				InForce: true, RequireApproval: true,
				ActiveCaps: []RoutineActiveCap{{ScopeKind: "tenant", Max: 1}},
			}
			gate := &fakeRoutineGate{pol: RoutinePolicy{}}
			rh := newRoutineHarness(t, WithRoutinePolicyGate(gate), WithApprovalGate(&uniqueRefGate{}))

			// One active routine already fills a cap of 1.
			filler := rh.create(cronSchedule("filler", "0 * * * *", 3600))
			if filler.code != http.StatusCreated {
				t.Fatalf("filler create = %d %s", filler.code, filler.raw)
			}

			// The subject of the attempt: for patch/restore a paused routine.
			target := ""
			if path != "create" {
				second := rh.create(cronSchedule("target", "0 * * * *", 3600))
				if second.code != http.StatusCreated {
					t.Fatalf("target create = %d %s", second.code, second.raw)
				}
				target = second.body["id"].(string)
				if r := rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+target, rh.tok,
					map[string]any{"desired_status": "paused"}, rh.hdr); r.code != http.StatusOK {
					t.Fatalf("pause = %d %s", r.code, r.raw)
				}
			}
			gate.pol = cap1

			attempt := func(ref string) resp {
				switch path {
				case "create":
					b := cronSchedule("blocked-by-cap", "0 * * * *", 3600)
					if ref != "" {
						b["approval_ref"] = ref
					}
					return rh.create(b)
				case "patch":
					b := map[string]any{"desired_status": "active"}
					if ref != "" {
						b["approval_ref"] = ref
					}
					return rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+target, rh.tok, b, rh.hdr)
				default:
					revs := rh.h.do("GET", "/v1/m/orchestration/schedules/"+target+"/revisions", rh.tok, nil, rh.hdr)
					var rev string
					for _, it := range revs.body["items"].([]any) {
						m := it.(map[string]any)
						var snap map[string]any
						_ = json.Unmarshal([]byte(mustJSONStr(m["snapshot"])), &snap)
						if snap["desired_status"] == "active" {
							rev = m["id"].(string)
						}
					}
					if rev == "" {
						t.Skip("no active revision to restore in this harness")
					}
					b := map[string]any{"revision_id": rev}
					if ref != "" {
						b["approval_ref"] = ref
					}
					return rh.h.do("POST", "/v1/m/orchestration/schedules/"+target+"/restore", rh.tok, b, rh.hdr)
				}
			}

			p1 := attempt("")
			if p1.code != http.StatusAccepted {
				t.Fatalf("phase 1 = %d %s, want 202", p1.code, p1.raw)
			}
			ref := p1.body["approval_ref"].(string)

			// Phase 2 while the cap is full: refused for CAPACITY, not approval.
			denied := attempt(ref)
			if denied.code != http.StatusUnprocessableEntity || denied.body["code"] != codeRoutineActiveCap {
				t.Fatalf("phase 2 at capacity = %d %s, want 422 %s", denied.code, denied.raw, codeRoutineActiveCap)
			}

			// Free capacity and retry with the SAME approval: it must still work.
			if r := rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+filler.body["id"].(string), rh.tok,
				map[string]any{"desired_status": "paused"}, rh.hdr); r.code != http.StatusOK {
				t.Fatalf("free capacity = %d %s", r.code, r.raw)
			}
			retry := attempt(ref)
			if retry.code == http.StatusConflict {
				t.Fatalf("the capacity refusal BURNED the approval: retry = %d %s", retry.code, retry.raw)
			}
			if retry.code != http.StatusCreated && retry.code != http.StatusOK {
				t.Fatalf("retry after freeing capacity = %d %s, want 201/200", retry.code, retry.raw)
			}
		})
	}
}

// mustJSONStr re-marshals a decoded snapshot field back to bytes.
func mustJSONStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Every refusal inside the fenced transaction returns nil rather than an error,
// so the transaction COMMITS. An approval claimed before any of them therefore
// survives the refusal and is burned for a change that never landed. The
// capacity denial was one such refusal; the incoherent-shape (400) refusal is
// another, and it sits further down the same closure.
func TestS496ShapeRefusalDoesNotBurnTheApproval(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate), WithApprovalGate(&uniqueRefGate{}))

	// A MANUAL-trigger routine: an expected_interval on it is the incoherent
	// combination the handler rejects with 400.
	created := rh.create(map[string]any{
		"name": "manual-r", "subject_kind": "agent", "subject_ref": "agent-1",
		"trigger_kind": "manual",
	})
	if created.code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.code, created.raw)
	}
	id := created.body["id"].(string)
	if r := rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+id, rh.tok,
		map[string]any{"desired_status": "paused"}, rh.hdr); r.code != http.StatusOK {
		t.Fatalf("pause = %d %s", r.code, r.raw)
	}
	gate.pol = RoutinePolicy{InForce: true, RequireApproval: true}

	patch := func(body map[string]any) resp {
		return rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+id, rh.tok, body, rh.hdr)
	}
	p1 := patch(map[string]any{"desired_status": "active"})
	if p1.code != http.StatusAccepted {
		t.Fatalf("phase 1 = %d %s, want 202", p1.code, p1.raw)
	}
	ref := p1.body["approval_ref"].(string)

	// Phase 2 that activates AND carries the incoherent shape: refused 400,
	// AFTER the point where the claim used to be taken.
	bad := patch(map[string]any{"desired_status": "active", "expected_interval_seconds": 3600, "approval_ref": ref})
	if bad.code != http.StatusBadRequest {
		t.Fatalf("incoherent activation = %d %s, want 400", bad.code, bad.raw)
	}

	// The approval must still be spendable: the refused patch persisted nothing.
	good := patch(map[string]any{"desired_status": "active", "approval_ref": ref})
	if good.code == http.StatusConflict {
		t.Fatalf("the 400 BURNED the approval: %s", good.raw)
	}
	if good.code != http.StatusOK {
		t.Fatalf("activation after the refused shape = %d %s, want 200", good.code, good.raw)
	}
}

// The dispatcher resolves its route on a TRIMMED subject reference, so
// " agent-1" and "agent-1" actuate the same target. The subject-wide cadence
// floor groups routines by the stored string, so storing the untrimmed form
// would split one subject into several populations and multiply its permitted
// firing rate. The reference is normalised at write.
func TestS496SubjectRefIsNormalisedSoWhitespaceCannotSplitTheSubject(t *testing.T) {
	gate := &fakeRoutineGate{pol: RoutinePolicy{}}
	rh := newRoutineHarness(t, WithRoutinePolicyGate(gate))

	for name, ref := range map[string]string{"padded": "  agent-1  ", "plain": "agent-1"} {
		body := cronSchedule("sched-"+name, "0 * * * *", 3600)
		body["subject_ref"] = ref
		r := rh.create(body)
		if r.code != http.StatusCreated {
			t.Fatalf("%s create = %d %s", name, r.code, r.raw)
		}
		if got := r.body["subject_ref"].(string); got != "agent-1" {
			t.Fatalf("%s stored subject_ref = %q, want the trimmed %q — a whitespace alias would be a separate cadence population", name, got, "agent-1")
		}
	}
	// A patch cannot reintroduce the alias either.
	first := rh.h.do("GET", "/v1/m/orchestration/schedules", rh.tok, nil, rh.hdr)
	if first.code != http.StatusOK {
		t.Fatalf("list = %d %s", first.code, first.raw)
	}
	id := first.body["items"].([]any)[0].(map[string]any)["id"].(string)
	p := rh.h.do("PATCH", "/v1/m/orchestration/schedules/"+id, rh.tok,
		map[string]any{"subject_ref": "  agent-2  "}, rh.hdr)
	if p.code != http.StatusOK {
		t.Fatalf("patch = %d %s", p.code, p.raw)
	}
	if got := p.body["subject_ref"].(string); got != "agent-2" {
		t.Fatalf("patched subject_ref = %q, want %q", got, "agent-2")
	}
}
