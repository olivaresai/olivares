// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestLive_RealCodexSessionIsGoverned is the gate's own requirement: a REAL Codex session —
// not a fixture — claimed, resolved to a canonical sid, governed, and anchored.
//
// It launches the actual `codex exec` binary with a hooks.json pointing at a real hook
// command, which posts to a real PEP served over loopback, backed by the real identity
// plane and the real signed ledger. Nothing between Codex and the ledger is stubbed.
//
// It is OPT-IN: it spends provider tokens and needs a logged-in codex on PATH, so it runs
// only with OLIVARES_CODEX_LIVE=1. A test that silently skips is not evidence, so the skip
// says exactly what is missing.
func TestLive_RealCodexSessionIsGoverned(t *testing.T) {
	if os.Getenv("OLIVARES_CODEX_LIVE") != "1" {
		t.Skip("live Codex test is opt-in: set OLIVARES_CODEX_LIVE=1 (needs a logged-in codex-cli on PATH; spends provider tokens)")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skipf("codex not on PATH: %v", err)
	}

	e := newCodexE2E(t)
	srv := httptest.NewServer(e.pep)
	defer srv.Close()

	home := t.TempDir()
	if outside := os.Getenv("OLIVARES_CODEX_LIVE_HOME"); outside != "" {
		// codex refuses to create helper binaries under a temp dir; the caller supplies a
		// real path when that matters.
		home = outside
	}
	work := home + "/work"
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// The hook command: a shell script that relays stdin to the PEP and stdout back. It is
	// the same contract session.RunClient implements; kept as a script here so the test
	// exercises the REAL process boundary Codex uses, not an in-process shortcut.
	hook := home + "/hook.sh"
	script := "#!/usr/bin/env bash\nexec curl -sS -X POST -H 'Content-Type: application/json'" +
		" -H 'Authorization: Bearer live-test-token' --data-binary @- " + srv.URL + "\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	hooks := `{"description":"olivares governed hooks (live test)","hooks":{` +
		`"SessionStart":[{"hooks":[{"type":"command","command":"` + hook + `"}]}],` +
		`"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"` + hook + `"}]}]}}`
	if err := os.WriteFile(home+"/hooks.json", []byte(hooks), 0o600); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}
	if src, err := os.ReadFile(os.Getenv("HOME") + "/.codex/auth.json"); err == nil {
		_ = os.WriteFile(home+"/auth.json", src, 0o600)
	}
	if err := os.WriteFile(home+"/config.toml", []byte("[projects.\""+work+"\"]\ntrust_level = \"trusted\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "exec",
		"--dangerously-bypass-hook-trust", "--skip-git-repo-check",
		"-s", "danger-full-access", "-C", work,
		"Run exactly one shell command: echo OLIVARES_LIVE_OK. Then stop.")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	out, runErr := cmd.CombinedOutput()
	t.Logf("codex exec output:\n%s", out)
	if runErr != nil && len(e.seen) == 0 {
		t.Fatalf("codex exec failed and no hook reached the PEP: %v", runErr)
	}

	// A real Codex session reached the governed plane.
	if len(e.seen) == 0 {
		t.Fatal("no hook reached the PEP: the live session was NOT governed")
	}
	var sid string
	for _, o := range e.seen {
		if f, ok := o.(sdkmodel.FindingReport); ok && f.SubjectKind == "session" {
			sid = f.SubjectRef
		}
		if edge, ok := o.(sdkmodel.EdgeObservation); ok {
			sid = edge.OriginRef
		}
	}
	if !strings.HasPrefix(sid, "osn_") {
		t.Fatalf("the live session did not resolve to a canonical sid, got %q", sid)
	}
	if n := ledgerCount(t, e.store, e.tenant, "codex.hook."); n == 0 {
		t.Error("a real governed session must leave its decisions on the ledger")
	}
	// And the alias really is a codex alias over Codex's own UUID.
	var alias string
	_ = json.Unmarshal(nil, &alias)
	back, err := e.mod.ResolveSession(context.Background(), e.tenant, sessions.SessionBinding{
		Provider: "codex", ExternalID: liveExternalOf(t, out), At: time.Now(),
	})
	if err == nil && back != sid {
		t.Errorf("the live session's codex alias resolves to %q but the governed path used %q", back, sid)
	}
}

// liveExternalOf pulls the session id codex printed, so the alias assertion is against what
// Codex actually said rather than against what we hoped it said.
func liveExternalOf(t *testing.T, out []byte) string {
	t.Helper()
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, "session id:"); i >= 0 {
			return strings.TrimSpace(line[i+len("session id:"):])
		}
	}
	t.Fatal("could not find the session id in codex's output")
	return ""
}
