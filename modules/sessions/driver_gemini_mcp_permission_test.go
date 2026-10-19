// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// geminiSessionMCPToolRequest is the permission request Gemini CLI 0.62.0 sent, as
// recorded from the real CLI, for a call to a tool of the session MCP server named
// olivares: kind "other", no rawInput, no locations, and a toolCallId the CLI builds
// from the name it registered the tool under. The title is the CLI's description of
// the tool, built from the call's own arguments, and is never read.
func geminiSessionMCPToolRequest(sessionID, toolCallID, kind, title string) map[string]any {
	return map[string]any{
		"sessionId": sessionID,
		"options": []any{
			map[string]any{"optionId": "proceed_always_server", "name": "Allow for this server", "kind": acpPermissionAllowAlways},
			map[string]any{"optionId": "proceed_always_tool", "name": "Allow for this tool", "kind": acpPermissionAllowAlways},
			map[string]any{"optionId": "proceed_once", "name": "Allow once", "kind": acpPermissionAllowOnce},
			map[string]any{"optionId": "cancel", "name": "Reject", "kind": acpPermissionRejectOnce},
		},
		"toolCall": map[string]any{
			"toolCallId": toolCallID, "status": "pending", "kind": kind,
			"title": title, "content": []any{}, "locations": []any{},
		},
	}
}

const (
	geminiRealSessionMCPCallID = "mcp_olivares_whoami__mcp_olivares_whoami_1792146617881_0"
	geminiRealSessionMCPTitle  = "whoami (olivares MCP Server)"
)

// A call to a tool of the session MCP server carries no command or path, so the
// driver used to refuse it before the live policy saw it. With the session server the
// only one the CLI can start, the call id names it; that reaches the authority, which
// answers as it does for any other tool request, and the driver still grants once
// only. The CLI's title is "<tool>(key: value, ...)" for a call with arguments, which
// is what the session tools mostly take, so both shapes must pass.
func TestGeminiSessionMCPToolRequestReachesTheAuthority(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ id, title string }{
		"a tool with no argument": {geminiRealSessionMCPCallID, geminiRealSessionMCPTitle},
		"a tool with arguments":   {"mcp_olivares_list_runs__mcp_olivares_list_runs_1792146617881_1", "list_runs(limit: 5, mode: all)"},
		"a truncated tool name":   {"mcp_olivares_a_very_long_tool_name...ng_tool_name_ending__mcp_olivares_x_1_0", ""},
	} {
		var seen ProviderApprovalRequest
		asked := 0
		peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
			cfg.sessionMCP = sessionMCPFor(t, providerDriverGemini)
			cfg.ownMCPOnly = true
			cfg.Approve = func(_ context.Context, req ProviderApprovalRequest) (ProviderApprovalDecision, error) {
				asked++
				seen = req
				return ProviderApprovalDecision{Allow: true, Granted: []string{"proceed_once", "proceed_always_server", "proceed_always_tool"}}, nil
			}
		})
		if _, _, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_mcp"), nil); err != nil {
			t.Fatalf("%s: handshake: %v", name, err)
		}
		go func() { _, _ = peer.session.Input(context.Background(), "call the session tool") }()
		promptID, _, _ := peer.nextRequest()
		peer.requestFromServer("srv-1", acpReqRequestPermission, geminiSessionMCPToolRequest("ses_mcp", tc.id, "other", tc.title))
		if got := peer.permissionOutcome("srv-1"); got.Outcome != acpOutcomeSelected || got.OptionID != "proceed_once" {
			t.Fatalf("%s: outcome = %+v, want proceed_once and never an always option", name, got)
		}
		if asked != 1 || seen.Driver != providerDriverGemini || seen.Kind != acpKindToolCallPermission {
			t.Fatalf("%s: the authority was asked %d times with %+v", name, asked, seen)
		}
		if seen.FactsComplete || seen.CommandLine != "" || len(seen.FilePaths) != 0 {
			t.Fatalf("%s: the authority was told %+v: the request carries no command or path and must not claim one", name, seen)
		}
		peer.reply(promptID, map[string]any{"stopReason": "end_turn"})
	}
}

