// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"
)

// Claude Code's "auto" lets the tool act without asking per call, like "full": it is a
// run administrator's own decision too. Anyone else is refused before anything durable
// happens, at launch and at resume.
func TestAutoPreset_OnlyARunAdministrator(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(gate))

	_, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "auto",
		Actor: actorU, ActorKind: actorKindU,
	})
	if statusOf(err) != http.StatusForbidden {
		t.Fatalf("auto without run administration = %v, want 403", err)
	}
	fr.mu.Lock()
	launched := len(fr.specs)
	fr.mu.Unlock()
	if len(gate.seen) != 0 || launched != 0 {
		t.Fatal("a refused auto launch reached the gate or the runner")
	}

	if _, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "auto",
		Actor: actorU, ActorKind: actorKindU, MayRunUnrestricted: true,
	}); err != nil {
		t.Fatalf("auto by a run administrator: %v", err)
	}
	if got := gate.last(t).PermissionMode; got != "auto" {
		t.Fatalf("gate saw mode %q", got)
	}
}

// A stored auto run is resumed only by a run administrator, as it is started.
func TestAResumeOfAnAutoRunNeedsRunAdministration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-auto"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		PermissionMode: "auto", Actor: "user:admin", ActorKind: "user", MayRunUnrestricted: true,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "the session id", func() bool {
		rec, lerr := m.loadRun(ctx, tenant, dto.RunRef)
		return lerr == nil && rec.String(colClaudeSessionID) != ""
	})
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:admin", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	before := launchCount(fr)
	if _, err := m.resumeRunAsCaller(ctx, tenant, dto.RunRef, "user:u1", "user", "", callerAsks{}); statusOf(err) != http.StatusForbidden {
		t.Fatalf("a caller without run administration resumed auto: %v, want 403", err)
	}
	if launchCount(fr) != before {
		t.Fatal("a refused resume started a child")
	}
	if _, err := m.resumeRunAsCaller(ctx, tenant, dto.RunRef, "user:admin", "user", "", answeredYes); err != nil {
		t.Fatalf("an administrator's resume: %v", err)
	}
	if mode, _ := argvValue(fr.lastSpec().Args, "--permission-mode"); mode != "auto" {
		t.Fatalf("the administrator's resume ran %q", mode)
	}
}

// Making auto a folder's default takes the authority that running auto takes, over HTTP:
// without a way to tell whether the caller may run it, the folder's administrator cannot
// set it, by name or label; with run authority it can.
func TestPatchWorkspaceAutoDefaultIsRefusedWithoutRunAuthority(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "perm-auto")
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir()}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	path := "/v1/m/sessions/workspaces/" + created.body["workspace_ref"].(string)
	authority := m.WorkAuthorizer
	m.WorkAuthorizer = nil
	t.Cleanup(func() { m.WorkAuthorizer = authority })
	for _, auto := range []string{"auto", "Auto", " AUTO "} {
		if r := h.doJSON("PATCH", path, admin, map[string]any{"default_permission": auto}, tenantHdr(tenant)); r.code != http.StatusForbidden {
			t.Errorf("PATCH default_permission=%q without run authority -> %d %s, want 403", auto, r.code, r.raw)
		}
	}
	m.WorkAuthorizer = authority
	if r := h.doJSON("PATCH", path, admin, map[string]any{"default_permission": "auto"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("PATCH auto with run authority -> %d %s, want 200", r.code, r.raw)
	}
}
