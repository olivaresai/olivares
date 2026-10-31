// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// A tool installed after boot is run at once: the program is resolved at launch.
// An explicit pin wins over the resolver; no answer keeps the official name.
func TestProgramResolverIsAskedAtLaunchAndLosesToAPin(t *testing.T) {
	installed := map[string]string{}
	resolve := func(driver string) string { return installed[driver] }

	m := New(WithProviderDriver(NewCodexDriver()), WithProgramResolver(resolve))
	codex, _ := m.driverFor("codex")
	if got := m.driverProgram(codex); got != "codex" {
		t.Fatalf("codex before install = %q, want the official name", got)
	}
	if got := m.claudeProgram(); got != "claude" {
		t.Fatalf("claude before install = %q, want the official name", got)
	}
	installed["codex"] = "/data/tools/codex/0.1/codex"
	installed["claude"] = "/data/tools/claude/2.1/claude"
	if got := m.driverProgram(codex); got != installed["codex"] {
		t.Fatalf("codex after install = %q, want %q (no restart)", got, installed["codex"])
	}
	if got := m.claudeProgram(); got != installed["claude"] {
		t.Fatalf("claude after install = %q, want %q (no restart)", got, installed["claude"])
	}

	pinned := New(WithProviderDriver(NewCodexDriver()), WithProgramResolver(resolve),
		WithDriverProgram("codex", "/opt/codex"), WithProgram("/opt/claude"))
	codex, _ = pinned.driverFor("codex")
	if got := pinned.driverProgram(codex); got != "/opt/codex" {
		t.Fatalf("pinned codex = %q, want /opt/codex", got)
	}
	if got := pinned.claudeProgram(); got != "/opt/claude" {
		t.Fatalf("pinned claude = %q, want /opt/claude", got)
	}
}

func TestCursorRunProgramRefusesAnUnresolvedBinary(t *testing.T) {
	m := New(WithProviderDriver(NewCursorDriver()), WithProgramResolver(func(string) string { return "" }))
	params := CreateRunParams{ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverCursor}}
	program, err := m.resolveRunProgram(context.Background(), "", params)
	if err == nil || program != "" {
		t.Fatalf("unresolved Cursor program = %q, %v, want a launch refusal instead of PATH agent", program, err)
	}
	var refusal *runErr
	if !errors.As(err, &refusal) || refusal.status != http.StatusServiceUnavailable ||
		refusal.msg != "Cursor CLI is not installed on this node; install it from AI tools or pin its official agent executable" {
		t.Fatalf("unresolved Cursor refusal = %v, want 503 naming the tool and its official program", err)
	}
	pinned := New(WithProviderDriver(NewCursorDriver()), WithProgramResolver(func(string) string { return "" }),
		WithDriverProgram(providerDriverCursor, "/opt/cursor/agent"))
	program, err = pinned.resolveRunProgram(context.Background(), "", params)
	if err != nil || program != "/opt/cursor/agent" {
		t.Fatalf("explicit Cursor pin = %q, %v, want /opt/cursor/agent", program, err)
	}
}
