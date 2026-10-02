// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestBareOlivaresWithNothingInstalledSaysToStart(t *testing.T) {
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("OLIVARES_DATA_DIR", t.TempDir())
	t.Setenv("OLIVARES_SERVER_URL", "")
	out, _, err := execRoot(t, []string{}...) // empty, not nil: nil makes cobra read os.Args
	if err != nil {
		t.Fatalf("olivares: %v", err)
	}
	for _, want := range []string{"none found on this machine", "olivares quickstart", "Start here:", "  session", "All commands: olivares --help"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "Setup & Configuration:") {
		t.Fatalf("bare olivares printed the full help:\n%s", out)
	}
}

func TestBareOlivaresSignedInNamesTheNextStep(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/livez":
			w.WriteHeader(http.StatusOK)
		case "/v1/auth/whoami":
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "user", "display_name": "Ana", "actor": "user:1"})
		case agentToolsPath + "/inventory":
			_ = json.NewEncoder(w).Encode(map[string]any{"drivers": []string{"claude"}, "inventory": map[string]any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	config := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(cliConfigOverrideEnv, config)
	if err := writeCLIConfig(config, cliConfig{CurrentContext: "lab", Contexts: []cliContext{{
		Name: "lab", Server: srv.URL, Token: "t", Tenant: "x",
	}}}); err != nil {
		t.Fatal(err)
	}
	out, _, err := execRoot(t, []string{}...) // empty, not nil: nil makes cobra read os.Args
	if err != nil {
		t.Fatalf("olivares: %v", err)
	}
	for _, want := range []string{srv.URL + " (running)", "Ana (context lab)", "olivares tool install claude"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
}

func TestAuditLsShowsRecentEventsNewestFirstWithoutReads(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Query().Get("limit") == "1" {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "head_seq": 30})
			return
		}
		items := []map[string]any{}
		for seq := 25; seq <= 29; seq++ {
			items = append(items, map[string]any{"seq": seq, "occurred_at": "2026-10-01T10:00:00Z", "actor": "user:1",
				"action": map[bool]string{true: "sessions.run.created", false: "auth.login"}[seq%2 == 1]})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "head_seq": 30})
	}))
	defer srv.Close()
	out, errb, err := execSessionCLI(t, nil, append([]string{"audit", "ls", "--limit", "3"}, sessionCreds(srv.URL)...)...)
	if err != nil {
		t.Fatalf("audit ls: %v\n%s", err, errb)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || !strings.Contains(lines[1], "sessions.run.created") || !strings.Contains(lines[2], "auth.login") {
		t.Fatalf("audit ls =\n%s", out)
	}
	if len(queries) != 2 || !strings.Contains(queries[1], "exclude_action=audit.read") || !strings.Contains(queries[1], "from=15") {
		t.Fatalf("queries = %v, want a window before head 30 without audit.read", queries)
	}
}

