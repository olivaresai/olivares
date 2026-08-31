// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"net/http"
	"testing"
)

// the routine-policy seam, proven END TO END through the REAL
// composition root.
//
// The failure this file exists to prevent is the one itself repaired: an
// enforcement path that looks complete in its own module but is never wired, so
// the control decides nothing in the running plane. A unit test of the adapter
// cannot see that; only driving buildModules + wire.go can.

// TestRoutinePolicyGateWiredThroughCompositionRoot authors a REAL routine
// policy through the governance API and proves the orchestration module refuses
// a schedule that violates it. A regression that dropped the
// WithRoutinePolicyGate call in wire.go would leave this create at 201.
func TestRoutinePolicyGateWiredThroughCompositionRoot(t *testing.T) {
	h := newHarness(t)

	// An hourly cadence floor, tenant-wide.
	if code, raw := h.req("POST", "/v1/m/governance/routine-policies", h.adminToken, h.tenantA, map[string]any{
		"name": "s496-floor", "scope_kind": "tenant", "max_cadence_seconds": 3600,
	}); code != http.StatusCreated {
		t.Fatalf("create routine policy = %d: %s", code, raw)
	}

	// A five-minute routine is below the floor and must be refused.
	code, raw := h.req("POST", "/v1/m/orchestration/schedules", h.adminToken, h.tenantA, map[string]any{
		"name": "s496-too-fast", "subject_kind": "agent", "subject_ref": "agent-s496",
		"trigger_kind": "cron", "cadence_spec": "*/5 * * * *", "expected_interval_seconds": 300,
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("create below the wired cadence floor = %d, want 422 (the routine-policy gate is NOT wired): %s", code, raw)
	}

	// A compliant routine still lands — the gate denies the violation, not the surface.
	if code, raw := h.req("POST", "/v1/m/orchestration/schedules", h.adminToken, h.tenantA, map[string]any{
		"name": "s496-compliant", "subject_kind": "agent", "subject_ref": "agent-s496",
		"trigger_kind": "cron", "cadence_spec": "0 * * * *", "expected_interval_seconds": 3600,
	}); code != http.StatusCreated {
		t.Fatalf("compliant create = %d, want 201: %s", code, raw)
	}
}

// TestTargetEnvironmentResolverWiredThroughCompositionRoot guards the OTHER
// half of the pair. The cadence-floor wireproof above passes even with
// WithTargetEnvironmentResolver deleted from wire.go, because a floor never
// consults the resolver — so without this test the blocked-environment control
// could ship unwired and every suite would stay green.
//
// It asserts the FIRE path: this harness provisions no dispatcher config, so
// the resolver correctly reports no route for the subject, and a
// blocked-environment policy must refuse the fire deny-closed. With the option
// unwired the module falls back to unwiredTargetEnvironment — which reports the
// same "no route" — so the discriminator is the DENIAL CODE, which only the
// governance-backed policy gate can produce.
func TestTargetEnvironmentResolverWiredThroughCompositionRoot(t *testing.T) {
	h := newHarness(t)

	if code, raw := h.req("POST", "/v1/m/governance/routine-policies", h.adminToken, h.tenantA, map[string]any{
		"name": "s496-blocked-envs", "scope_kind": "tenant",
		"blocked_environments": []string{"prod"},
	}); code != http.StatusCreated {
		t.Fatalf("create blocked-env policy = %d: %s", code, raw)
	}

	var created struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/orchestration/schedules", h.adminToken, h.tenantA, map[string]any{
		"name": "s496-env-routine", "subject_kind": "agent", "subject_ref": "agent-s496-env",
		"trigger_kind": "manual",
	}, &created); code != http.StatusCreated || created.ID == "" {
		t.Fatalf("declaring an unrouted routine under a blocked-env policy = %d id=%q (declaration must stay possible)", code, created.ID)
	}

	code, raw := h.req("POST", "/v1/m/orchestration/schedules/"+created.ID+"/fire", h.adminToken, h.tenantA, nil)
	if code != http.StatusForbidden {
		t.Fatalf("fire under a blocked-env policy = %d, want 403: %s", code, raw)
	}
	if !bytes.Contains(raw, []byte("routine_policy_environment")) {
		t.Fatalf("fire denial did not carry the routine-policy environment code: %s", raw)
	}
}

// TestOrchTargetEnvironmentMirrorsDispatcherResolution pins the resolver to the
// dispatcher's OWN filtering and precedence. If they diverge, a routine policy
// would be evaluated against an environment other than the one Fire actuates
// in — policy theater with a real blast radius.
func TestOrchTargetEnvironmentMirrorsDispatcherResolution(t *testing.T) {
	var cfg orchDispatchConfig
	// Valid runtime target.
	cfg.Runtime.Targets = append(cfg.Runtime.Targets, orchRuntimeTargetJSON{
		SubjectKind: "agent", SubjectRef: "prod-bot", Runtime: "docker", Environment: "prod",
	})
	// INVALID runtime target (empty runtime) — the dispatcher skips it, so the
	// resolver must skip it too rather than answering for a subject that has no
	// actuation route.
	cfg.Runtime.Targets = append(cfg.Runtime.Targets, orchRuntimeTargetJSON{
		SubjectKind: "agent", SubjectRef: "ghost-bot", Runtime: "", Environment: "prod",
	})
	// A2A agent: a real route that carries NO environment dimension.
	cfg.A2A.Agents = append(cfg.A2A.Agents, orchA2AAgentJSON{
		SubjectKind: "agent", SubjectRef: "peer-bot", URL: "https://peer.example/a2a",
	})
	// A subject present in BOTH must resolve to the RUNTIME route, because that
	// is the one orchdispatch.Fire picks (runtimes before agents).
	cfg.A2A.Agents = append(cfg.A2A.Agents, orchA2AAgentJSON{
		SubjectKind: "agent", SubjectRef: "both-bot", URL: "https://both.example/a2a",
	})
	cfg.Runtime.Targets = append(cfg.Runtime.Targets, orchRuntimeTargetJSON{
		SubjectKind: "agent", SubjectRef: "both-bot", Runtime: "docker", Environment: "staging",
	})

	r := newOrchTargetEnvironment(cfg)
	ctx := context.Background()

	for _, c := range []struct {
		ref   string
		found bool
		env   string
		why   string
	}{
		{"prod-bot", true, "prod", "a valid runtime target carries its environment"},
		{"ghost-bot", false, "", "an entry the dispatcher SKIPS must not resolve"},
		{"peer-bot", true, "", "an A2A route exists but carries no environment"},
		{"both-bot", true, "staging", "runtime wins the precedence, exactly as Fire resolves it"},
		{"unknown-bot", false, "", "no route at all"},
	} {
		got, err := r.Resolve(ctx, "agent", c.ref)
		if err != nil {
			t.Fatalf("%s: %v", c.ref, err)
		}
		if got.RouteFound != c.found || got.Environment != c.env {
			t.Errorf("%s = %+v, want found=%t env=%q (%s)", c.ref, got, c.found, c.env, c.why)
		}
	}
}
