// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
)

// processBytesRead is what this process has asked read(2) for so far (rchar), whether
// or not the page cache served it: a hash of an installed tool shows here warm or cold.
func processBytesRead(t *testing.T) int64 {
	t.Helper()
	b, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Skipf("/proc/self/io is not readable: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "rchar:"); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Skip("/proc/self/io has no rchar line")
	return 0
}

// installedClaude installs a fake Claude Code, whose executable is padding bytes longer, through
// the real installer with a GPG-checked engine and the process's verified cache, and returns the
// fixture and the executable the install placed.
func installedClaude(t *testing.T, padding int) (*agentToolFixture, string) {
	t.Helper()
	f := newAgentToolFixture(t)
	previous := verifiedToolReleases
	verifiedToolReleases = toolinstall.NewVerifiedCache()
	t.Cleanup(func() { verifiedToolReleases = previous })
	verifier, err := toolinstall.NewGPGVerifier(context.Background(), exec.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	toolInstallEngine = func(context.Context) *toolinstall.Engine {
		claude := toolinstall.NewClaudeWithTrust(toolinstall.ClaudeOptions{BaseURL: f.srv.URL, Verifier: verifier, ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond}, f.key.Public, f.key.Fingerprint)
		return toolinstall.NewEngine(toolinstall.NewCatalog(claude), toolinstall.EngineOptions{InstallerVersion: "test", Verified: verifiedToolReleases})
	}
	// The padding is one trailing shell comment: the program still reports its version.
	exe := append(toolinstalltest.Executable("2.1.261", f.marker), bytes.Repeat([]byte("#"), padding)...)
	f.srv.Publish(t, f.key, "2.1.261", map[string][]byte{"linux-x64": exe})
	f.srv.SetPointer("latest", "2.1.261")
	if code, out, errOut := f.run(f.installArgs("--version", "2.1.261", "--yes", "-o", "json")...); code != 0 {
		t.Fatalf("install = %d: %s %s", code, out, errOut)
	}
	inv, err := toolInstallEngine(context.Background()).List(context.Background(), f.root)
	if err != nil || len(inv.Installed) != 1 || inv.Installed[0].State != toolinstall.StateInstalled {
		t.Fatalf("verified and kept: %+v %v", inv, err)
	}
	want := inv.Installed[0].Executable
	if filepath.Base(want) != "claude" {
		t.Fatalf("the installed executable is %q", want)
	}
	return f, want
}

// dev#2584: the AI tools status, the profile resolve rule and a launch each ask which
// program Claude Code runs. The host observer cannot answer for a Claude release (its
// verifier is unavailable on purpose), so its verdict is never kept; asking it first made
// every one of those requests hash the whole executable (251 MB in the release
// container: 5 to 12 s on a busy disk). Once the verified check has kept the release,
// a lookup reads only the release directory's identity.
func TestAProgramLookupForAnInstalledClaudeDoesNotReadItsExecutableAgain(t *testing.T) {
	const padding = 4 << 20
	f, want := installedClaude(t, padding)
	observer := newHostToolObserverAt(f.root, nil, filepath.Join(t.TempDir(), "empty-home"), "")
	if got := installedSessionProgram(observer, "claude", exec.LookPath); got != want {
		t.Fatalf("the first lookup answered %q, want the verified install %q", got, want)
	}
	before := processBytesRead(t)
	for i := 0; i < 3; i++ {
		if got := installedSessionProgram(observer, "claude", exec.LookPath); got != want {
			t.Fatalf("lookup %d answered %q, want %q", i+2, got, want)
		}
	}
	if n := processBytesRead(t) - before; n >= padding {
		t.Fatalf("three lookups of an installed, kept Claude read %d bytes; a hash of its %d-byte executable per lookup is the stall of dev#2584", n, padding)
	}
}

// When the verified check answers "" (a release that no longer matches its receipt), the
// host observer cannot answer for Claude either: its verifier is unavailable on purpose, so
// asking it only reads the whole executable a second time per lookup, and neither engine
// keeps the result. The lookup hashes once, in the verified check, and falls back to the PATH.
// The bound is one hash per lookup plus one hash of slack for incidental reads.
func TestAProgramLookupForADamagedClaudeHashesItsExecutableOnce(t *testing.T) {
	const padding = 4 << 20
	const lookups = 3
	f, want := installedClaude(t, padding)
	observer := newHostToolObserverAt(f.root, nil, filepath.Join(t.TempDir(), "empty-home"), "")
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(want, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, append(body, '#'), 0o755); err != nil {
		t.Fatal(err)
	}
	onPath := func(string) (string, error) { return "/usr/local/bin/claude-on-the-path", nil }
	before := processBytesRead(t)
	for i := 0; i < lookups; i++ {
		if got := installedSessionProgram(observer, "claude", onPath); got != "/usr/local/bin/claude-on-the-path" {
			t.Fatalf("lookup %d of a damaged install answered %q, want the PATH program", i+1, got)
		}
	}
	n := processBytesRead(t) - before
	if n < lookups*padding {
		t.Fatalf("%d lookups of a damaged Claude read only %d bytes; the verified check no longer hashes its %d-byte executable, so this test measures nothing", lookups, n, padding)
	}
	if n >= (lookups+1)*padding {
		t.Fatalf("%d lookups of a damaged Claude read %d bytes; more than one hash of its %d-byte executable per lookup is the host observer asked after the verified check", lookups, n, padding)
	}
}

// The lookup's order changed, never its answer: a Claude executable that no longer matches
// its receipt is not a program a lookup returns, with either engine asked first; it falls
// back to the engine's PATH, as before.
func TestAProgramLookupForATamperedClaudeFallsBackToThePath(t *testing.T) {
	f, want := installedClaude(t, 1<<10)
	observer := newHostToolObserverAt(f.root, nil, filepath.Join(t.TempDir(), "empty-home"), "")
	if got := installedSessionProgram(observer, "claude", exec.LookPath); got != want {
		t.Fatalf("before the change the lookup answered %q, want the install %q", got, want)
	}
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(want, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, append(body, '#'), 0o755); err != nil {
		t.Fatal(err)
	}
	onPath := func(string) (string, error) { return "/usr/local/bin/claude-on-the-path", nil }
	if got := installedSessionProgram(observer, "claude", onPath); got != "/usr/local/bin/claude-on-the-path" {
		t.Fatalf("a tampered executable answered %q, want the PATH program", got)
	}
}
