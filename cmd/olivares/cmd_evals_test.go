// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

	cfg := &evalsClientConfig{server: srv.URL, token: "tok", tenant: "t1", timeout: 0}
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
