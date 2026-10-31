// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// Every child of this module goes through the composition's ChildCommand, which
// confines it like a session child (#1114): the provider probes (one-shot
// commands and stdio exchanges), the status read and the login. Each may write
// its configuration home, its login home unless that is the engine user's own
// home, and a private TMPDIR removed once it has exited.
func TestEveryToolChildGoesThroughTheChildCommand(t *testing.T) {
	type child struct {
		tool     string
		args, rw []string
		cmd      *exec.Cmd
	}
	var mu sync.Mutex
	var children []child
	call, m, home := newProvidersServer(t)
	m.SetChildCommand(func(ctx context.Context, rw, ro []string, program string, args ...string) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, program, args...)
		mu.Lock()
		children = append(children, child{filepath.Base(program), args, rw, cmd})
		mu.Unlock()
		return cmd, nil
	})
	previous := toolCommand
	toolCommand = func(ctx context.Context, program string, args ...string) *exec.Cmd {
		t.Errorf("%s %v started without the composition's ChildCommand", filepath.Base(program), args)
		return exec.CommandContext(ctx, program, args...)
	}
	t.Cleanup(func() { toolCommand = previous })

	providers(t, call, "")
	code, flow := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "codex"})
	if code != 202 {
		t.Fatalf("sign-in start = %d %v", code, flow)
	}
	call("DELETE", "/v1/m/agenttools/sign-in/"+flow["id"].(string), nil)
	m.wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	engineHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	ran := map[string]bool{}
	for _, c := range children {
		name := c.tool + " " + strings.Join(c.args, " ")
		ran[name] = true
		facts, ok := driverfacts.Lookup(c.tool)
		if !ok {
			t.Errorf("unexpected child %s", name)
			continue
		}
		tmp := envValue(c.cmd.Env, "TMPDIR")
		// confine.ChildTempDir names the child's private directory s-*.
		if !strings.HasPrefix(filepath.Base(tmp), "s-") || !slices.Contains(c.rw, tmp) {
			t.Errorf("%s: TMPDIR %q is not its private writable directory (rw %q)", name, tmp, c.rw)
		} else if _, err := os.Stat(tmp); !os.IsNotExist(err) {
			t.Errorf("%s: its TMPDIR %s outlived it (%v)", name, tmp, err)
		}
		want := []string{filepath.Join(home, c.tool, facts.ConfigDir), tmp, filepath.Join(home, c.tool)}
		if envValue(c.cmd.Env, "HOME") == engineHome { // the tool's own login, in the engine user's home
			want = []string{filepath.Join(engineHome, facts.ConfigDir), tmp}
		}
		if c.tool == "opencode" {
			want = c.rw[:len(want)] // OpenCode's data, state and cache homes follow
		}
		if !slices.Equal(c.rw[:min(len(want), len(c.rw))], want) || slices.Contains(c.rw, engineHome) {
			t.Errorf("%s may write %q, want %q and never the engine user's home", name, c.rw, want)
		}
	}
	claude, _ := driverfacts.Lookup("claude")
	codex, _ := driverfacts.Lookup("codex")
	for _, want := range []string{
		"claude --version", // a one-shot probe command
		"claude " + strings.Join(claude.StatusArgs, " "), // the status read
		"codex app-server --disable hooks",               // a stdio exchange
		"codex " + strings.Join(codex.LoginArgs, " "),    // the login
	} {
		if !ran[want] {
			t.Errorf("%q did not go through the ChildCommand; children: %v", want, slices.Collect(maps.Keys(ran)))
		}
	}
}

