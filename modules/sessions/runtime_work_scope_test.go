// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
package sessions

import (
	"context"
	"encoding/json"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"testing"
	"time"
)

func TestRunWorkScopeProjectionDenyUnknownAndCorrupt(t *testing.T) {
	m := New()
	m.clock = &testClock{now: baseTime.Add(time.Hour)}
	workspace := model.NewID().String()
	other := model.NewID().String()
	cases := []string{"", `{"role":"admin","workspace_id":"` + workspace + `"}`, `{"role":"worker","workspace_id":"forged"}`, `{"role":"worker","workspace_id":"` + other + `"}`, `{"role":"worker","workspace_id":"` + workspace + `","capabilities":["work.create"]}`, `{"role":"worker","workspace_id":"` + workspace + `","token":"never expose"}`, `{"role":"orchestrator","workspace_id":"` + workspace + `","grant_id":"` + model.NewID().String() + `","capabilities":["operator.admin"]}`}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			rec := model.Record{colRunRef: "run-fixture", colState: stateRunning, colLastActivityAt: model.NewTimestamp(baseTime).String(), colRunAuthzWorkspaceID: workspace, "session_work_scope": raw}
			encoded, err := json.Marshal(m.toRunDTO(rec))
			if err != nil {
				t.Fatal(err)
			}
			var dto map[string]any
			if err := json.Unmarshal(encoded, &dto); err != nil {
				t.Fatal(err)
			}
			if _, ok := dto["work_scope"]; ok {
				t.Fatal("unknown or corrupt scope was projected as authority")
			}
		})
	}
}
func TestRunWorkScopeProjectionSeparatesProcessAndActivity(t *testing.T) {
	m := New()
	m.clock = &testClock{now: baseTime.Add(time.Hour)}
	workspace := model.NewID().String()
	grant := model.NewID().String()
	rec := model.Record{colRunRef: "run-fixture", colState: stateRunning, colLastActivityAt: model.NewTimestamp(baseTime).String(), colRunAuthzWorkspaceID: workspace, "session_work_scope": `{"role":"orchestrator","workspace_id":"` + workspace + `","grant_id":"` + grant + `","capabilities":["work.read"]}`}
	encoded, _ := json.Marshal(m.toRunDTO(rec))
	var dto map[string]any
	_ = json.Unmarshal(encoded, &dto)
	if dto["state"] != stateIdle || dto["process_state"] != stateRunning {
		t.Fatalf("activity/process states conflated: %s", encoded)
	}
	scope, ok := dto["work_scope"].(map[string]any)
	if !ok || scope["role"] != "orchestrator" || scope["workspace_id"] != workspace || scope["grant_id"] != grant {
		t.Fatalf("launch scope absent: %s", encoded)
	}
}
func TestRunWorkScopeWorkerPersistedBeforeSpawnAndAcrossStopResume(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{initSID: "scope-worker"}
	inspector := &inspectingWorkLaunchRunner{inner: runner}
	m, _, tenant, clk := newRuntimeHarness(t, WithRunner(inspector), WithCredentialSource(staticCred()))
	spy := &workSessionCredentialSpy{mintCredential: WorkSessionCredential{ID: model.NewID(), Token: "fixture-work-token", NotAfter: clk.get().Add(30 * time.Minute)}}
	m.UseWorkSessionCredentialSource(spy)
	inspector.before = func() error {
		return m.data.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			records, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{{Column: colRunName, Op: model.OpEq, Value: "scope-before-spawn"}}, Limit: 1})
			if err != nil {
				return err
			}
			if len(records) != 1 || runWorkScope(records[0]) == nil || records[0].String(colState) != statePending {
				t.Fatal("scope was not durable before spawn")
			}
			return nil
		})
	}
	created, err := m.createRun(ctx, tenant, CreateRunParams{Name: "scope-before-spawn", Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "fixture-human", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(created)
	var dto map[string]any
	_ = json.Unmarshal(encoded, &dto)
	scope, ok := dto["work_scope"].(map[string]any)
	if !ok || scope["role"] != "worker" || scope["workspace_id"] == "" {
		t.Fatalf("worker scope absent: %s", encoded)
	}
	waitFor(t, "capture", func() bool { d, _ := m.getRun(ctx, tenant, created.RunRef); return d.ClaudeSessionID != "" })
	_, err = m.stopRun(ctx, tenant, created.RunRef, "fixture-human", model.ActorUser)
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := m.getRun(ctx, tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(stopped)
	_ = json.Unmarshal(encoded, &dto)
	if dto["process_state"] != stateStopped || dto["work_scope"] == nil {
		t.Fatal("stopping discarded launch scope or invented activity")
	}
	inspector.before = nil
	resumed, err := m.resumeRun(ctx, tenant, created.RunRef, "fixture-human", model.ActorUser, "")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(resumed)
	_ = json.Unmarshal(encoded, &dto)
	if dto["process_state"] != stateRunning || dto["work_scope"] == nil {
		t.Fatalf("resume scope absent: %s", encoded)
	}
	_, _ = m.stopRun(ctx, tenant, created.RunRef, "fixture-human", model.ActorUser)
}

func TestRunWorkScopeOrchestrationSnapshotChangesOnlyOnGovernedResume(t *testing.T) {
	ctx := context.Background()
	m, st, tenant, clk := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "scope-orchestrator"}), WithCredentialSource(staticCred()))
	spy := &workSessionCredentialSpy{mintCredential: WorkSessionCredential{ID: model.NewID(), Token: "fixture-work-token", NotAfter: clk.get().Add(30 * time.Minute)}}
	m.UseWorkSessionCredentialSource(spy)
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseWorkAuthorizer(auth.NewAuthorizer(nil))
	profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), AuthSource: AuthSourceAccountHome})
	actor, err := auth.NewSystemOperator("test:work-scope", "test launch scope snapshots")
	if err != nil {
		t.Fatal(err)
	}
	var workspace model.ID
	if err := st.View(ctx, tenant, func(sc store.Scope) error { ws, err := sc.DefaultWorkspace(ctx); workspace = ws.ID; return err }); err != nil {
		t.Fatal(err)
	}
	grant := func(action string) ProviderProfile {
		raw, _ := json.Marshal(map[string]any{"role": "orchestrator", "workspace_id": workspace, "capabilities": []string{action}})
		p, err := m.PatchProfile(ctx, tenant, profile.Ref, ProfilePatch{SessionWorkGrant: raw, WorkGrantActor: actor})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	initial := grant("work.read")
	expected, err := decodeProfileWorkGrant(initial.SessionWorkGrant)
	if err != nil {
		t.Fatal(err)
	}
	created, err := m.createRun(ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, ProviderProfileRef: profile.Ref, Actor: "fixture-human", ActorKind: model.ActorUser, AgentRef: "agent-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if created.WorkScope == nil || created.WorkScope.Role != "orchestrator" || created.WorkScope.GrantID != expected.GrantID || created.WorkScope.WorkspaceID != workspace {
		t.Fatalf("scope not captured: %+v", created.WorkScope)
	}
	updated := grant("work.review")
	next, _ := decodeProfileWorkGrant(updated.SessionWorkGrant)
	reloaded, err := m.getRun(ctx, tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.WorkScope == nil || reloaded.WorkScope.GrantID != expected.GrantID {
		t.Fatal("profile replacement rewrote launch history")
	}
	waitFor(t, "capture", func() bool { d, _ := m.getRun(ctx, tenant, created.RunRef); return d.ClaudeSessionID != "" })
	_, err = m.stopRun(ctx, tenant, created.RunRef, "fixture-human", model.ActorUser)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := m.resumeRun(ctx, tenant, created.RunRef, "fixture-human", model.ActorUser, "agent-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.WorkScope == nil || resumed.WorkScope.GrantID != next.GrantID || resumed.WorkScope.Capabilities[0] != "work.review" {
		t.Fatal("governed resume did not record its revalidated grant")
	}
	_, _ = m.stopRun(ctx, tenant, created.RunRef, "fixture-human", model.ActorUser)
}