func TestABusinessFeatureIsOneSentenceWithItsOwnExitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"error":{"code":"not_implemented","message":"MCP tool pinning requires the enterprise add-on"}}`))
	}))
	defer srv.Close()
	_, _, err := execSessionCLI(t, nil, append([]string{"mcp", "pins", "ls"}, sessionCreds(srv.URL)...)...)
	if exitcode.From(err) != exitcode.Edition || err.Error() != "MCP tool pinning is a Business feature: "+pricingURL {
		t.Fatalf("err = %v (exit %d), want the edition sentence and exit 9", err, exitcode.From(err))
	}
	root := newRootCmd()
	pins, _, _ := root.Find([]string{"mcp", "pins"})
	if pins == nil || pins.Name() != "pins" || pins.Hidden == enterpriseAddOnsLinked {
		t.Fatalf("mcp pins must stay invocable and be hidden exactly when this build lacks the add-ons")
	}
}

// TestSeatUtilizationSendsWhatTheRouteNeeds is N1 RU-03: the route needs provider,
// from and to, and the command sent none of them.
func TestSeatUtilizationSendsWhatTheRouteNeeds(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{"provider": "anthropic", "days": []any{}})
	}))
	defer srv.Close()
	if _, _, err := execSessionCLI(t, nil, append([]string{"finops", "seats", "utilization", "--provider", "anthropic"}, sessionCreds(srv.URL)...)...); err != nil {
		t.Fatalf("utilization: %v", err)
	}
	if !strings.Contains(got, "provider=anthropic") || !strings.Contains(got, "from=") || !strings.Contains(got, "to=") {
		t.Fatalf("query = %q, want provider, from and to", got)
	}
	got = ""
	// classifyOutcome is what turns a missing required flag into exit 2 (main.go).
	root := newRootCmd()
	root.SetArgs(append([]string{"finops", "seats", "utilization"}, sessionCreds(srv.URL)...))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	code, err := classifyOutcome(root.ExecuteC())
	if code != exitcode.Usage || err == nil || !strings.Contains(err.Error(), "provider") || got != "" {
		t.Fatalf("without --provider: exit %d, err %v, request %q; want a local usage error", code, err, got)
	}
}

// TestSeatUtilizationDefaultsAreComputedWhenItRuns: the CLI reference is generated from
// `--help` and must be byte-stable. --from and --to defaulted to dates computed when the
// command was built, so the page changed every day (check-cli-ref-docs, 2026-10-01).
// The flags now default to empty in --help and the range is computed when it runs.
func TestSeatUtilizationDefaultsAreComputedWhenItRuns(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"finops", "seats", "utilization"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"from", "to"} {
		if f := cmd.Flags().Lookup(name); f == nil || f.DefValue != "" {
			t.Fatalf("--%s declares a default of %q; a date in --help changes every day", name, f.DefValue)
		}
	}
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{"provider": "anthropic", "days": []any{}})
	}))
	defer srv.Close()
	if _, _, err := execSessionCLI(t, nil, append([]string{"finops", "seats", "utilization", "--provider", "anthropic"}, sessionCreds(srv.URL)...)...); err != nil {
		t.Fatalf("utilization: %v", err)
	}
	today := time.Now().UTC()
	want := "from=" + today.AddDate(0, 0, -29).Format("2006-01-02") + "&provider=anthropic&to=" + today.Format("2006-01-02")
	if got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
}

// TestRootNextFollowsTheFirstInstalledTool: bare `olivares` names the same next step
// as doctor. On 2026-10-01, with Codex signed in and no Claude Code, it said "install
// Claude Code" while doctor said to start a Codex session.
func TestRootNextFollowsTheFirstInstalledTool(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name string
		rows []toolRow
		want string
	}{
		{"nothing installed", []toolRow{{Driver: "claude"}, {Driver: "codex"}}, "olivares tool install claude"},
		{"claude signed out", []toolRow{{Driver: "claude", Installed: true, SignedIn: &no}}, "olivares tool login claude"},
		{"claude signed in", []toolRow{{Driver: "claude", Installed: true, SignedIn: &yes}}, "olivares session start <folder>"},
		{"only codex, signed in", []toolRow{{Driver: "claude"}, {Driver: "codex", Installed: true, SignedIn: &yes}},
			"olivares session start <folder> --tool codex"},
		{"only codex, signed out", []toolRow{{Driver: "codex", Installed: true, SignedIn: &no}}, "olivares tool login codex"},
		{"claude first", []toolRow{{Driver: "codex", Installed: true, SignedIn: &yes}, {Driver: "claude", Installed: true, SignedIn: &no}},
			"olivares tool login claude"},
		{"grok has no sign-in here", []toolRow{{Driver: "grok", Installed: true}}, "olivares session start <folder> --tool grok"},
	} {
		if got := rootNextFromTools(tc.rows); got != tc.want {
			t.Errorf("%s: next = %q, want %q", tc.name, got, tc.want)
		}
	}
}