// Only a call to a tool of the server named olivares gets past the refusal. Every
// other request with no facts stays refused without asking.
func TestGeminiOnlyTheSessionMCPServersToolsSkipTheNoFactsRefusal(t *testing.T) {
	t.Parallel()
	const real, title = geminiRealSessionMCPCallID, geminiRealSessionMCPTitle
	for name, tc := range map[string]struct{ id, kind, title string }{
		"another server's tool":                 {"mcp_github_create_issue__mcp_github_create_issue_1_0", "other", "create_issue (github MCP Server)"},
		"a server whose name only starts so":    {"mcp_olivares-evil_whoami__mcp_olivares-evil_whoami_1_0", "other", "whoami (olivares-evil MCP Server)"},
		"a built-in tool the model named so":    {"run_shell_command__mcp_olivares_whoami_1_0", "other", title},
		"a built-in tool with a forged title":   {"run_shell_command__run_shell_command_1_0", "other", title},
		"no call id":                            {"", "other", title},
		"the shell kind":                        {real, "execute", title},
		"the edit kind":                         {real, "edit", title},
		"no kind":                               {real, "", title},
		"the olivares prefix without its tool":  {"mcp_olivares", "other", title},
		"the prefix after another server's one": {"mcp_github_mcp_olivares_whoami__x", "other", title},
	} {
		asked := 0
		peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
			cfg.sessionMCP = sessionMCPFor(t, providerDriverGemini)
			cfg.ownMCPOnly = true
			cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
				asked++
				return ProviderApprovalDecision{Allow: true, Granted: []string{"proceed_once"}}, nil
			}
		})
		if _, _, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_mcp_no"), nil); err != nil {
			t.Fatalf("%s: handshake: %v", name, err)
		}
		go func() { _, _ = peer.session.Input(context.Background(), "do it") }()
		peer.nextRequest()
		peer.requestFromServer("srv-1", acpReqRequestPermission, geminiSessionMCPToolRequest("ses_mcp_no", tc.id, tc.kind, tc.title))
		if got := peer.permissionOutcome("srv-1"); got.Outcome != acpOutcomeSelected || got.OptionID != "cancel" {
			t.Errorf("%s: outcome = %+v, want the one-shot rejection", name, got)
		}
		if asked != 0 {
			t.Errorf("%s reached the approval authority with nothing for it to review", name)
		}
	}
}

// The prefix names the session server only when it is the only server the CLI can
// start. A launch that could not prove that refuses an olivares-prefixed call, even
// one whose title spells the session tool exactly: the CLI builds the title of an MCP
// call from the call's own "command" argument when it has one, so a settings server
// named olivares_x (or olivares/x, which the CLI registers as olivares_x) can send exactly that.
// The same holds for a launch that configured no session server at all.
func TestGeminiAnOlivaresPrefixedCallIsRefusedWhenOurServerIsNotTheOnlyOne(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		sessionMCP, ownMCPOnly bool
		id, title              string
	}{
		"the real call, own-only not proved": {true, false, geminiRealSessionMCPCallID, geminiRealSessionMCPTitle},
		"a forged title on olivares_x":       {true, false, "mcp_olivares_x_whoami__mcp_olivares_x_whoami_1_0", "x_whoami (olivares MCP Server)"},
		"no session MCP server configured":   {false, false, geminiRealSessionMCPCallID, geminiRealSessionMCPTitle},
	} {
		asked := 0
		peer := newGeminiPeer(t, func(cfg *DriverSessionConfig) {
			if tc.sessionMCP {
				cfg.sessionMCP = sessionMCPFor(t, providerDriverGemini)
			}
			cfg.ownMCPOnly = tc.ownMCPOnly
			cfg.Approve = func(context.Context, ProviderApprovalRequest) (ProviderApprovalDecision, error) {
				asked++
				return ProviderApprovalDecision{Allow: true, Granted: []string{"proceed_once"}}, nil
			}
		})
		if _, _, err := peer.answerGemini(geminiInitializeResult(), geminiSessionResultFor("ses_mcp_none"), nil); err != nil {
			t.Fatalf("%s: handshake: %v", name, err)
		}
		go func() { _, _ = peer.session.Input(context.Background(), "do it") }()
		peer.nextRequest()
		peer.requestFromServer("srv-1", acpReqRequestPermission, geminiSessionMCPToolRequest("ses_mcp_none", tc.id, "other", tc.title))
		if got := peer.permissionOutcome("srv-1"); got.Outcome != acpOutcomeSelected || got.OptionID != "cancel" {
			t.Fatalf("%s: outcome = %+v, want the one-shot rejection", name, got)
		}
		if asked != 0 {
			t.Fatalf("%s: a call to a tool of a server that is not provably ours reached the authority", name)
		}
	}
}

