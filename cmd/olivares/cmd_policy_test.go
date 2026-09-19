// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPolicyReplayPrintsCouldNotReconstruct(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/m/governance/decisions/replay" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":                "insufficient",
			"could_not_reconstruct": true,
			"missing":               "authorization_decision",
			"used_live_policy":      false,
			"reason_code":           "COULD NOT RECONSTRUCT",
		})
	}))
	defer srv.Close()

	cmd := newPolicyCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{
		"--server", srv.URL, "--token", "tok", "--tenant", "t1",
		"replay", "--at", "2026-09-15T12:00:00Z", "--principal", "agent-7",
		"--resource", "public.customers", "--action", "SELECT", "--resource-kind", "postgres.table",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("policy replay: %v (output: %s)", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "COULD NOT RECONSTRUCT") {
		t.Fatalf("output missing COULD NOT RECONSTRUCT: %s", out)
	}
	if !strings.Contains(out, "missing: authorization_decision") {
		t.Fatalf("output missing named fact: %s", out)
	}
}

func TestPolicyReplayRendersReconstructedAllow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":            "reconstructed",
			"outcome":           "allow",
			"recorded_outcome":  "allow",
			"policy_version_id": "art-1",
			"inputs_digest":     "abc",
			"used_live_policy":  false,
		})
	}))
	defer srv.Close()

	cmd := newPolicyCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{
		"--server", srv.URL, "--token", "tok", "--tenant", "t1",
		"replay", "--decision-id", "01aaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("policy replay: %v (output: %s)", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "RECONSTRUCTED") || !strings.Contains(out, "outcome: allow") {
		t.Fatalf("output: %s", out)
	}
	if strings.Contains(out, "used_live_policy") {
		t.Fatalf("must not claim live policy: %s", out)
	}
}
