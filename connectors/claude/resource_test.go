// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestResourceFromToolKnownTools(t *testing.T) {
	cases := []struct {
		tool     string
		input    map[string]any
		wantKind string
		wantRef  string
		wantMode model.AccessMode
	}{
		{"Read", map[string]any{"file_path": "/home/u/a.go"}, resFile, "/home/u/a.go", model.ModeRead},
		{"Write", map[string]any{"file_path": "/home/u/b.go"}, resFile, "/home/u/b.go", model.ModeWrite},
		{"Edit", map[string]any{"file_path": "/x"}, resFile, "/x", model.ModeWrite},
		{"WebFetch", map[string]any{"url": "https://api.test/v1?token=abc"}, resHTTP, "https://api.test/v1", model.ModeRead},
		{"WebSearch", map[string]any{"query": "secret stuff"}, resWeb, resWeb, model.ModeRead},
		{"Bash", map[string]any{"command": "psql postgres://u:p@h/db -c 'select 1'"}, resShell, "psql", model.ModeUnknown},
		// Sub-agent delegation: the CURRENT "Agent" tool and the legacy "Task" both
		// carry subagent_type and resolve to an agent.task edge (so module IV can
		// classify the supervisor→worker delegation on current traffic).
		{"Agent", map[string]any{"subagent_type": "code-reviewer"}, resAgent, "code-reviewer", model.ModeUnknown},
		{"Task", map[string]any{"subagent_type": "researcher"}, resAgent, "researcher", model.ModeUnknown},
		{"mcp__github__create_issue", nil, resMCP, "github/create_issue", model.ModeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			kind, ref, mode := resourceFromTool(tc.tool, tc.input)
			if kind != tc.wantKind || ref != tc.wantRef || mode != tc.wantMode {
				t.Errorf("resourceFromTool(%q) = (%q,%q,%q), want (%q,%q,%q)",
					tc.tool, kind, ref, mode, tc.wantKind, tc.wantRef, tc.wantMode)
			}
		})
	}
}

func TestResourceFromToolNoDetailFallsBackToTool(t *testing.T) {
	// A Read with no tool_input (no hook, OTEL detail off) becomes a usage edge
	// against the tool itself, mode still inferred from the tool.
	kind, ref, mode := resourceFromTool("Read", nil)
	if kind != resTool || ref != "Read" || mode != model.ModeRead {
		t.Errorf("fallback = (%q,%q,%q)", kind, ref, mode)
	}
}

func TestResourceFromToolUnknownTool(t *testing.T) {
	kind, ref, mode := resourceFromTool("SomeFutureTool", map[string]any{"x": "y"})
	if kind != resTool || ref != "SomeFutureTool" || mode != model.ModeUnknown {
		t.Errorf("unknown tool = (%q,%q,%q)", kind, ref, mode)
	}
}

func TestResourceFromToolRedactsSecretInPath(t *testing.T) {
	_, ref, _ := resourceFromTool("Read", map[string]any{"file_path": "/tmp/AKIAIOSFODNN7EXAMPLE/data"})
	if strings.Contains(ref, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("secret survived in resource ref: %q", ref)
	}
}

func TestWebFetchDropsCredentials(t *testing.T) {
	_, ref, _ := resourceFromTool("WebFetch", map[string]any{"url": "https://user:pw@api.test/v1?api_key=zzz"})
	if strings.Contains(ref, "pw") || strings.Contains(ref, "zzz") || strings.Contains(ref, "api_key") {
		t.Errorf("WebFetch leaked credential: %q", ref)
	}
	if !strings.Contains(ref, "api.test") {
		t.Errorf("WebFetch dropped host: %q", ref)
	}
}

func TestShellProgramStripsArgsAndEnv(t *testing.T) {
	if got := shellProgram("FOO=bar BAZ=qux curl https://x.test"); got != "curl" {
		t.Errorf("shellProgram env+args = %q, want curl", got)
	}
	if got := shellProgram("  ls -la /etc "); got != "ls" {
		t.Errorf("shellProgram = %q, want ls", got)
	}
	if got := shellProgram(""); got != resShell {
		t.Errorf("empty command = %q", got)
	}
	// The full command (with a secret in an arg) must never survive.
	got := shellProgram("deploy --token=ghp_1234567890abcdefghijklmnopqrstuvwxyzAB")
	if strings.Contains(got, "ghp_1234567890") {
		t.Errorf("shellProgram leaked an arg secret: %q", got)
	}
}

func TestShellProgramEnvValueWithSlashOrSecret(t *testing.T) {
	// An env-assignment whose VALUE contains '/' (or a credential) must still be
	// skipped — the program, not the variable, is the resource, and no secret may
	// survive. This is the regression for the '!ContainsAny(f,"/")' bypass.
	cases := map[string]string{
		"DATABASE_URL=postgres://admin:s3cr3tpw@db.host/prod psql": "psql",
		"FOO=/path/to/thing ls":       "ls",
		"PATH=/usr/bin npm run build": "npm",
	}
	for cmd, wantProg := range cases {
		got := shellProgram(cmd)
		if got != wantProg {
			t.Errorf("shellProgram(%q) = %q, want %q", cmd, got, wantProg)
		}
	}
	// Even if a credential-bearing token is taken as the program, it is stripped.
	if got := shellProgram("postgres://admin:s3cr3tpw@db.host/prod"); strings.Contains(got, "s3cr3tpw") {
		t.Errorf("shellProgram leaked a credential: %q", got)
	}
}

func TestMCPResourceRefMalformed(t *testing.T) {
	if got := mcpResourceRef("mcp__onlyserver"); got != "onlyserver" {
		t.Errorf("malformed mcp name = %q", got)
	}
}
