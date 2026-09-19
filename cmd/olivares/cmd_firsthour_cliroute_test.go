// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// The defect these tests close: the first hour never named its own CLI route.
// Every panel and every page sent the operator to the console, and
// `olivares auth bootstrap` — which does the same job against the running engine
// in under 300 ms — was named by nothing the product prints. On a headless host
// it is the ONLY route.

// TestQuickstartWelcomeNamesTheCLISetupRoute pins that the panel offers it.
func TestQuickstartWelcomeNamesTheCLISetupRoute(t *testing.T) {
	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: "test", Logger: slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	var out strings.Builder
	if err := announceQuickstart(context.Background(), &out, eng, declaredConsoleAddress(t, "https://127.0.0.1:8443", false)); err != nil {
		t.Fatal(err)
	}
	assertCLISetupRoute(t, out.String(), dir, "https://127.0.0.1:8443")
}

// TestFirstBootNamesTheCLISetupRoute pins the same for the command an operator
// runs when they come back to a pending install.
func TestFirstBootNamesTheCLISetupRoute(t *testing.T) {
	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dir, Engine: "sqlite", Version: "test", Logger: slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if _, _, err := eng.setupTok.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := writeConsoleState(dir, consoleState{
		Version: consoleStateVersion, Browse: "https://127.0.0.1:8443", Listen: "127.0.0.1:8443",
	}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runFirstBoot(&out, dir, false); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "Setup is PENDING") {
		t.Fatalf("this fixture must be a pending install:\n%s", got)
	}
	assertCLISetupRoute(t, got, dir, "https://127.0.0.1:8443")
}

// assertCLISetupRoute is the shared oracle, and its middle assertion is the one
// that matters most.
//
// MEASURED 2026-09-18: the first version of this route printed
// `--setup-token-file <data-dir>/setup.token`, and that file holds the token's
// SHA-256 — the plaintext is stored NOWHERE by design, which is the same reason
// first-boot says it cannot be shown again. Run verbatim against a live engine it
// answered HTTP 403. The product would have printed a command that cannot work,
// so the test pins the stdin form AND forbids the data-directory path.
func assertCLISetupRoute(t *testing.T, got, dataDir, console string) {
	t.Helper()
	for _, want := range []string{
		"olivares auth bootstrap",
		"--server " + console,
		"--ca-cert " + filepath.Join(dataDir, "tls.crt"),
		"--setup-token-file -",
		"--password-file",
		"--save-context",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the CLI setup route lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--setup-token-file "+filepath.Join(dataDir, "setup.token")) {
		t.Errorf("that file holds the token's SHA-256, not the token: the printed command answers HTTP 403:\n%s", got)
	}
	// The token must never be offered as a flag VALUE: that form is visible in
	// the process table, which is why --setup-token-file exists.
	if strings.Contains(got, "--setup-token ") {
		t.Errorf("the printed route puts the token in the process table:\n%s", got)
	}
	// Every line the operator copies has to fit a terminal, MINUS the one path it
	// carries. A data directory can be arbitrarily long and this command cannot
	// shorten the operator's own path — what it can do is put at most one such
	// path on a line, so the overflow is the path and never the flags around it.
	// This assertion is what caught the first draft, where --ca-cert and
	// --setup-token-file shared a line and a 60-character data directory pushed
	// it to 109 columns.
	for _, line := range strings.Split(got, "\n") {
		if !strings.Contains(line, "auth bootstrap") && !strings.Contains(line, "--ca-cert") &&
			!strings.Contains(line, "--setup-token-file") && !strings.Contains(line, "--password-file") {
			continue
		}
		longest := 0
		for _, tok := range strings.Fields(line) {
			if strings.Contains(tok, string(filepath.Separator)) && len([]rune(tok)) > longest {
				longest = len([]rune(tok))
			}
		}
		if n := len([]rune(line)) - longest; n > 80 {
			t.Errorf("a line an operator copies is %d columns without its path:\n%q", n, line)
		}
	}
}

// TestFirstBootUsesAPlaceholderWhenTheConsoleIsUnknown: a command with a blank
// --server is one an operator would run and not understand.
func TestFirstBootUsesAPlaceholderWhenTheConsoleIsUnknown(t *testing.T) {
	if got := firstBootConsoleOrPlaceholder(consoleState{}, nil); got != "<console-url>" {
		t.Fatalf("no recorded address must yield a visible placeholder, got %q", got)
	}
	if got := firstBootConsoleOrPlaceholder(consoleState{Browse: "   "}, nil); got != "<console-url>" {
		t.Fatalf("a blank address must yield the placeholder, got %q", got)
	}
	if got := firstBootConsoleOrPlaceholder(consoleState{Browse: "https://x:1"}, nil); got != "https://x:1" {
		t.Fatalf("a recorded address must be used, got %q", got)
	}
}
