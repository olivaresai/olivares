// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

type managedCostTurnRunner struct {
	process *approvalProjectionProcess
}

func (r *managedCostTurnRunner) Launch(context.Context, sessions.LaunchSpec) (sessions.Process, error) {
	r.process = &approvalProjectionProcess{output: make(chan sessions.OutputFrame, 2), done: make(chan struct{})}
	r.process.output <- sessions.OutputFrame{Stream: "stdout", Data: []byte(`{"type":"system","subtype":"init","session_id":"managed-cost-provider"}`)}
	return r.process, nil
}

// Drive the normal runtime usage path from a provider result, rather than posting
// a CostSample directly. Boot must bind this producer to the shared session
// principal and owning observation port before any run starts.
func TestGovernedTurnCostUpdatesOnlyItsManagedLiveRow(t *testing.T) {
	for _, engineName := range consentEngines {
		t.Run(engineName, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			for _, name := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME"} {
				t.Setenv(name, "")
			}
			// The runner supplies the protocol process. A real executable path only
			// satisfies the existing readiness check; it is never started here.
			t.Setenv(envSessionClaudeBin, os.Args[0])
			e := bootConsentEstate(t, engineName)
			// The consent estate deliberately boots with NoIngest. This behavior
			// needs the real subscribers and publishers started as serve does.
			if err := e.eng.rt.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			m := e.eng.sessionsMod
			runner := &managedCostTurnRunner{}
			sessions.WithRunner(runner)(m)
			pep, err := buildClaudeHookPEPServer(e.eng, discardLog())
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(pep.Handler)
			t.Cleanup(server.Close)
			if err := e.eng.sessionHooks.bindEndpoint(server.Listener.Addr().String()); err != nil {
				t.Fatal(err)
			}
			profile := e.do(http.MethodPost, "/v1/m/sessions/provider-profiles", e.admin, e.tT, map[string]any{
				"driver": "claude", "auth_source": "provider_account_home", "config_home": t.TempDir(), "user_home": t.TempDir(),
			})
			if profile.code != http.StatusCreated {
				t.Fatalf("profile=%d %s", profile.code, profile.raw)
			}
			run := e.do(http.MethodPost, "/v1/m/sessions/runs", e.admin, e.tT, map[string]any{
				"transport": "stream-json", "permission_mode": "plan", "isolation": "native", "provider_profile_ref": profile.body["profile_ref"],
			})
			if run.code != http.StatusCreated {
				t.Fatalf("launch=%d %s", run.code, run.raw)
			}
			runRef, ok := run.body["run_ref"].(string)
			if !ok || runRef == "" || runner.process == nil {
				t.Fatal("launch did not create its supervised process")
			}
			t.Cleanup(func() { e.do(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/stop", e.admin, e.tT, nil) })
			_, scope, err := e.eng.sessionHooks.ResolveRun(t.Context(), e.tT, runRef)
			if err != nil {
				t.Fatal(err)
			}
			runner.process.output <- sessions.OutputFrame{Stream: "stdout", Data: []byte(`{"type":"result","subtype":"success","is_error":false,"session_id":"managed-cost-provider","result":"ok","num_turns":1,"total_cost_usd":0.000017,"usage":{"input_tokens":7,"output_tokens":3},"modelUsage":{"managed-cost-test":{"inputTokens":7,"outputTokens":3,"costUSD":0.000017}}}`)}
			deadline := time.Now().Add(5 * time.Second)
			for {
				spend := e.do(http.MethodGet, "/v1/m/finops/spend?dimension=session", e.admin, e.tT, nil)
				if spend.code != http.StatusOK {
					t.Fatalf("spend=%d %s", spend.code, spend.raw)
				}
				buckets, ok := spend.body["buckets"].([]any)
				if !ok {
					t.Fatal("spend did not return its bucket list")
				}
				if len(buckets) != 0 {
					if len(buckets) != 1 {
						t.Fatalf("one turn produced %d spend buckets", len(buckets))
					}
					bucket, ok := buckets[0].(map[string]any)
					if !ok || bucket["key"] != scope.SessionRef || bucket["cost_micro_usd"] != float64(17) || bucket["samples"] != float64(1) || bucket["input_tokens"] != float64(7) || bucket["output_tokens"] != float64(3) {
						t.Fatalf("cost lost attribution or was published twice: %v", bucket)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the ordinary turn cost never reached FinOps")
				}
				time.Sleep(10 * time.Millisecond)
			}
			live := e.do(http.MethodGet, "/v1/m/sessions/live", e.admin, e.tT, nil)
			items, ok := live.body["items"].([]any)
			if live.code != http.StatusOK || !ok || len(items) != 1 {
				t.Fatalf("one launch and one cost produced %d live rows: %d %s", len(items), live.code, live.raw)
			}
			row, ok := items[0].(map[string]any)
			if !ok || row["attribution"] != "managed" || row["run_ref"] != runRef || row["canonical_sid"] != scope.SessionRef || row["cost_micro_usd"] != float64(17) || row["input_tokens"] != float64(7) || row["output_tokens"] != float64(3) {
				t.Fatalf("the turn cost was not on its managed session: %v", row)
			}
			for _, ref := range []string{runRef, scope.SessionRef} {
				if legacy := e.do(http.MethodGet, "/v1/m/sessions/live/"+ref, e.admin, e.tT, nil); legacy.code != http.StatusNotFound {
					t.Fatalf("legacy duplicate exists for managed ref: %d %s", legacy.code, legacy.raw)
				}
			}
			liveRef, ok := row["live_ref"].(string)
			if !ok || liveRef == "" {
				t.Fatal("managed row has no navigable reference")
			}
			timeline := e.do(http.MethodGet, "/v1/m/sessions/live/by-id/"+liveRef+"/timeline", e.admin, e.tT, nil)
			entries, ok := timeline.body["items"].([]any)
			if timeline.code != http.StatusOK || !ok || len(entries) != 1 {
				t.Fatalf("one turn produced an incorrect managed timeline: %d %s", timeline.code, timeline.raw)
			}
			// The composition-root caller cannot fall back to cooperative ingestion
			// when a sample does not name its currently resolved managed run.
			sink := &sessionCostSink{credentials: e.eng.sessionHooks.SessionCredentials, sessions: m}
			for name, input := range map[string]struct {
				tenant model.TenantID
				run    string
				sid    string
			}{
				"foreign tenant":  {tenant: e.tB, run: runRef, sid: scope.SessionRef},
				"foreign run":     {tenant: e.tT, run: "another-run", sid: scope.SessionRef},
				"foreign session": {tenant: e.tT, run: runRef, sid: "another-session"},
				"missing run":     {tenant: e.tT, sid: scope.SessionRef},
			} {
				if err := sink.PublishSessionCost(t.Context(), input.tenant, sessions.SessionCostSample{RunRef: input.run, SessionRef: input.sid, CostMicroUSD: 19}); err == nil {
					t.Fatalf("%s posted cost without current run authority", name)
				}
			}
			if stopped := e.do(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/stop", e.admin, e.tT, nil); stopped.code != http.StatusOK {
				t.Fatalf("stop=%d %s", stopped.code, stopped.raw)
			}
			if err := sink.PublishSessionCost(t.Context(), e.tT, sessions.SessionCostSample{RunRef: runRef, SessionRef: scope.SessionRef, CostMicroUSD: 19}); err == nil {
				t.Fatal("a stopped run posted a managed cost observation")
			}
			after := e.do(http.MethodGet, "/v1/m/sessions/live/by-id/"+liveRef, e.admin, e.tT, nil)
			if after.code != http.StatusOK || after.body["cost_micro_usd"] != float64(17) {
				t.Fatalf("refused samples changed managed cost: %d %s", after.code, after.raw)
			}
		})
	}
}
