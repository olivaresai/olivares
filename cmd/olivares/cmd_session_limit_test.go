// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A session whose tool ended a turn on a limit says so in one sentence in `session
// show`, and `-o json` and `ls -o json` carry the engine's limit object as it is.
func TestSessionShowSaysWhenTheToolRanOutOfQuota(t *testing.T) {
	at := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	limit := map[string]any{"kind": "usage", "resets_at": at.Format(time.RFC3339), "account_ref": "ppf_a"}
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r1", "name": "alpha", "state": "running", "provider_driver": "claude", "limit": limit},
		{"run_ref": "r2", "name": "quiet", "state": "running", "provider_driver": "claude"},
	}
	run := func(args ...string) string {
		t.Helper()
		out, errb, err := execSessionCLI(t, nil, append(append([]string{"session"}, args...), sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("session %v: %v\n%s", args, err, errb)
		}
		return out
	}

	want := "Out of quota until " + at.Local().Format("15:04")
	if !strings.Contains(at.Local().Format("2006-01-02"), time.Now().Format("2006-01-02")) {
		want = "Out of quota until " + at.Local().Format("2006-01-02 15:04")
	}
	if out := run("show", "alpha"); !strings.Contains(out, want) {
		t.Fatalf("show alpha lacks %q:\n%s", want, out)
	}
	if out := run("show", "quiet"); strings.Contains(out, "LIMIT") {
		t.Fatalf("a session with no limit shows a limit row:\n%s", out)
	}

	var shown struct {
		Limit map[string]string `json:"limit"`
	}
	if err := json.Unmarshal([]byte(run("show", "alpha", "-o", "json")), &shown); err != nil || shown.Limit["kind"] != "usage" || shown.Limit["resets_at"] != at.Format(time.RFC3339) {
		t.Fatalf("show -o json limit = %v (%v)", shown.Limit, err)
	}
	var listed []struct {
		Name  string            `json:"name"`
		Limit map[string]string `json:"limit"`
	}
	if err := json.Unmarshal([]byte(run("ls", "-o", "json")), &listed); err != nil || len(listed) != 2 {
		t.Fatalf("ls -o json = %v (%v)", listed, err)
	}
	for _, r := range listed {
		if (r.Name == "alpha") != (r.Limit["kind"] == "usage") {
			t.Fatalf("ls -o json row %s carries limit %v", r.Name, r.Limit)
		}
	}
}

func TestLimitSentence(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.Local)
	soon := now.Add(3 * time.Hour)
	next := now.Add(30 * time.Hour)
	for _, tc := range []struct {
		name  string
		limit map[string]any
		want  string
	}{
		{"usage today", map[string]any{"kind": "usage", "resets_at": soon.UTC().Format(time.RFC3339)}, "Out of quota until 15:00"},
		{"usage another day", map[string]any{"kind": "usage", "resets_at": next.UTC().Format(time.RFC3339)}, "Out of quota until 2026-10-15 18:00"},
		{"usage without a time", map[string]any{"kind": "usage"}, "Out of quota."},
		{"usage that has lifted", map[string]any{"kind": "usage", "resets_at": now.Add(-time.Minute).UTC().Format(time.RFC3339)}, ""},
		{"rate", map[string]any{"kind": "rate"}, "Rate limited by the provider. Try again in a moment."},
		{"none", nil, ""},
	} {
		run := map[string]any{}
		if tc.limit != nil {
			run["limit"] = tc.limit
		}
		if got := limitSentence(run, now); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
