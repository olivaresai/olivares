// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// CLT1 — the console's half of the lifecycle is proved at the transport boundary in
// web/src/features/sessions/session-card-transport.test.tsx. This is the engine's half,
// and it exists because the two proofs meet nowhere else.
//
// runtime_profile_authority_test.go already drives POST /runs, /runs/{ref}/stop and
// /runs/{ref}/resume over HTTP, but it asserts STATUS CODES: its resume is the REFUSED
// one (409, an unprofiled run under profiled launches), so no test follows a
// SUCCESSFUL HTTP resume down to what the driver was actually told to do. Conversely
// TestCodexRuntimeResumeUsesTheStoredConversationAndRefusesToFallBack proves the
// stored-conversation rule, but calls m.resumeRun in-process — the route, the
// authorizer and the tenant scope are not in that chain.
//
// So a resume that answered 200 while spawning a FRESH conversation would be green in
// both files. That is the join these cases close, over the existing harness with the
// existing fixture runner: no provider binary, no credential, no network.

// resumeFlagValue returns the argument `claude --resume` was given, and whether the
// flag was present at all (runtime_bridge.go:559 appends the pair).
func resumeFlagValue(spec LaunchSpec) (string, bool) {
	for i, a := range spec.Args {
		if a == "--resume" && i+1 < len(spec.Args) {
			return spec.Args[i+1], true
		}
	}
	return "", false
}

// lifecycleHarness is the existing HTTP harness with the existing fixture runner: an
// admin, one tenant, and a legacy stream-json run whose conversation id the fixture
// announced through the real init frame.
func lifecycleHarness(t *testing.T, sid string) (*Module, *fakeRunner, *harness, string, model.TenantID, string) {
	t.Helper()
	fr := &fakeRunner{initSID: sid}
	m := New(WithRunner(fr), WithCredentialSource(staticCred()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "clt1-lifecycle")
	created := h.doJSON("POST", "/v1/m/sessions/runs", admin,
		map[string]any{"transport": "stream-json", "isolation": "native"}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("fixture create = %d", created.code)
	}
	ref := created.body["run_ref"].(string)
	waitFor(t, "the fixture's init frame binds the conversation", func() bool {
		d, _ := m.getRun(context.Background(), tenant, ref)
		return d.ClaudeSessionID == sid
	})
	return m, fr, h, admin, tenant, ref
}

// The successful HTTP resume, followed all the way to the argv the runner received.
func TestLifecycleHTTP_ResumeCarriesTheStoredConversationToTheDriver(t *testing.T) {
	const sid = "sess-stored-clt1"
	_, fr, h, admin, tenant, ref := lifecycleHarness(t, sid)

	if _, ok := resumeFlagValue(fr.lastSpec()); ok {
		t.Fatal("the FIRST launch must start a conversation, not resume one")
	}
	if got := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("stop = %d", got.code)
	}
	launched := launchCount(fr)

	got := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/resume", admin, nil, tenantHdr(tenant))
	if got.code != http.StatusOK {
		t.Fatalf("resume = %d", got.code)
	}
	if launchCount(fr) != launched+1 {
		t.Fatalf("a resume must spawn exactly one new process: %d -> %d", launched, launchCount(fr))
	}

	// The join. A 200 alone would also be returned by a resume that quietly started a
	// new conversation; only the argv distinguishes them.
	resumed, ok := resumeFlagValue(fr.lastSpec())
	if !ok {
		t.Fatalf("the resume launch carried no --resume: %v", fr.lastSpec().Args)
	}
	if resumed != sid {
		t.Fatalf("the resume continued %q, not the stored conversation %q", resumed, sid)
	}
	if got.body["run_ref"] != ref {
		t.Fatalf("the resume answered another run: %v", got.body["run_ref"])
	}
}

// The negative control the positive case cannot give: the route is tenant-scoped, so a
// resume presented under a foreign tenant must reach no runner at all.
func TestLifecycleHTTP_ResumeUnderAForeignTenantReachesNoRunner(t *testing.T) {
	_, fr, h, admin, tenant, ref := lifecycleHarness(t, "sess-foreign-clt1")
	other := h.createOrg(admin, "clt1-other")

	if got := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("stop = %d", got.code)
	}
	launched := launchCount(fr)

	got := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/resume", admin, nil, tenantHdr(other))
	if got.code == http.StatusOK {
		t.Fatalf("a foreign tenant resumed the run: %d", got.code)
	}
	if launchCount(fr) != launched {
		t.Fatalf("a refused resume spawned a process: %d -> %d", launched, launchCount(fr))
	}
}
