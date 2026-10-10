// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func toolProvidersFixture() []map[string]any {
	soon := time.Now().Add(3*time.Hour + 5*time.Minute + 30*time.Second).UTC().Format(time.RFC3339)
	later := time.Now().Add(60*time.Hour + 30*time.Minute).UTC().Format(time.RFC3339)
	return []map[string]any{
		{"instance": "claude", "driver": "claude", "default": true, "config_dir": "/home/u/.claude", "state": "ready", "installed": true,
			"version": "2.1.289 (Claude Code)", "auth_method": "claude.ai", "plan": "max", "email": "f***@example.test",
			"limits": []map[string]any{
				{"label": "5-hour", "percent": 40, "resets_at": soon, "severity": "normal"},
				{"label": "Weekly", "percent": 78, "resets_at": later, "severity": "warning"},
			},
			"models": []map[string]any{{"id": "default"}, {"id": "opus"}}, "checked_at": "2026-10-05T21:00:00Z", "source": "claude auth status --json"},
		{"instance": "codex", "driver": "codex", "default": true, "state": "not_signed_in", "installed": true, "version": "codex-cli 0.160.0",
			"next_command": "codex login --device-auth", "limits": []any{}, "models": []any{}, "checked_at": "2026-10-05T21:00:00Z", "source": "codex login status"},
		{"instance": "opencode", "driver": "opencode", "default": true, "state": "not_installed", "installed": false,
			"next_command": "olivares tool install opencode", "limits": []any{}, "models": []any{}, "checked_at": "2026-10-05T21:00:00Z", "source": ""},
	}
}

func TestToolProvidersShowsEmailPlanUsageAndTheNextCommand(t *testing.T) {
	f := newFakeToolEngine(t)
	f.providers = toolProvidersFixture()
	out, errb, err := execSessionCLI(t, nil, append([]string{"tool", "providers"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("tool providers: %v\n%s", err, errb)
	}
	for _, want := range []string{
		"Claude Code (own login)  2.1.289 (Claude Code)",
		"signed in as f***@example.test · max · claude.ai",
		"5-hour", "40% used, resets in 3 h 5 min",
		"Weekly", "78% used, resets in 2 d 12 h",
		"models: default, opus",
		"checked ", "claude auth status --json",
		"Codex (own login)  codex-cli 0.160.0", "not signed in. Run: codex login --device-auth",
		"OpenCode (own login)", "not installed. Run: olivares tool install opencode",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(f.allHits(), "GET /v1/m/agenttools/providers?tenant_id=tenant-a") {
		t.Errorf("the CLI must name its organization: %s", f.allHits())
	}
}

func TestToolProvidersShowsAStaleAnswerWithTheFailedCommand(t *testing.T) {
	f := newFakeToolEngine(t)
	f.providers = toolProvidersFixture()[:1]
	f.providers[0]["stale"] = true
	f.providers[0]["error"] = "claude auth status --json: timed out"
	out, _, err := execSessionCLI(t, nil, append([]string{"tool", "providers"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stale, the last answer is shown. claude auth status --json: timed out") || !strings.Contains(out, "signed in as f***@example.test") {
		t.Fatalf("a stale snapshot must still show the last answer and the failed command:\n%s", out)
	}
}

func TestToolProvidersJSONKeepsEveryFieldTheEngineSent(t *testing.T) {
	f := newFakeToolEngine(t)
	f.providers = toolProvidersFixture()
	out, _, err := execSessionCLI(t, nil, append([]string{"tool", "providers", "-o", "json"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Providers) != 3 {
		t.Fatalf("json = %v\n%s", err, out)
	}
	if p := got.Providers[0]; p["config_dir"] != "/home/u/.claude" || p["plan"] != "max" || p["email"] != "f***@example.test" {
		t.Fatalf("the JSON must be the engine's own: %v", p)
	}
}

func TestToolProvidersOnAnOlderEngineSaysToUpgrade(t *testing.T) {
	f := newFakeToolEngine(t)
	_, _, err := execSessionCLI(t, nil, append([]string{"tool", "providers"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.NotFound || !strings.Contains(err.Error(), "older than this CLI") {
		t.Fatalf("err = %v", err)
	}
}

func TestToolProvidersListsLongModelListsInShort(t *testing.T) {
	f := newFakeToolEngine(t)
	f.providers = toolProvidersFixture()[:1]
	var models []map[string]any
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		models = append(models, map[string]any{"id": id})
	}
	f.providers[0]["models"] = models
	out, _, err := execSessionCLI(t, nil, append([]string{"tool", "providers"}, sessionCreds(f.URL)...)...)
	if err != nil || !strings.Contains(out, "models: a, b, c, d, e, f, +2 more (-o json)") {
		t.Fatalf("err=%v\n%s", err, out)
	}
}

func TestUntilReset(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{-time.Minute: "reset time passed", 20 * time.Second: "resets in 1 min", 59 * time.Minute: "resets in 59 min",
		3*time.Hour + 5*time.Minute: "resets in 3 h 5 min", 47 * time.Hour: "resets in 47 h 0 min", 60*time.Hour + 30*time.Minute: "resets in 2 d 12 h"} {
		if got := untilReset(now.Add(d), now); got != want {
			t.Errorf("%v = %q, want %q", d, got, want)
		}
	}
}
