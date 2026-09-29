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
	if runtimeCompletionWireLeak(t, wire, resumed.RuntimeLaunchID) {
		t.Fatal("in-process witness leaked into the run wire DTO")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	var workspacePath string
	if err := json.Unmarshal(fields["workspace_path"], &workspacePath); err != nil {
		t.Fatal(err)
	}
	if workspacePath != second.WorkspacePath || workspacePath == "" {
		t.Fatal("completion serialization changed the public working-directory fact")
	}
}

// The harness's temporary workspace includes this test's name (and therefore
// "Completion"). That inert value is not the in-process field. Inspect field
// names structurally while still rejecting the launch UUID anywhere on the wire.
func runtimeCompletionWireLeak(t *testing.T, wire []byte, launch model.ID) bool {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		if strings.EqualFold(key, "completion") {
			return true
		}
	}
	return strings.Contains(string(wire), launch.String())
}

func TestRuntimeCompletionWireOracleDistinguishesValuesFromFields(t *testing.T) {
	launch := model.ID("0199abc0-0000-7000-8000-000000000001")
	for _, tc := range []struct {
		name string
		wire string
		leak bool
	}{
		{"public workspace value", `{"workspace_path":"/tmp/TestRuntimeCompletion/work"}`, false},
		{"unexported witness body still exposed as a field", `{"Completion":{}}`, true},
		{"lowercase witness field", `{"completion":{}}`, true},
		{"launch UUID under another field", `{"unexpected":"` + launch.String() + `"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimeCompletionWireLeak(t, []byte(tc.wire), launch); got != tc.leak {
				t.Fatalf("wire leak = %v, want %v", got, tc.leak)
			}
		})
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
