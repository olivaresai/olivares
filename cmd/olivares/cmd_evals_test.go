// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// evalsGateServer is a canned control plane for the gate CLI: it returns body for
// POST /gate and getBody for GET /gate/{id}.
func evalsGateServer(t *testing.T, postBody, getBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-Olivares-Tenant") != "t1" {
			t.Errorf("missing auth headers: %v", r.Header)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/m/evals/gate":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(postBody))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/m/evals/gate/"):
			_, _ = w.Write([]byte(getBody))
		default:
			http.NotFound(w, r)
		}
	}))
}

// runGateCLI executes `evals gate` against srv with extra args and returns the
// error + combined output.
func runGateCLI(t *testing.T, srv *httptest.Server, outputs string, args ...string) (error, string) {
	t.Helper()
	cmd := newEvalsGateCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetIn(strings.NewReader(outputs))
	cmd.SetArgs(append([]string{"--server", srv.URL, "--token", "tok", "--tenant", "t1"}, args...))
	return cmd.Execute(), buf.String()
}

// TestEvalsGateExitMapping proves the CI contract: effective pass/warn → exit 0,
// fail → the blocking error; an overridden fail re-checked via --check-id passes.
func TestEvalsGateExitMapping(t *testing.T) {
	gate := func(verdict, effective string, overridden bool) string {
		b, _ := json.Marshal(map[string]any{
			"id": "g1", "verdict": verdict, "effective_verdict": effective,
			"reasons": []string{"regression_vs_baseline"}, "overridden": overridden,
			"sampled": 2, "total_cases": 5,
		})
		return string(b)
	}

	srv := evalsGateServer(t, gate("fail", "fail", false), "")
	defer srv.Close()
	err, out := runGateCLI(t, srv, `{"c1":"x"}`, "--suite", "s1", "--outputs", "-")
	if !errors.Is(err, errGateFailed) {
		t.Fatalf("failing gate err = %v, want errGateFailed (output: %s)", err, out)
	}
	if !strings.Contains(out, "merge blocked") {
		t.Errorf("fail output missing the block message: %s", out)
	}

	srv2 := evalsGateServer(t, gate("pass", "pass", false), "")
	defer srv2.Close()
	if err, _ := runGateCLI(t, srv2, `{"c1":"x"}`, "--suite", "s1", "--outputs", "-"); err != nil {
		t.Fatalf("passing gate err = %v, want nil", err)
	}

	srv3 := evalsGateServer(t, gate("warn", "warn", false), "")
	defer srv3.Close()
	err, out = runGateCLI(t, srv3, `{"c1":"x"}`, "--suite", "s1", "--outputs", "-")
	if err != nil {
		t.Fatalf("warn gate err = %v, want nil (declared degradation does not block)", err)
	}
	if !strings.Contains(out, "WARNING") {
		t.Errorf("warn output not loud: %s", out)
	}

	// Overridden fail, re-checked: the EFFECTIVE verdict unblocks.
	srv4 := evalsGateServer(t, "", gate("fail", "pass", true))
	defer srv4.Close()
	err, out = runGateCLI(t, srv4, "", "--check-id", "g1")
	if err != nil {
		t.Fatalf("overridden gate err = %v, want nil", err)
	}
	if !strings.Contains(out, "overridden") {
		t.Errorf("override note missing: %s", out)
	}
}

// TestEvalsUsesTheSavedSignInAndKeepsItsMissingValueMessages: after a sign-in the
// gate reaches the engine with the saved credential; with nothing configured it
// keeps its own message and exit 1, and its longer timeout.
func TestEvalsUsesTheSavedSignInAndKeepsItsMissingValueMessages(t *testing.T) {
	for _, name := range []string{"OLIVARES_SERVER_URL", "OLIVARES_TOKEN", "OLIVARES_TENANT"} {
		t.Setenv(name, "")
	}
	config := filepath.Join(t.TempDir(), "client.yaml")
	t.Setenv(cliConfigOverrideEnv, config)
	cmd := newEvalsGateCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--check-id", "g1"})
	if err := cmd.Execute(); err == nil || err.Error() != "no server: set --server or OLIVARES_SERVER_URL" || exitcode.From(err) != exitcode.Err {
		t.Fatalf("nothing configured = %v (exit %d)", err, exitcode.From(err))
	}
	if d := cmd.Flags().Lookup("timeout").DefValue; d != "10m0s" {
		t.Fatalf("timeout default = %s", d)
	}
	srv := evalsGateServer(t, "", `{"id":"g1","verdict":"pass","effective_verdict":"pass","sampled":2,"total_cases":5}`)
	defer srv.Close()
	if err := writeCLIConfig(config, cliConfig{CurrentContext: "selected", Contexts: []cliContext{{Name: "selected", Server: srv.URL, Token: "tok", Tenant: "t1"}}}); err != nil {
		t.Fatal(err)
	}
	cmd = newEvalsGateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--check-id", "g1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gate after sign-in = %v (%s)", err, out.String())
	}
	// A connection given in full never reads the saved file, so an unrelated
	// unreadable one does not stop it.
	if err := os.WriteFile(config, []byte("{{"), 0o600); err != nil || os.Chmod(config, 0o644) != nil {
		t.Fatal(err)
	}
	cmd = newEvalsGateCmd()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--server", srv.URL, "--token", "tok", "--tenant", "t1", "--check-id", "g1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("explicit gate with an unreadable saved file = %v (%s)", err, out.String())
	}
}

