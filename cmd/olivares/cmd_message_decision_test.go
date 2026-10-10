// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestMessageDecisionResponseUsesCurrentCredentialAndReplayHeaders(t *testing.T) {
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "absent.yaml"))
	t.Setenv("OLIVARES_TOKEN", "person-fixture")
	t.Setenv("OLIVARES_COMMUNICATION_TOKEN", "")
	id, key := model.NewID().String(), model.NewID().String()
	calls := 0
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != workAPIBase+"/decision-requests/"+id+"/responses" || r.Header.Get("If-Match") != `"v2"` || r.Header.Get("Idempotency-Key") != key {
			t.Error("response lost endpoint or replay preconditions")
		}
		want := "Bearer person-fixture"
		if status == http.StatusForbidden {
			want = "Bearer session-fixture"
		}
		if r.Header.Get("Authorization") != want {
			t.Error("response changed credential")
		}
		var body sessions.DecisionRequestResponseCommand
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Transition != sessions.DecisionResolve || body.Response.ChoiceKey != "yes" || body.Response.Reason.Text != "Proceed" {
			t.Errorf("response body=%+v err=%v", body, err)
		}
		w.WriteHeader(status)
		if status == http.StatusForbidden {
			_, _ = w.Write([]byte(`{"error":{"message":"permission denied"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"request_id":"` + id + `","state":"resolved","version":3}`))
	}))
	defer srv.Close()
	run := func(version string) (string, error) {
		cmd := newRootCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"message", "decision", "respond", id, "yes", "--reason", "Proceed", "--version", version, "--idempotency-key", key, "--server", srv.URL, "--tenant", "tenant-a", "-o", "json"})
		err := cmd.Execute()
		return out.String(), err
	}
	if out, err := run("2"); err != nil || !strings.Contains(out, `"state":"resolved"`) && !strings.Contains(out, `"state": "resolved"`) {
		t.Fatalf("response=%s err=%v", out, err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	if _, err := run("0"); err == nil || calls != 1 {
		t.Fatal("missing version reached engine")
	}
	t.Setenv("OLIVARES_COMMUNICATION_TOKEN", "session-fixture")
	status = http.StatusForbidden
	if _, err := run("2"); err == nil || calls != 2 {
		t.Fatal("denial was hidden or retried with person credential")
	}
}
