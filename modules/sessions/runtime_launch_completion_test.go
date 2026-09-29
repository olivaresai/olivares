// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestRuntimeCompletionRetainsOriginalAttemptAcrossResume(t *testing.T) {
	runner := &fakeRunner{initSID: "completion-provider-session"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
	ctx := context.Background()
	first, err := m.createRun(ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:operator", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	original, ok := first.Completion.Identity()
	if !ok || original.RuntimeLaunchID.IsZero() || original.RunRef != first.RunRef || original.Tenant != tenant {
		t.Fatalf("missing original completion: %#v", original)
	}
	live, ok := m.rt.getLive(tenant, first.RunRef)
	if !ok || live.launchID != original.RuntimeLaunchID || original.SessionSID != live.claim.SID || !validCanonicalSID(original.SessionSID) {
		t.Fatal("completion did not capture the launched attempt")
	}
	waitFor(t, "original provider identity", func() bool { r, _ := m.getRun(ctx, tenant, first.RunRef); return r.ClaudeSessionID != "" })
	if _, err := m.stopRun(ctx, tenant, first.RunRef, "user:operator", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	second, err := m.resumeRun(ctx, tenant, first.RunRef, "user:operator", model.ActorUser, "")
	if err != nil {
		t.Fatal(err)
	}
	resumed, ok := second.Completion.Identity()
	if !ok || resumed.RuntimeLaunchID == original.RuntimeLaunchID || resumed.RunRef != original.RunRef {
		t.Fatalf("resume reused original completion: %#v", resumed)
	}
	resumedLive, ok := m.rt.getLive(tenant, second.RunRef)
	if !ok || resumed.SessionSID != resumedLive.claim.SID || !validCanonicalSID(resumed.SessionSID) {
		t.Fatal("resume completion omitted its original SID")
	}
	retained, ok := first.Completion.Identity()
	if !ok || retained != original {
		t.Fatal("retained completion followed the current row")
	}
	presented, err := m.getRun(ctx, tenant, first.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := presented.Completion.Identity(); ok {
		t.Fatal("ordinary read manufactured a completion")
	}
	wire, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "Completion") || strings.Contains(string(wire), resumed.RuntimeLaunchID.String()) {
		t.Fatal("in-process witness leaked into the run wire DTO")
	}
}

func TestRuntimeCompletionCannotBeBuiltFromAReplacementRow(t *testing.T) {
	tenant, workspace, original, replacement := model.NewTenantID(), model.NewID(), model.NewID(), model.NewID()
	rec := model.Record{colRunRef: "run-original", colRuntimeLaunchID: replacement.String(), colRunAuthzWorkspaceID: workspace.String(), colRunClaimSID: newSID(), colState: stateRunning}
	if _, ok := runtimeLaunchCompletion(tenant, "run-original", original, rec).Identity(); ok {
		t.Fatal("current row replaced original attempt")
	}
	if _, ok := (RuntimeLaunchCompletion{}).Identity(); ok {
		t.Fatal("empty completion manufactured identity")
	}
}

func TestRuntimeCompletionRequiresTheCommittedCanonicalSID(t *testing.T) {
	tenant, workspace, launch := model.NewTenantID(), model.NewID(), model.NewID()
	rec := model.Record{colRunRef: "run-original", colRuntimeLaunchID: launch.String(), colRunAuthzWorkspaceID: workspace.String(), colState: stateRunning}
	for _, sid := range []string{"", model.NewID().String(), "invalid"} {
		rec[colRunClaimSID] = sid
		if _, ok := runtimeLaunchCompletion(tenant, "run-original", launch, rec).Identity(); ok {
			t.Fatal("completion accepted a missing or malformed SID")
		}
	}
	original := newSID()
	rec[colRunClaimSID] = original
	completion := runtimeLaunchCompletion(tenant, "run-original", launch, rec)
	rec[colRunClaimSID] = newSID()
	identity, ok := completion.Identity()
	if !ok || identity.SessionSID != original {
		t.Fatal("completion followed a later row's SID")
	}
}
