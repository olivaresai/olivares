// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestSandboxGenerateFeedsExistingScenarioCreate(t *testing.T) {
	srv := newLot3Server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/m/sandbox/synthetic-data" {
			_, _ = w.Write([]byte(`{"samples":[{"key":"sample-0001","input":"agent-1\n"},{"key":"sample-0002","input":"agent-2\n"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"scenario-1"}`))
	})
	seed := "{{subject_kind}}-{{index}}\n"
	seedFile := filepath.Join(t.TempDir(), "seed.txt")
	if err := os.WriteFile(seedFile, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	generated, _, err := execRoot(t, lot3Args(srv.URL, "sandbox", "generate", "--count", "2", "--seed-file", seedFile, "-o", "json")...)
	if err != nil {
		t.Fatal(err)
	}
	if srv.lastPath() != "/v1/m/sandbox/synthetic-data" {
		t.Fatalf("path = %s", srv.lastPath())
	}
	var request map[string]any
	if err := json.Unmarshal([]byte(srv.lastBody()), &request); err != nil {
		t.Fatal(err)
	}
	if request["seed"] != seed || request["count"] != float64(2) || request["subject_kind"] != "agent" {
		t.Fatalf("generation request = %#v", request)
	}
	var samples []map[string]any
	if err := json.Unmarshal([]byte(generated), &samples); err != nil || len(samples) != 2 || samples[1]["input"] != "agent-2\n" {
		t.Fatalf("generated array = %q, %v", generated, err)
	}
	if _, _, err := execRootStdin(t, generated, lot3Args(srv.URL, "sandbox", "scenarios", "create", "--name", "generated", "--steps-file", "-")...); err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Steps []map[string]any `json:"steps"`
	}
	if err := json.Unmarshal([]byte(srv.lastBody()), &saved); err != nil || !reflect.DeepEqual(saved.Steps, samples) {
		t.Fatalf("create did not receive the unchanged generated steps: %v", err)
	}
	if srv.lastPath() != "/v1/m/sandbox/scenarios" || srv.calls.Load() != 2 {
		t.Fatalf("path=%s calls=%d", srv.lastPath(), srv.calls.Load())
	}
	_, _, err = execRoot(t, lot3Args(srv.URL, "sandbox", "generate", "--seed-file", seedFile+".missing")...)
	if exitcode.From(err) != exitcode.Usage || srv.calls.Load() != 2 {
		t.Fatalf("missing seed: err=%v calls=%d", err, srv.calls.Load())
	}
	text, _, err := execRootStdin(t, seed, lot3Args(srv.URL, "sandbox", "generate", "--seed-file", "-", "-o", "text")...)
	if err != nil || !strings.Contains(text, "sample-0001") || !strings.Contains(text, "agent-1") {
		t.Fatalf("text generation = %q, %v", text, err)
	}
	if err := json.Unmarshal([]byte(srv.lastBody()), &request); err != nil || request["seed"] != seed || request["count"] != float64(10) {
		t.Fatalf("stdin/default request = %#v, %v", request, err)
	}
}
