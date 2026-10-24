// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// "The default works" (NEXT-COMMON): POST /runs fills an empty provider profile with
// the one the engine resolves for Claude Code (runtime_default_profile_test.go). A
// workflow's session-launch step names none, so the work launch must resolve the same
// default, or no workflow can ever start a managed session.

// defaultProfileWorkHarness is a runtime that can launch for work and resolve a
// default profile: Claude Code is installed on the node, and signed in when signedIn.
func defaultProfileWorkHarness(t *testing.T, signedIn bool) (*Module, store.Store, model.TenantID, *fakeRunner, *loginStub) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // the engine user's own home: never the product's login
	runner := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()), WithProviderSecretVault(newFakeVault()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
	)
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(t.TempDir())
	stub := &loginStub{
		installed: map[string]bool{"claude": true},
		signedIn:  map[string]bool{"claude": signedIn},
	}
	m.ToolLogin = stub.status
	return m, st, tenant, runner, stub
}

func TestLaunchForWorkWithoutAProviderProfileRunsUnderTheDefault(t *testing.T) {
	// A blank ref and a whitespace-only one both name no profile.
	for _, ref := range []string{"", "   "} {
		t.Run("ref="+ref, func(t *testing.T) {
			m, st, tenant, runner, _ := defaultProfileWorkHarness(t, true)
			itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
			spec := workLaunchSpec(t, m, tenant, itemID, agentRef, ref)

			var control RuntimeControl = m
			managed, err := control.LaunchForWork(context.Background(), tenant, spec)
			if err != nil {
				t.Fatalf("LaunchForWork without a provider profile: %v", err)
			}
			if managed.State != stateRunning || !validCanonicalSID(managed.SessionID) {
				t.Fatalf("managed run = %#v", managed)
			}
			// The run is bound to the profile POST /runs would have chosen for this node.
			want, err := m.ResolveProfile(context.Background(), tenant, providerDriverClaude)
			if err != nil || want.Created {
				t.Fatalf("default profile after the launch = %+v, %v; want the one the launch made, reused", want, err)
			}
			rec, err := m.loadRun(context.Background(), tenant, managed.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			if got := rec.String(colRunProfileID); got != want.Profile.Ref {
				t.Fatalf("run profile = %q, want the default %q", got, want.Profile.Ref)
			}
			// The same dispatch again resolves the same default: the durable row comes back
			// and the runner is not called a second time.
			replayed, err := control.LaunchForWork(context.Background(), tenant, spec)
			if err != nil || !replayed.Replayed || replayed.RunRef != managed.RunRef {
				t.Fatalf("replay without a provider profile = %#v, %v; want the first run", replayed, err)
			}
			if n := len(runner.specs); n != 1 {
				t.Fatalf("runner launches = %d, want 1", n)
			}
		})
	}
}

func TestLaunchForWorkWithoutAProviderProfileAndNothingToRunOnSpawnsNothing(t *testing.T) {
	m, st, tenant, runner, stub := defaultProfileWorkHarness(t, false)
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	spec := workLaunchSpec(t, m, tenant, itemID, agentRef, "")

	var control RuntimeControl = m
	_, err := control.LaunchForWork(context.Background(), tenant, spec)
	if got := statusOf(err); got != http.StatusConflict {
		t.Fatalf("LaunchForWork with nothing to run on = %v (status %d), want the resolve rule's 409", err, got)
	}
	if n := len(runner.specs); n != 0 {
		t.Fatalf("runner launches = %d, want none for a refused launch", n)
	}
	// The refusal consumed nothing: once Claude is signed in, the same dispatch launches.
	stub.mu.Lock()
	stub.signedIn["claude"] = true
	stub.mu.Unlock()
	managed, err := control.LaunchForWork(context.Background(), tenant, spec)
	if err != nil || managed.Replayed || managed.State != stateRunning {
		t.Fatalf("launch after signing in = %#v, %v; want a first launch", managed, err)
	}
}

// A step that names a profile keeps it: the default is neither resolved nor needed, so
// a node with nothing to run the default on still launches the named profile.
func TestLaunchForWorkWithANamedProviderProfileDoesNotResolveTheDefault(t *testing.T) {
	m, st, tenant, runner, _ := defaultProfileWorkHarness(t, false)
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	named := ensureRuntimeTestProfileRef(t, m, tenant)
	spec := workLaunchSpec(t, m, tenant, itemID, agentRef, named)

	var control RuntimeControl = m
	managed, err := control.LaunchForWork(context.Background(), tenant, spec)
	if err != nil {
		t.Fatalf("LaunchForWork with a named profile: %v", err)
	}
	rec, err := m.loadRun(context.Background(), tenant, managed.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.String(colRunProfileID); got != named {
		t.Fatalf("run profile = %q, want the named %q", got, named)
	}
	if n := len(runner.specs); n != 1 {
		t.Fatalf("runner launches = %d, want 1", n)
	}
}
