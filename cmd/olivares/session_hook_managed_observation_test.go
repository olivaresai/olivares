// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// The existing hook publication enters the real bus through its public ingest
// seam. Managed observations must instead cross the sessions authority port.
type hookProjectionBus struct {
	eventbus.Bus
	rt *runtime.Runtime
}

func (b hookProjectionBus) Publish(ctx context.Context, e event.Event) error {
	return b.rt.Ingest(ctx, e.Tenant, e.Source, e.Payload.(sdkmodel.Observation))
}

func TestSessionClaudeHookUpdatesOnlyManagedLiveRow(t *testing.T) {
	h := newHarness(t)
	m := h.set.sessions
	sessions.WithRunner(approvalProjectionRunner{})(m)
	m.UseExecutionEnvironmentRef("managed-observation-test")
	credentials := newSessionHookCredentials(h.authr, h.st, m, h.set.gov)
	var token, folder string
	sessions.WithLaunchGate(approvalProjectionLaunchGate(func(ctx context.Context, tenant model.TenantID, intent sessions.LaunchIntent) (sessions.LaunchDecision, error) {
		var err error
		folder = intent.FolderPath
		token, err = credentials.mint(ctx, tenant, intent)
		return sessions.LaunchDecision{Allowed: err == nil}, err
	}))(m)
	var profile struct {
		Ref string `json:"profile_ref"`
	}
	if code := h.reqInto(http.MethodPost, "/v1/m/sessions/provider-profiles", h.adminToken, h.tenantA, map[string]any{"driver": "claude", "auth_source": "provider_account_home", "config_home": t.TempDir(), "user_home": t.TempDir()}, &profile); code != http.StatusCreated {
		t.Fatalf("profile=%d", code)
	}
	var run struct {
		Ref string `json:"run_ref"`
	}
	if code := h.reqInto(http.MethodPost, "/v1/m/sessions/runs", h.adminToken, h.tenantA, map[string]any{"transport": "stream-json", "permission_mode": "plan", "isolation": "native", "provider_profile_ref": profile.Ref}, &run); code != http.StatusCreated {
		t.Fatalf("launch=%d", code)
	}
	t.Cleanup(func() { h.req(http.MethodPost, "/v1/m/sessions/runs/"+run.Ref+"/stop", h.adminToken, h.tenantA, nil) })
	var liveRef string
	deadline := time.Now().Add(3 * time.Second)
	for liveRef == "" {
		var dto map[string]any
		h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+run.Ref, h.adminToken, h.tenantA, nil, &dto)
		liveRef, _ = dto["live_ref"].(string)
		if liveRef == "" {
			if time.Now().After(deadline) {
				t.Fatalf("bridge did not register its managed row: %v", dto)
			}
			time.Sleep(time.Millisecond * 10)
		}
	}
	d := newClaudeHookDecider(&hookpep.Decider{DefaultPolicy: &hookpep.PolicyDoc{Default: "allow"}, Authr: credentials, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Store: h.st, Clock: time.Now, Log: discardLog(), Bus: hookProjectionBus{rt: h.rt}, SessionObservation: m.RecordSessionObservation})
	body, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Read", "tool_input": map[string]any{"file_path": filepath.Join(folder, "source.txt")}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	claude.NewHookPEP(d, nil, time.Now).ServeHTTP(rec, req)
	var reply struct {
		Output struct {
			Decision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || reply.Output.Decision != "allow" {
		t.Fatalf("hook=%s err=%v", rec.Body.String(), err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		var dto map[string]any
		h.reqInto(http.MethodGet, "/v1/m/sessions/live/by-id/"+liveRef, h.adminToken, h.tenantA, nil, &dto)
		if dto["current_action"] == "Read" {
			break
		}
		if code, _ := h.req(http.MethodGet, "/v1/m/sessions/live/"+run.Ref, h.adminToken, h.tenantA, nil); code == http.StatusOK {
			t.Fatal("hook created a legacy duplicate instead of updating the launched session")
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed tool activity was not projected: %v", dto)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, _ := h.req(http.MethodGet, "/v1/m/sessions/live/"+run.Ref, h.adminToken, h.tenantA, nil); code != http.StatusNotFound {
		t.Fatalf("legacy run row=%d; want404", code)
	}
	principal, scope, err := credentials.Resolve(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	cost := sdkmodel.CostSample{Labels: map[string]string{"olivares.core_session_id": model.NewID().String()}, SessionRef: run.Ref, ProviderRef: "anthropic", ModelRef: "managed-projection-test", InputTokens: 7, OutputTokens: 3, CostMicroUSD: 17, Gateway: sdkmodel.GatewayDirect, Provenance: sdkmodel.ProvenanceEstimated, OccurredAt: time.Now().UTC()}
	if err := m.RecordSessionObservation(t.Context(), principal, cost); err != nil {
		t.Fatal(err)
	}
	// This subsequent ordinary observation is a public bus-delivery barrier for
	// sessions' subscriber: the marked cost event ahead of it must not fold again.
	barrier := "managed-observation-barrier-" + run.Ref
	if err := h.rt.Ingest(t.Context(), h.tenantA, "test.barrier", sdkmodel.EdgeObservation{OriginKind: "session", OriginRef: barrier, ResourceKind: "test", ResourceRef: "barrier", ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		if code, _ := h.req(http.MethodGet, "/v1/m/sessions/live/"+barrier, h.adminToken, h.tenantA, nil); code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bus delivery barrier was not folded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertManaged := func(wantCost float64, wantTimeline int) {
		t.Helper()
		var live map[string]any
		if code := h.reqInto(http.MethodGet, "/v1/m/sessions/live/by-id/"+liveRef, h.adminToken, h.tenantA, nil, &live); code != http.StatusOK {
			t.Fatalf("live=%d", code)
		}
		if live["attribution"] != "managed" || live["canonical_sid"] != scope.SessionRef || live["run_ref"] != run.Ref || live["tool_call_count"] != float64(1) || live["event_count"] != float64(1) || live["cost_micro_usd"] != wantCost {
			t.Fatalf("managed row lost scope or double-folded: %v", live)
		}
		var timeline struct {
			Items []map[string]any `json:"items"`
		}
		if code := h.reqInto(http.MethodGet, "/v1/m/sessions/live/by-id/"+liveRef+"/timeline", h.adminToken, h.tenantA, nil, &timeline); code != http.StatusOK || len(timeline.Items) != wantTimeline {
			t.Fatalf("timeline=%d rows=%d", code, len(timeline.Items))
		}
		for _, ref := range []string{run.Ref, scope.SessionRef} {
			if code, _ := h.req(http.MethodGet, "/v1/m/sessions/live/"+ref, h.adminToken, h.tenantA, nil); code != http.StatusNotFound {
				t.Fatalf("legacy duplicate for %s=%d", ref, code)
			}
		}
	}
	assertManaged(17, 2)
	deadline = time.Now().Add(3 * time.Second)
	for {
		var spend struct {
			Buckets []struct {
				Key  string `json:"key"`
				Cost int64  `json:"cost_micro_usd"`
			} `json:"buckets"`
		}
		if code := h.reqInto(http.MethodGet, "/v1/m/finops/spend?dimension=session", h.adminToken, h.tenantA, nil, &spend); code != http.StatusOK {
			t.Fatalf("finops=%d", code)
		}
		found := false
		for _, bucket := range spend.Buckets {
			if bucket.Key == scope.SessionRef && bucket.Cost == 17 {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("FinOps did not receive canonical session cost: %+v", spend)
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertAttemptCost := func(wantRows int) string {
		t.Helper()
		var dto struct {
			CoreSessionID string `json:"core_session_id"`
		}
		if code := h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+run.Ref, h.adminToken, h.tenantA, nil, &dto); code != http.StatusOK || dto.CoreSessionID == "" {
			t.Fatalf("managed core session: code=%d id=%q", code, dto.CoreSessionID)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			var costs []model.CostRecord
			err := h.st.View(t.Context(), model.TenantID(h.tenantA), func(sc store.Scope) error {
				var err error
				costs, _, err = sc.Costs().List(t.Context(), model.Query{Limit: 100})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			matching := 0
			for _, row := range costs {
				if row.InputTokens == 7 && row.OutputTokens == 3 && row.CostMicroUSD == 17 {
					matching++
					if row.SessionID.IsZero() {
						t.Fatal("managed cost has no core Session attribution")
					}
				}
			}
			current := 0
			for _, row := range costs {
				if row.SessionID.String() == dto.CoreSessionID && row.CostMicroUSD == 17 {
					current++
				}
			}
			if matching == wantRows && current == 1 {
				return dto.CoreSessionID
			}
			if time.Now().After(deadline) {
				t.Fatalf("attempt ledger rows=%d want%d; current=%d", matching, wantRows, current)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	firstCore := assertAttemptCost(1)
	human, err := h.authr.Authenticate(t.Context(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	wrongFence, wrongWorkspace, wrongSession, wrongRun := principal, principal, principal, principal
	wrongFence.SessionFence++
	wrongWorkspace.SessionWorkspaceID = model.NewID()
	wrongSession.SessionIdentity = "another-session"
	wrongRun.SessionRunRef = "another-run"
	for name, candidate := range map[string]auth.Principal{"human": human, "fence": wrongFence, "workspace": wrongWorkspace, "session": wrongSession, "run": wrongRun} {
		if err := m.RecordSessionObservation(t.Context(), candidate, cost); err == nil {
			t.Fatalf("%s wrote a managed observation without its current scope", name)
		}
	}
	foreign := cost
	foreign.SessionRef = "another-session"
	if err := m.RecordSessionObservation(t.Context(), principal, foreign); err == nil {
		t.Fatal("a foreign session reference was silently projected")
	}
	assertManaged(17, 2)
	if code, _ := h.req(http.MethodPost, "/v1/m/sessions/runs/"+run.Ref+"/stop", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("stop=%d", code)
	}
	if code, _ := h.req(http.MethodPost, "/v1/m/sessions/runs/"+run.Ref+"/resume", h.adminToken, h.tenantA, nil); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("resume=%d", code)
	}
	if err := m.RecordSessionObservation(t.Context(), principal, cost); err == nil {
		t.Fatal("the old generation wrote into its successor")
	}
	current, _, err := credentials.Resolve(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RecordSessionObservation(t.Context(), current, cost); err != nil {
		t.Fatal(err)
	}
	assertManaged(34, 3)
	if second := assertAttemptCost(2); second == firstCore {
		t.Fatal("resume reused its predecessor core Session")
	}

}