// TestRunLabelSession drives the labeling loop against a canned plane: an already-
// labeled key is skipped (resume), p/f labels post immediately with the right
// human_passed, s skips, q ends the session.
func TestRunLabelSession(t *testing.T) {
	type posted struct {
		SetName string `json:"set_name"`
		Items   []struct {
			CaseKey     string `json:"case_key"`
			HumanPassed bool   `json:"human_passed"`
		} `json:"items"`
	}
	var got []posted
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/m/evals/calibration/items":
			_, _ = w.Write([]byte(`{"items":[{"case_key":"k0"}],"has_more":false}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/m/evals/calibration/items":
			var p posted
			_ = json.NewDecoder(r.Body).Decode(&p)
			got = append(got, p)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"created":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	candidates := strings.Join([]string{
		`{"case_key":"k0","output":"already labeled"}`,
		`{"case_key":"k1","output":"good output"}`,
		`{"case_key":"k2","output":"bad output"}`,
		`{"case_key":"k3","output":"skipped output"}`,
		`{"case_key":"k4","output":"never reached"}`,
	}, "\n")
	stdin := "p\nf\ns\nq\n"

	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "client.yaml"))
	cfg := &evalsClientConfig{agentClientConfig{server: srv.URL, token: "tok", tenant: "t1"}}
	if err := cfg.resolve(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runLabelSession(context.Background(), cfg, strings.NewReader(stdin), &out,
		strings.NewReader(candidates), "ref", "criterion X")
	if err != nil {
		t.Fatalf("label session: %v\n%s", err, out.String())
	}

	if len(got) != 2 {
		t.Fatalf("posted %d labels, want 2 (p+f)\n%s", len(got), out.String())
	}
	if got[0].Items[0].CaseKey != "k1" || got[0].Items[0].HumanPassed != true {
		t.Errorf("first label = %+v, want k1 pass", got[0].Items[0])
	}
	if got[1].Items[0].CaseKey != "k2" || got[1].Items[0].HumanPassed != false {
		t.Errorf("second label = %+v, want k2 fail", got[1].Items[0])
	}
	if !strings.Contains(out.String(), "2 labeled, 1 skipped, 1 already labeled") {
		t.Errorf("summary wrong: %s", out.String())
	}
}

func TestEvalsGateComparisonRefusalAndExperimentFlags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		comparison, _ := req["comparison"].(map[string]any)
		if req["model_ref"] != "model-b" || req["prompt_variant"] != "new" || comparison["mode"] != "candidate_change" || comparison["version"] != float64(1) {
			t.Errorf("experiment fields not sent: %#v", req)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"g1","verdict":"fail","effective_verdict":"fail","reasons":["baseline_unavailable"],"comparison":{"version":1,"status":"unknown","reason":"baseline_unavailable","mode":"candidate_change"}}`))
	}))
	defer srv.Close()
	err, out := runGateCLI(t, srv, `{"one":"answer"}`, "--suite", "s1", "--outputs", "-", "--baseline", "missing", "--model", "model-b", "--variant", "new", "--comparison-mode", "candidate_change")
	if !errors.Is(err, errGateFailed) {
		t.Fatalf("comparison refusal did not block CLI: %v %s", err, out)
	}
	if !strings.Contains(out, "comparison=unknown reason=baseline_unavailable mode=candidate_change") {
		t.Fatalf("comparison diagnostics not rendered: %s", out)
	}
}
