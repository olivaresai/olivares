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

	"github.com/olivaresai/olivares/core/driverfacts"
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
		if !strings.HasPrefix(filepath.Base(tmp), "olivares-tool-") || !slices.Contains(c.rw, tmp) {
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
		"claude " + strings.Join(claude.StatusArgs, " "),     // the status read
		"codex app-server --disable hooks -c mcp_servers={}", // a stdio exchange
		"codex " + strings.Join(codex.LoginArgs, " "),        // the login
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
