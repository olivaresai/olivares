// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// The "/" menu against the REAL Claude Code on this host (#1801). The fake child proves
// the engine's side; only this proves that the vendor program answers the request with
// what the parser reads, for the folders a session really runs in.
//
// ⛔ NO MODEL TURN CAN BE SPENT: the launch gets a fresh HOME and configuration home
// (so no credential), no hooks, no MCP servers, and the one request is the SDK's
// `initialize`, which the CLI answers locally, the same handshake
// official_cli_live_test.go uses.
func TestToolCommands_RealClaudeReportsTheFoldersSkillsAndTakesNewOnes(t *testing.T) {
	program, err := exec.LookPath(cliruntime.OfficialProgram(cliruntime.KindClaude))
	if err != nil {
		t.Skipf("LIVE claude: not on PATH (%v); the fake child still proved the engine's side", err)
	}
	work, home, config := t.TempDir(), t.TempDir(), t.TempDir()
	writeFile := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	skill := func(name, description string) string {
		return "---\nname: " + name + "\ndescription: " + description + "\n---\nBody.\n"
	}
	writeFile(filepath.Join(work, ".claude", "skills", "project-skill", "SKILL.md"), skill("project-skill", "A skill of the project folder"))
	writeFile(filepath.Join(work, ".claude", "commands", "project-cmd.md"), "---\ndescription: A command of the project folder\nargument-hint: [target]\n---\nDo it.\n")
	writeFile(filepath.Join(config, "skills", "user-skill", "SKILL.md"), skill("user-skill", "A skill of the user's configuration"))

	args := cliruntime.ClaudeArgs(cliruntime.LaunchRequest{PermissionMode: "dontAsk", ToolSurfaceDeclared: true})
	args = append(args, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-session-persistence", "--settings", `{"disableAllHooks":true}`)
	runCtx, endRun := context.WithCancel(context.Background())
	defer endRun()
	proc, err := NewProcRunner().Launch(runCtx, LaunchSpec{
		Program: program, Args: args, Dir: work,
		Env:       toSessionEnv(cliruntime.HomeEnv(cliruntime.KindClaude, home, config)),
		Isolation: IsolationNative, WaitDelay: time.Second,
	})
	if err != nil {
		t.Fatalf("launch live claude: %v", err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proc.Stop(stop)
	}()
	// The bridge's part of the exchange: the answer goes to the waiter, as in bridge().
	lr := &liveRun{proc: proc}
	var toolSaidChanged atomic.Bool
	go func() {
		for frame := range proc.Output() {
			if frame.Stream != streamStdout {
				continue
			}
			if lr.takeCommandsResponse(frame.Data) {
				continue
			}
			if f, ok := parseStreamJSON(frame.Data); ok && f.isCommandsChanged() {
				toolSaidChanged.Store(true)
			}
		}
	}()

	m := &Module{}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	first, err := m.askClaudeCommands(ctx, lr)
	if err != nil {
		t.Fatalf("live claude did not report its commands: %v", err)
	}
	find := func(items []toolCommand, name string) (toolCommand, bool) {
		for _, c := range items {
			if c.Name == name && c.Kind == "command" {
				return c, true
			}
		}
		return toolCommand{}, false
	}
	t.Logf("LIVE claude: reported %d entries", len(first))
	for name, want := range map[string]struct{ source, description string }{
		"project-skill": {"project", "A skill of the project folder"},
		"project-cmd":   {"project", "A command of the project folder"},
		"user-skill":    {"user", "A skill of the user's configuration"},
	} {
		got, ok := find(first, name)
		if !ok {
			t.Errorf("live claude reports no %q: it is not in the menu", name)
			continue
		}
		if got.Source != want.source || got.Description != want.description {
			t.Errorf("%s = source %q description %q, want %q %q", name, got.Source, got.Description, want.source, want.description)
		}
		t.Logf("LIVE claude: /%s source=%s argument_hint=%q description=%q", got.Name, got.Source, got.ArgumentHint, got.Description)
	}
	if c, ok := find(first, "project-cmd"); ok && c.ArgumentHint != "target" {
		t.Errorf("project-cmd argument hint = %q, want the tool's own %q", c.ArgumentHint, "target")
	}
	if c, ok := find(first, "compact"); !ok || c.Source != "built-in" {
		t.Errorf("compact = %+v (found %v), want a built-in the tool reports itself", c, ok)
	}

	// A skill added to the folder while the session runs: the tool's next answer has it,
	// with no restart of the session or of Olivares. The tool watches its folders a moment
	// after it starts, so the skill is added to a session that has been running a while.
	time.Sleep(4 * time.Second)
	writeFile(filepath.Join(work, ".claude", "skills", "added-later", "SKILL.md"), skill("added-later", "Added while the session ran"))
	var second []toolCommand
	deadline := time.Now().Add(30 * time.Second)
	for {
		if second, err = m.askClaudeCommands(ctx, lr); err != nil {
			t.Fatalf("second ask: %v", err)
		}
		if _, ok := find(second, "added-later"); ok || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	got, ok := find(second, "added-later")
	if !ok {
		t.Fatalf("live claude never reported the skill added to the project folder (%d entries)", len(second))
	}
	t.Logf("LIVE claude: /%s appeared after being added to the running session's folder (source=%s)", got.Name, got.Source)
	for end := time.Now().Add(10 * time.Second); !toolSaidChanged.Load() && time.Now().Before(end); {
		time.Sleep(100 * time.Millisecond)
	}
	if !toolSaidChanged.Load() {
		t.Error("live claude did not send system/commands_changed for the added skill: the engine's invalidation signal is not the real tool's")
	}
}

// #1871 against the actual vendor: a large local catalog, fresh homes, no
// credential, no hooks/MCP and no model turn. Two initialize requests prove the
// first oversized answer did not retire the live session or corrupt stdout.
func TestToolCommands_RealClaudeOversizedInitializeKeepsSessionAlive(t *testing.T) {
	program, err := exec.LookPath(cliruntime.OfficialProgram(cliruntime.KindClaude))
	if err != nil {
		t.Skip("LIVE claude is not on PATH")
	}
	work, home, config := t.TempDir(), t.TempDir(), t.TempDir()
	for n := range 1600 {
		dir := filepath.Join(work, ".claude", "commands")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		text := "---\ndescription: " + strings.Repeat("Catalog text ", 100) + "\n---\nBody.\n"
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("large-%04d.md", n)), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := cliruntime.ClaudeArgs(cliruntime.LaunchRequest{PermissionMode: "dontAsk", ToolSurfaceDeclared: true})
	args = append(args, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-session-persistence", "--settings", `{"disableAllHooks":true}`)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proc, err := NewProcRunner().Launch(runCtx, LaunchSpec{Program: program, Args: args, Dir: work,
		Env: toSessionEnv(cliruntime.HomeEnv(cliruntime.KindClaude, home, config)), Isolation: IsolationNative, WaitDelay: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := proc.Stop(context.Background()); err != nil {
			t.Errorf("stop: %v", err)
		}
	}()
	lr := &liveRun{proc: proc}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for f := range proc.Output() {
			if f.Stream == streamStdout {
				lr.takeCommandsResponse(f.Data)
			}
		}
	}()
	defer func() { _ = proc.Stop(context.Background()); <-drained }()
	m := &Module{}
	for n := range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		_, err := m.askClaudeCommands(ctx, lr)
		cancel()
		var re *runErr
		if !errors.As(err, &re) || re.status != http.StatusBadGateway || !strings.Contains(err.Error(), "output line limit") {
			t.Fatalf("LIVE initialize %d = %v, want explicit oversized catalog error", n+1, err)
		}
		if p := proc.(*procProcess); p.finishedWithin(time.Millisecond) {
			t.Fatal("LIVE catalog ended the session")
		}
		if waiting, abandoned := lr.commandWaitCount(); waiting != 0 || abandoned != 0 {
			t.Fatalf("LIVE waits = %d/%d", waiting, abandoned)
		}
		t.Logf("LIVE initialize %d: oversized catalog refused; session still live, no pending answer", n+1)
	}
}
