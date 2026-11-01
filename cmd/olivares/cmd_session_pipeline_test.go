// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestSessionPipelineWaitJSON(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "finished", true: "failed"}[failed], func(t *testing.T) {
			f := newFakeSessionEngine(t)
			f.runs = []map[string]any{{"run_ref": "01a0f6dc-0000-7000-8000-000000000009", "name": "existing", "state": "running", "provider_driver": "claude", "transport": "stream-json"}}
			f.reply = []string{`{"type":"assistant","message":{"content":[{"type":"text","text":"answer"}]}}`}
			if failed {
				f.reply = append(f.reply, `{"type":"result","is_error":true,"subtype":"error_during_execution","result":"failed"}`)
			} else {
				f.reply = append(f.reply, `{"type":"result","is_error":false,"subtype":"success","result":"answer"}`)
			}
			out, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "hello", "--wait", "--json"}, sessionCreds(f.URL)...)...)
			if failed && (err == nil || exitcode.From(err) != 10) || !failed && err != nil {
				t.Fatalf("err=%v out=%s", err, out)
			}
			var doc map[string]any
			if json.Unmarshal([]byte(out), &doc) != nil || str(doc, "run_ref") == "" || doc["state"] != map[bool]string{false: "finished", true: "failed"}[failed] {
				t.Fatalf("one result document required: %s", out)
			}
			if _, ok := doc["cost_micro_usd"]; !ok {
				t.Fatal("result omitted unknown cost")
			}
			if doc["last_message"] != map[bool]string{false: "answer", true: "failed"}[failed] {
				t.Fatalf("last message=%v", doc["last_message"])
			}
		})
	}
}
func TestSessionPipelineLaunchApproval(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.launchWaits = true
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "hello", "--wait", "--json"}, sessionCreds(f.URL)...)...)
	var doc map[string]any
	if err == nil || exitcode.From(err) != 11 || json.Unmarshal([]byte(out), &doc) != nil || doc["approval_ref"] != "apr-1" {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

func TestSessionPipelineInvalidOptions(t *testing.T) {
	for _, opts := range [][]string{{"--wait", "--timeout", "-1s"}, {"--wait", "--detach"}, {"--cost-cap-micro-usd", "-1"}, {"--repository-ref", "main"}} {
		f := newFakeSessionEngine(t)
		args := append([]string{"session", "start", t.TempDir(), "hello"}, opts...)
		_, _, err := execSessionCLI(t, nil, append(args, sessionCreds(f.URL)...)...)
		if err == nil || exitcode.From(err) != exitcode.Usage {
			t.Fatalf("opts=%v err=%v", opts, err)
		}
		if len(f.postsTo(sessionRunsPath)) > 0 {
			t.Fatal("invalid invocation launched a session")
		}
	}
}

func TestSessionPipelineRepositoryDoesNotRegisterLocalFolder(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.launchWaits = true
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "start", "--repository", "https://example.com/team/repo.git", "--repository-ref", "main", "--wait", "--json"}, sessionCreds(f.URL)...)...)
	if err == nil || exitcode.From(err) != 11 {
		t.Fatalf("err=%v out=%s", err, out)
	}
	starts := f.postsTo(sessionRunsPath)
	if len(starts) != 1 || starts[0]["repository_url"] != "https://example.com/team/repo.git" || starts[0]["repository_ref"] != "main" || str(starts[0], "workspace_ref") != "" {
		t.Fatalf("start=%v", starts)
	}
	if len(f.postsTo(sessionWorkspacesPath)) != 0 {
		t.Fatal("repository start registered a local folder")
	}
}

func TestSessionPipelineTimeoutReachesEngine(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.launchWaits = true
	_, _, _ = execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "hello", "--wait", "--timeout", "2s", "--json"}, sessionCreds(f.URL)...)...)
	starts := f.postsTo(sessionRunsPath)
	if len(starts) != 1 || starts[0]["timeout_seconds"] != float64(2) {
		t.Fatalf("start=%v", starts)
	}
}

func TestSessionPipelineTerminalOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, state, reason string
		code                int
	}{
		{"timeout", "timed_out", "run timeout reached", 12},
		{"cap", "cost_cap", "run cost cap reached", 13},
		{"limit", "stopped", "operator stop", 14},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSessionEngine(t)
			f.runs = []map[string]any{{"run_ref": "01a0f6dc-0000-7000-8000-000000000009", "name": "existing", "state": "stopped", "reason": tc.reason, "provider_driver": "claude", "transport": "stream-json", "cost_micro_usd": 100}}
			out, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "hello", "--wait", "--json"}, sessionCreds(f.URL)...)...)
			var doc map[string]any
			if err == nil || exitcode.From(err) != tc.code || json.Unmarshal([]byte(out), &doc) != nil || doc["state"] != tc.state {
				t.Fatalf("err=%v out=%s", err, out)
			}
		})
	}
}
func TestSessionPipelineFailedBeforeFirstPrompt(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "01a0f6dc-0000-7000-8000-000000000009", "name": "existing", "state": "running", "provider_driver": "claude", "transport": "stream-json"}}
	f.failOnAttach = "not logged in"
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "hello", "--wait", "--json"}, sessionCreds(f.URL)...)...)
	var doc map[string]any
	if err == nil || exitcode.From(err) != 10 || json.Unmarshal([]byte(out), &doc) != nil || doc["state"] != "failed" {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

func TestSessionPipelineInterruptedTurnIsNotSuccess(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.launchDriver = "codex"
	f.runs = []map[string]any{{"run_ref": "01a0f6dc-0000-7000-8000-000000000009", "name": "existing", "state": "running", "provider_driver": "codex", "transport": "stream-json"}}
	f.reply = []string{`{"method":"turn/completed","params":{"turn":{"status":"interrupted"}}}`}
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "hello", "--wait", "--json"}, sessionCreds(f.URL)...)...)
	var doc map[string]any
	if err == nil || exitcode.From(err) != 14 || json.Unmarshal([]byte(out), &doc) != nil || doc["state"] != "limited" {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

func TestSessionPipelineApprovalBindsLimits(t *testing.T) {
	original := sessions.LaunchIntent{Action: sessions.LaunchActionCreate, Actor: "user:original", CostCapMicroUSD: 100, RunDeadlineAt: "2026-10-28T12:00:00.000000000Z"}
	for _, change := range []func(*sessions.LaunchIntent){
		func(i *sessions.LaunchIntent) { i.CostCapMicroUSD = 200 },
		func(i *sessions.LaunchIntent) { i.RunDeadlineAt = "2026-10-29T12:00:00.000000000Z" },
	} {
		wider := original
		change(&wider)
		if sessionLaunchPlanHash(original) == sessionLaunchPlanHash(wider) {
			t.Fatal("wider run limits reused the approved plan")
		}
	}
}

func TestSessionPipelineFractionalTimeoutReachesEngine(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.launchWaits = true
	_, _, _ = execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "hello", "--wait", "--timeout", "125ms", "--json"}, sessionCreds(f.URL)...)...)
	starts := f.postsTo(sessionRunsPath)
	if len(starts) != 1 || starts[0]["timeout_seconds"] != float64(0.125) {
		t.Fatalf("start=%v", starts)
	}
}

func TestSessionPipelinePendingApprovalReference(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "01a0f6dc-0000-7000-8000-000000000009", "state": "running", "provider_driver": "claude", "transport": "stream-json", "approval_ref": "launch-consumed", "pending_approval_ref": "tool-pending"}}
	f.reply = nil
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "hello", "--wait", "--json"}, sessionCreds(f.URL)...)...)
	var doc map[string]any
	if err == nil || exitcode.From(err) != 11 || json.Unmarshal([]byte(out), &doc) != nil || doc["approval_ref"] != "tool-pending" {
		t.Fatalf("err=%v out=%s", err, out)
	}
	if len(f.postsTo(sessionRunsPath+"/01a0f6dc-0000-7000-8000-000000000009/stop")) != 0 {
		t.Fatal("approval stopped the resumable run")
	}
}

