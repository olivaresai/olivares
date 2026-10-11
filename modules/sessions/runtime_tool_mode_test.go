// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"encoding/json"
	"testing"
)

// The tool's own mode, as Claude Code 2.1.292 reports it (measured 2026-10-07 with
// `--permission-mode plan` and a `set_permission_mode` control request): the init
// frame names the mode the session started in, and a later system/status frame
// names the mode it changed to. The run records what the tool said, not what the
// launch asked for.
const (
	claudeInitPlanFrame = `{"type":"system","subtype":"init","cwd":"/w","session_id":"sess-mode",` +
		`"tools":["Bash"],"model":"claude-opus-5-5","permissionMode":"plan"}`
	claudeStatusAcceptEditsFrame = `{"type":"system","subtype":"status","status":null,"permissionMode":"acceptEdits"}`
)

func TestClaudeRunRecordsTheModeTheToolReports(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()

	// The launch names no mode, so it runs under "default": the tool's own answer
	// is the only place "plan" can come from.
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if dto.ToolMode != "" {
		t.Fatalf("tool_mode = %q before the tool reported one", dto.ToolMode)
	}

	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(claudeInitPlanFrame)}
	waitFor(t, "the mode from the init frame", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ToolMode == "plan"
	})
	if got, _ := m.getRun(ctx, tenant, dto.RunRef); got.PermissionMode != "default" || got.ClaudeSessionID != "sess-mode" {
		t.Fatalf("the requested mode or the session id moved: %+v", got)
	}

	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(claudeStatusAcceptEditsFrame)}
	waitFor(t, "the mode the tool changed to", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ToolMode == "acceptEdits"
	})

	// A status frame that names no mode (compacting) leaves it alone, and only a
	// system frame of the tool names it: an assistant frame carrying the word does not.
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"system","subtype":"status","status":"compacting"}`)}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"assistant","session_id":"sess-mode","permissionMode":"bypassPermissions"}`)}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"system","subtype":"api_retry","permissionMode":"bypassPermissions"}`)}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(resultFrame)}
	waitFor(t, "the frames after the mode change were read", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.CostMicroUSD != nil
	})
	if got, _ := m.getRun(ctx, tenant, dto.RunRef); got.ToolMode != "acceptEdits" {
		t.Fatalf("tool_mode = %q after frames that name no mode of the session, want acceptEdits", got.ToolMode)
	}
}

// Codex's answer can name both halves, one, or neither, and an approval policy in
// a shape this driver does not know is left out rather than guessed.
func TestCodexToolModeWords(t *testing.T) {
	for _, tc := range []struct{ approval, sandbox, want string }{
		{`"never"`, "dangerFullAccess", "never · dangerFullAccess"},
		{`"untrusted"`, "", "untrusted"},
		{``, "readOnly", "readOnly"},
		{``, "", ""},
		{`{"granular":{"rules":true}}`, "", "granular"},
		{`"ask me; then rm -rf /"`, "workspaceWrite", "workspaceWrite"},
		{`{"other":true}`, "readOnly", "readOnly"},
	} {
		r := codexThreadResponse{ApprovalPolicy: json.RawMessage(tc.approval)}
		r.Sandbox.Type = tc.sandbox
		if got := r.toolMode(); got != tc.want {
			t.Errorf("approval %s, sandbox %q: toolMode = %q, want %q", tc.approval, tc.sandbox, got, tc.want)
		}
	}
}

func TestToolModeValueKeepsOnlyAShortWord(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"plan":              "plan",
		"bypassPermissions": "bypassPermissions",
		"on-request":        "on-request",
		" plan ":            "plan",
		"":                  "",
		"plan; rm -rf /":    "",
		"line\nbreak":       "",
		"a-mode-name-far-too-long-to-be-a-mode-name": "",
	} {
		if got := toolModeValue(in); got != want {
			t.Errorf("toolModeValue(%q) = %q, want %q", in, got, want)
		}
	}
}

// Codex names its mode in its own thread answer. The launch asks for the default
// policy (untrusted, read-only); the peer answers with another one, and the run
// records Codex's answer.
func TestCodexRunRecordsTheModeCodexAnswers(t *testing.T) {
	for name, tc := range map[string]struct {
		approval string
		sandbox  string
		want     string
	}{
		"word":     {`"on-request"`, "workspaceWrite", "on-request · workspaceWrite"},
		"granular": {`{"granular":{"mcp_elicitations":true,"rules":true,"sandbox_approval":true}}`, "readOnly", "granular · readOnly"},
	} {
		t.Run(name, func(t *testing.T) {
			m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
			setCodexFixture(t, prof, codexFixture{
				ThreadID: "thread-mode-" + name, Account: "apikey",
				ApprovalPolicy: json.RawMessage(tc.approval), SandboxType: tc.sandbox,
			})
			dto, err := codexLaunch(t, m, tenant, prof)
			if err != nil {
				t.Fatalf("createRun: %v", err)
			}
			waitFor(t, "Codex's mode on the run", func() bool {
				d, _ := m.getRun(context.Background(), tenant, dto.RunRef)
				return d.ToolMode == tc.want
			})
		})
	}
}