// launchOwnMCPOnly proves the session server is the only one the CLI can start, from
// every place the CLI 0.62.0 reads a server from in a folder: the profile's and the
// workspace's settings, an extension, and a subagent definition. What it cannot read,
// parse or classify is not proved.
func TestLaunchOwnMCPOnlyNeedsEverySourceOfAServerAbsent(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		home, workspace map[string]string
		want            bool
	}{
		"no file anywhere":                   {nil, nil, true},
		"empty settings":                     {map[string]string{"settings.json": `{}`}, map[string]string{"settings.json": `{}`}, true},
		"settings that name no server":       {map[string]string{"settings.json": `{"ui":{"theme":"x"},"mcpServers":{},"mcp":{"serverCommand":""}}`}, nil, true},
		"null mcpServers":                    {map[string]string{"settings.json": `{"mcpServers":null}`}, nil, true},
		"a server in the profile's settings": {map[string]string{"settings.json": `{"mcpServers":{"olivares_x":{"command":"x"}}}`}, nil, false},
		"a server named olivares there":      {map[string]string{"settings.json": `{"mcpServers":{"olivares":{"url":"http://x"}}}`}, nil, false},
		"a server in the workspace":          {nil, map[string]string{"settings.json": `{"mcpServers":{"olivares_x":{"command":"x"}}}`}, false},
		"mcp.serverCommand in the profile":   {map[string]string{"settings.json": `{"mcp":{"serverCommand":"x"}}`}, nil, false},
		"mcp.serverCommand in the workspace": {nil, map[string]string{"settings.json": `{"mcp":{"serverCommand":"x"}}`}, false},
		"an extension in the profile":        {map[string]string{"extensions/e/gemini-extension.json": `{}`}, nil, false},
		"an extension in the workspace":      {nil, map[string]string{"extensions/e/gemini-extension.json": `{}`}, false},
		"an agent in the profile":            {map[string]string{"agents/a.md": "---\nname: a\n---\n"}, nil, false},
		"an agent in the workspace":          {nil, map[string]string{"agents/a.md": "---\nname: a\n---\n"}, false},
		"settings with comments":             {nil, map[string]string{"settings.json": "{\n // x\n \"ui\": {}\n}"}, false},
		"settings that are not an object":    {map[string]string{"settings.json": `[]`}, nil, false},
		"mcpServers that is not an object":   {map[string]string{"settings.json": `{"mcpServers":[1]}`}, nil, false},
		"settings that are a directory":      {map[string]string{"settings.json/x": ``}, nil, false},
		"an empty settings file":             {map[string]string{"settings.json": ``}, nil, false},
		"a top-level null":                   {nil, map[string]string{"settings.json": `null`}, false},
		"settings over the size limit":       {map[string]string{"settings.json": `{"ui":"` + strings.Repeat("x", geminiSettingsMaxBytes) + `"}`}, nil, false},
		"a dotfile in extensions":            {map[string]string{"extensions/.keep": ""}, nil, false},
		"extensions that is a file":          {map[string]string{"extensions": `x`}, nil, false},
		"agents that is a file":              {nil, map[string]string{"agents": `x`}, false},
		"the server key spelled with \\u":    {map[string]string{"settings.json": `{"\u006dcpServers":{"a":{"command":"x"}}}`}, nil, false},
		"a repeated key, the last is set":    {map[string]string{"settings.json": `{"mcpServers":{},"mcpServers":{"a":{"command":"x"}}}`}, nil, false},
		"a repeated key, the last is empty":  {map[string]string{"settings.json": `{"mcpServers":{"a":{"command":"x"}},"mcpServers":{}}`}, nil, true},
		"a built-in agent override server":   {nil, map[string]string{"settings.json": `{"agents":{"overrides":{"generalist":{"mcpServers":{"olivares_x":{"command":"x"}}}}}}`}, false},
		"agent overrides with no server":     {nil, map[string]string{"settings.json": `{"agents":{"overrides":{"generalist":{"enabled":true}}}}`}, true},
		"agent overrides that are a list":    {nil, map[string]string{"settings.json": `{"agents":{"overrides":[1]}}`}, false},
		"a differently cased key":            {map[string]string{"settings.json": `{"MCPSERVERS":{"a":{"command":"x"}}}`}, nil, true},
	} {
		home := geminiTestHome(t, tc.home)
		workDir := t.TempDir()
		for rel, content := range tc.workspace {
			path := filepath.Join(workDir, geminiConfigDirName, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got := launchOwnMCPOnly(providerDriverGemini, home, workDir); got != tc.want {
			t.Errorf("%s: launchOwnMCPOnly = %v, want %v", name, got, tc.want)
		}
	}
	home := geminiTestHome(t, nil)
	padded := t.TempDir()
	if err := os.MkdirAll(filepath.Join(padded, geminiConfigDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(padded, geminiConfigDirName, "settings.json"), []byte(`{"mcpServers":{"a":{"command":"x"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if launchOwnMCPOnly(providerDriverGemini, home, " "+padded+"\n") {
		t.Error("a workspace directory with surrounding white space was checked at another path than the CLI reads")
	}
	for _, sub := range []string{"extensions", "agents"} {
		if err := os.Mkdir(filepath.Join(home.ConfigHome, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if !launchOwnMCPOnly(providerDriverGemini, home, filepath.Dir(home.ConfigHome)) {
		t.Error("empty extensions and agents directories, with the profile's .gemini as the workspace's too, were not proved")
	}
	if launchOwnMCPOnly(providerDriverOpenCode, home, t.TempDir()) || launchOwnMCPOnly(providerDriverGemini, nil, t.TempDir()) ||
		launchOwnMCPOnly(providerDriverGemini, &ProviderHomeSnapshot{}, t.TempDir()) {
		t.Error("a driver that is not Gemini CLI, or a launch with no configuration home, was proved")
	}
}

// Gemini CLI starts no MCP server in a folder it does not trust and says nothing,
// so the run says why its session tools can be missing and what trusting costs. The
// other drivers keep the one sentence they had, and a driver whose MCP configuration
// Olivares owns says nothing.
func TestMCPGovernanceWarningNamesGeminiFolderTrust(t *testing.T) {
	t.Parallel()
	const base = "MCP servers configured in this tool's own settings are not governed by Olivares."
	for _, driver := range []string{providerDriverGrok, providerDriverOpenCode, providerDriverGemini} {
		got := mcpGovernanceWarning(driver)
		if !strings.HasPrefix(got, base) {
			t.Errorf("%s warning = %q, must keep the sentence clients already show", driver, got)
		}
		trustSentence := strings.Contains(got, "does not trust") && strings.Contains(got, "trustedFolders.json") &&
			strings.Contains(got, "an MCP server, an extension or an agent") && strings.Contains(got, "refused")
		if (driver == providerDriverGemini) != trustSentence {
			t.Errorf("%s warning = %q: the folder trust sentence belongs to Gemini CLI only", driver, got)
		}
	}
	for _, driver := range []string{"", providerDriverClaude, providerDriverCodex} {
		if got := mcpGovernanceWarning(driver); got != "" {
			t.Errorf("driver %q warning = %q, want none", driver, got)
		}
	}
}

// The runtime hands the driver the proof from the launch it is opening, not from a
// default: a Gemini launch whose workspace holds an MCP server gets ownMCPOnly false,
// a clean one true, and no other driver gets it.
func TestOpenDriverSessionProvesOwnMCPOnlyFromTheLaunch(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		driver    string
		workspace string
		mcpURL    string
		want      bool
	}{
		"a clean Gemini launch":                     {providerDriverGemini, "", "http://127.0.0.1:1/session/mcp", true},
		"a Gemini launch with a workspace server":   {providerDriverGemini, `{"mcpServers":{"olivares_x":{"command":"x"}}}`, "http://127.0.0.1:1/session/mcp", false},
		"a Gemini launch with no session MCP":       {providerDriverGemini, "", "", false},
		"another driver with a clean configuration": {providerDriverOpenCode, "", "http://127.0.0.1:1/session/mcp", false},
	} {
		home := geminiTestHome(t, nil)
		home.Driver = tc.driver
		workDir := t.TempDir()
		if tc.workspace != "" {
			if err := os.MkdirAll(filepath.Join(workDir, geminiConfigDirName), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workDir, geminiConfigDirName, "settings.json"), []byte(tc.workspace), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		spec := LaunchSpec{Dir: workDir, SessionMCPURL: tc.mcpURL, SessionMCPTokenEnv: envWorkToken, Env: []EnvVar{{Name: envWorkToken, Value: "t"}}}
		session := newFakeTurnSession()
		driver := fakeTurnDriver{session: session}
		m := New(WithProviderDriver(driver))
		m.openDriverSession(&liveRun{runRef: "run-own-mcp"}, driver, CreateRunParams{ProviderHome: home}, spec, "")
		if got := session.cfg.ownMCPOnly; got != tc.want {
			t.Errorf("%s: ownMCPOnly = %v, want %v", name, got, tc.want)
		}
	}
}