// A child the composition cannot confine is not started at all: the status read,
// the provider probe and the login report the failure instead of running it as
// the engine user.
func TestAToolChildThatCannotBeConfinedDoesNotRun(t *testing.T) {
	call, m, _ := newProvidersServer(t)
	m.SetChildCommand(func(context.Context, []string, []string, string, ...string) (*exec.Cmd, error) {
		return nil, errors.New("confine: refused")
	})
	previous := toolCommand
	toolCommand = func(ctx context.Context, program string, args ...string) *exec.Cmd {
		t.Errorf("%s %v started unconfined", filepath.Base(program), args)
		return exec.CommandContext(ctx, program, args...)
	}
	t.Cleanup(func() { toolCommand = previous })
	if code, body := call("GET", "/v1/m/agenttools/sign-in?driver=claude", nil); code == 200 {
		t.Errorf("status read = %d %v, want a failure", code, body)
	}
	if p := providers(t, call, "")["claude"]; !strings.Contains(fmt.Sprint(p["error"]), "confine: refused") {
		t.Errorf("provider probe = %v, want the confinement failure", p)
	}
	if code, body := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "codex"}); code != 503 {
		t.Errorf("sign-in start = %d %v, want 503", code, body)
	}
}

// Without confinement on the node, the login and the status read of an
// unsigned install (none-origin-only Grok Build, origin-tarball Cursor CLI)
// are refused with one sentence naming Landlock and the tool never runs; a
// signed tool signs in as before (#3312).
func TestUnsignedToolChildRefusedWhileUnconfined(t *testing.T) {
	var m *Module
	bin := ""
	call, _ := newSignInServer(t, func(mod *Module, dir string) {
		m, bin = mod, dir
		for name, script := range map[string]string{"claude": providerClaude, "grok": providerGrok, "cursor": providerCursor} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		cat, err := toolinstall.NewCapabilityCatalog(
			toolinstall.NewCatalog(toolinstall.NewClaude(toolinstall.ClaudeOptions{})),
			toolinstall.NewGrok(toolinstall.GrokOptions{}), toolinstall.NewCursor(toolinstall.CursorOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		mod.engine = toolinstall.NewEngineWithCapabilities(cat, toolinstall.EngineOptions{})
	})
	previous := hostConfinement
	hostConfinement = func() confine.State {
		return confine.State{Mode: confine.ModeNone, Reason: "landlock unavailable in this test"}
	}
	t.Cleanup(func() { hostConfinement = previous })

	// The login of an unsigned install is refused before the tool starts.
	code, body := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "grok"})
	refusalError := func(body map[string]any) map[string]any {
		e, _ := body["error"].(map[string]any)
		return e
	}
	if e := refusalError(body); code != 422 || e["code"] != toolinstall.KindVerificationUnavailable {
		t.Fatalf("grok sign-in start = %d %v, want 422 %s", code, body, toolinstall.KindVerificationUnavailable)
	}
	if msg := fmt.Sprint(refusalError(body)["message"]); !strings.Contains(msg, "Landlock") || !strings.Contains(msg, "olivares doctor") {
		t.Errorf("grok sign-in refusal %q does not name Landlock and the fix", msg)
	}

	// The status read of an unsigned install is refused the same way.
	code, body = call("GET", "/v1/m/agenttools/sign-in?driver=cursor", nil)
	if e := refusalError(body); code != 422 || e["code"] != toolinstall.KindVerificationUnavailable {
		t.Fatalf("cursor status read = %d %v, want 422 %s", code, body, toolinstall.KindVerificationUnavailable)
	}
	if msg := fmt.Sprint(refusalError(body)["message"]); !strings.Contains(msg, "Landlock") {
		t.Errorf("cursor status refusal %q does not name Landlock", msg)
	}

	// Neither unsigned tool ran.
	for _, driver := range []string{"grok", "cursor"} {
		if _, err := os.Stat(filepath.Join(bin, driver+".runs")); !os.IsNotExist(err) {
			t.Errorf("%s ran without confinement: %v", driver, err)
		}
	}

	// A signed tool signs in as before.
	code, body = call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "claude"})
	if code != 202 || body["state"] != "needs_code" {
		t.Fatalf("claude sign-in start = %d %v, want 202 needs_code", code, body)
	}
	call("DELETE", "/v1/m/agenttools/sign-in/"+body["id"].(string), nil)
	m.wg.Wait()
	if _, err := os.Stat(filepath.Join(bin, "claude.runs")); err != nil {
		t.Errorf("the signed tool's login did not run: %v", err)
	}
}
