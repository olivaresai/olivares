// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/driverfacts"
)

// readOnlyCommands is the one list of what a status, health, usage or sign-in
// status read may run: the argument line of every command each driver's probes
// start. Reading the state of a tool never changes it: a read starts no session,
// runs none of the user's hooks or plugins, starts none of their MCP servers and
// opens no login. A row is added only after measuring the command with a canary
// hook, a canary MCP server and a canary plugin in a signed-out scratch home:
// claude 2.1.310, codex-cli 0.162.1, grok 1.0.13 and opencode 1.19.4 fired none of
// them for these lines (opencode `models` ran the plugins until OPENCODE_PURE=1), and
// cursor 2026.10.22-27dcf5f fired none for its two (a canary MCP server in the user's
// and the project's .cursor/mcp.json started only under `agent mcp list`, the control).
// A driver with a sign-in and no row fails the test below, and so does a command
// that is not listed: adding a driver or a flag is a decision made here.
var readOnlyCommands = map[string][]string{
	"claude": {
		"--version",
		"auth status --json",
		// A session that sends no prompt: initialize and get_usage only, hooks and MCP off.
		`-p --input-format stream-json --output-format stream-json --verbose --no-session-persistence --settings {"disableAllHooks":true} --strict-mcp-config --mcp-config {"mcpServers":{}}`,
	},
	"codex":      {"--version", "login status", "app-server --disable hooks"},
	"grok":       {"--version", "models"},
	"opencode":   {"--version", "auth list", "models"},
	"gemini-cli": {"--version"},
	"cursor":     {"--version", "status --format json"},
}

// Every read of every driver, signed in and signed out, for the engine user's own
// login and for a login made through Olivares, runs only listed commands and fires
// no canary (the stubs fire one when hooks, plugins or MCP are left on or a
// session is started).
func TestStatusAndHealthReadsNeverStartAnything(t *testing.T) {
	for _, f := range driverfacts.All() {
		if _, ok := readOnlyCommands[f.Key]; !ok && f.SignIn != "" {
			t.Errorf("driver %s has a sign-in and no row in readOnlyCommands: measure its reads with a canary, then add the row", f.Key)
		}
	}
	for driver := range readOnlyCommands {
		if facts, ok := driverfacts.Lookup(driver); !ok || facts.SignIn == "" {
			t.Errorf("readOnlyCommands lists %s, which has no sign-in: remove the row", driver)
		}
	}

	call, m, home := newProvidersServer(t)
	bin := filepath.Dir(m.program("claude"))
	tenant := "?tenant_id=" + url.QueryEscape(filepath.Base(home))
	tenantLogins := []string{"claude/.claude/.credentials.json", "codex/.codex/auth.json", "grok/.grok/auth.json", "cursor/.config/cursor/auth.json"}
	for _, f := range tenantLogins {
		p := filepath.Join(home, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// readAll reads every driver every way the product does, from fresh logs, and holds
	// that no canary fired, that only listed commands ran and that every command in
	// mustRun did (a row whose command never ran proves nothing).
	readAll := func(state string, mustRun map[string][]string) {
		t.Helper()
		for driver := range readOnlyCommands {
			_ = os.Remove(filepath.Join(bin, driver+".runs"))
		}
		_ = os.Remove(filepath.Join(bin, "canary.log"))
		m.mu.Lock()
		m.statusReads = nil
		m.mu.Unlock()
		ageProviders(m)
		providers(t, call, tenant)
		for driver := range readOnlyCommands {
			if code, body := call("GET", "/v1/m/agenttools/sign-in?driver="+driver, nil); code != 200 {
				t.Errorf("%s: GET sign-in for %s = %d %v, want 200", state, driver, code, body)
			}
		}
		if b, err := os.ReadFile(filepath.Join(bin, "canary.log")); err == nil {
			t.Errorf("%s: a read started something it must not:\n%s", state, b)
		}
		for driver, allowed := range readOnlyCommands {
			raw, err := os.ReadFile(filepath.Join(bin, driver+".runs"))
			if err != nil {
				t.Errorf("%s: %s never ran: %v", state, driver, err)
				continue
			}
			ran := strings.Split(strings.TrimSpace(string(raw)), "\n")
			for _, line := range ran {
				if !slices.Contains(allowed, line) {
					t.Errorf("%s: %s ran %q, which is not a listed read-only command", state, driver, line)
				}
			}
			for _, want := range mustRun[driver] {
				if !slices.Contains(ran, want) {
					t.Errorf("%s: %s never ran %q, so that row proves nothing; it ran %q", state, driver, want, ran)
				}
			}
		}
	}

	readAll("signed in", readOnlyCommands)

	// Signed out: the engine user's own logins and the tenant's are gone.
	own := os.Getenv("HOME")
	for _, f := range []string{".claude/.credentials.json", ".codex/auth.json", ".grok/auth.json", ".local/share/opencode/auth.json", ".config/cursor/auth.json"} {
		if err := os.Remove(filepath.Join(own, f)); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range tenantLogins {
		if err := os.Remove(filepath.Join(home, f)); err != nil {
			t.Fatal(err)
		}
	}
	// Signed out, a tool is asked its status and goes no further (Grok Build has no
	// status command, so its own login is read through `models`).
	readAll("signed out", map[string][]string{
		"claude": {"--version", "auth status --json"}, "codex": {"--version", "login status"},
		"grok": {"--version", "models"}, "opencode": {"--version", "auth list"}, "gemini-cli": {"--version"},
		"cursor": {"--version", "status --format json"},
	})
}