func TestSessionPipelineAttachErrorRetainsTerminalOutcome(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "01a0f6dc-0000-7000-8000-000000000009", "state": "failed", "provider_driver": "claude", "transport": "stream-json"}}
	f.refuseAttach = true
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--wait", "--json"}, sessionCreds(f.URL)...)...)
	var doc map[string]any
	if err == nil || exitcode.From(err) != 10 || json.Unmarshal([]byte(out), &doc) != nil || doc["state"] != "failed" {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

func TestSessionPipelineCostPrecision(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "01a0f6dc-0000-7000-8000-000000000009", "state": "running", "provider_driver": "claude", "transport": "stream-json", "cost_micro_usd": int64(9007199254740995)}}
	f.reply = []string{`{"type":"result","is_error":false,"subtype":"success","result":"answer"}`}
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "hello", "--wait", "--json", "--cost-cap-micro-usd", "9007199254740996"}, sessionCreds(f.URL)...)...)
	var doc map[string]any
	if err != nil || decodeWorkJSON([]byte(out), &doc) != nil || doc["cost_micro_usd"] != json.Number("9007199254740995") {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

func TestSessionPipelineConfirmsConcurrentEngineStop(t *testing.T) {
	for _, capped := range []bool{false, true} {
		t.Run(map[bool]string{false: "unbounded", true: "capped"}[capped], func(t *testing.T) {
			f := newFakeSessionEngine(t)
			const ref = "01a0f6dc-0000-7000-8000-000000000009"
			f.runs = []map[string]any{{"run_ref": ref, "state": "running", "provider_driver": "claude", "transport": "stream-json", "cost_micro_usd": 100}}
			f.reply = []string{`{"type":"result","subtype":"success","result":"answer"}`}
			var stops atomic.Int32
			var observed atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == sessionRunsPath+"/"+ref+"/stop" {
					if stops.Add(1) == 1 {
						w.WriteHeader(403)
						fmt.Fprint(w, `{"error":{"code":"module_error","message":"the session authority is no longer valid"}}`)
						return
					}
					if !observed.Load() {
						t.Error("stop retried without observing terminal state")
					}
				}
				if r.Method == "GET" && r.URL.Path == sessionRunsPath+"/"+ref && stops.Load() > 0 {
					observed.Store(true)
					reason := "exit 0"
					if capped {
						reason = "run cost cap reached"
					}
					fmt.Fprintf(w, `{"run_ref":%q,"state":"stopped","reason":%q,"cost_micro_usd":100}`, ref, reason)
					return
				}
				f.serve(w, r)
			}))
			defer server.Close()
			args := []string{"session", "start", t.TempDir(), "hello", "--wait", "--json"}
			expected, state := 0, "finished"
			if capped {
				args = append(args, "--cost-cap-micro-usd", "100")
				expected, state = 13, "cost_cap"
			}
			out, _, err := execSessionCLI(t, nil, append(args, sessionCreds(server.URL)...)...)
			var doc map[string]any
			got := exitcode.OK
			if err != nil {
				got = exitcode.From(err)
			}
			if got != expected || json.Unmarshal([]byte(out), &doc) != nil || doc["state"] != state || stops.Load() != 2 {
				t.Fatalf("err=%v out=%s stops=%d", err, out, stops.Load())
			}
		})
	}
}

func TestSessionPipelineFinalStopFailure(t *testing.T) {
	for _, capped := range []bool{false, true} {
		t.Run(map[bool]string{false: "unbounded", true: "capped"}[capped], func(t *testing.T) {
			f := newFakeSessionEngine(t)
			const ref = "01a0f6dc-0000-7000-8000-000000000009"
			f.runs = []map[string]any{{"run_ref": ref, "state": "running", "provider_driver": "claude", "transport": "stream-json"}}
			f.reply = []string{`{"type":"result","subtype":"success","result":"answer"}`}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == sessionRunsPath+"/"+ref+"/stop" {
					fmt.Fprint(w, `{"state":"failed","reason":"session I/O evidence failed","cost_micro_usd":200}`)
					return
				}
				f.serve(w, r)
			}))
			defer server.Close()
			args := []string{"session", "start", t.TempDir(), "hello", "--wait", "--json"}
			if capped {
				args = append(args, "--cost-cap-micro-usd", "100")
			}
			out, _, err := execSessionCLI(t, nil, append(args, sessionCreds(server.URL)...)...)
			var doc map[string]any
			if err == nil || exitcode.From(err) != 10 || json.Unmarshal([]byte(out), &doc) != nil || doc["state"] != "failed" || doc["reason"] != "session I/O evidence failed" {
				t.Fatalf("err=%v out=%s", err, out)
			}
		})
	}
}

func TestSessionPipelineStopRefusalRemainsError(t *testing.T) {
	f := newFakeSessionEngine(t)
	const ref = "01a0f6dc-0000-7000-8000-000000000009"
	f.runs = []map[string]any{{"run_ref": ref, "state": "running", "provider_driver": "claude", "transport": "stream-json"}}
	f.reply = []string{`{"type":"result","subtype":"success","result":"answer"}`}
	var refused atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == sessionRunsPath+"/"+ref+"/stop" || (r.URL.Path == sessionRunsPath+"/"+ref && refused.Load()) {
			refused.Store(true)
			w.WriteHeader(403)
			fmt.Fprint(w, `{"error":{"code":"forbidden","message":"session access ended"}}`)
			return
		}
		f.serve(w, r)
	}))
	defer server.Close()
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "hello", "--wait", "--json"}, sessionCreds(server.URL)...)...)
	if err == nil || exitcode.From(err) != exitcode.Auth || out != "" {
		t.Fatalf("refusal became completion: err=%v out=%s", err, out)
	}
}
