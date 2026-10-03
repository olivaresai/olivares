// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// mcpTrustServer is a tested server with one tool its own hints call read-only and one
// with no hint. The engine's proposal is the read-only one.
func mcpTrustServer() map[string]any {
	return map[string]any{"id": "srv-docs", "name": "docs", "url": "https://mcp.example.com/mcp", "enabled": false,
		"trust": map[string]any{}, "egress_cidrs": []any{}, "allowed_tools": []any{},
		"probe": map[string]any{"state": "ok", "tools": []any{
			map[string]any{"name": "search", "read_only": true}, map[string]any{"name": "delete_page"}}},
		"proposed_allow": []any{"search"}}
}

// mcpSentPolicies is the allowed_tools the last write sent, as name -> destructive.
func mcpSentPolicies(t *testing.T, f *fakeMCPRoster) (map[string]bool, bool) {
	t.Helper()
	sent, _ := f.bodies[len(f.bodies)-1]["server"].(map[string]any)
	raw, set := sent["allowed_tools"]
	if !set {
		return nil, false
	}
	out := map[string]bool{}
	list, _ := raw.([]any)
	for _, p := range list {
		pm, _ := p.(map[string]any)
		if pm["required_scope"] != "tools:call" {
			t.Fatalf("policy %v has no tools:call scope", pm)
		}
		d, _ := pm["destructive"].(bool)
		out[pm["name"].(string)] = d
	}
	return out, true
}

// TestMCPEnableRunsNothingWithoutApprovalUnlessTheAdminSaysSo is Root's decision of
// 2026-10-02T01:42Z: a server's own annotations never grant "run without approval".
// `mcp enable` alone sends no list, so every tool asks; it prints the engine's proposal
// (the tools the server calls read-only) and how to accept it. --allow-proposed sends
// that proposal as an explicit list, --allow names the tools; every other tested tool
// asks.
func TestMCPEnableRunsNothingWithoutApprovalUnlessTheAdminSaysSo(t *testing.T) {
	f := newFakeMCPRoster(t)
	f.session = true
	f.servers = []map[string]any{mcpTrustServer()}

	out, errb, err := execSessionCLI(t, nil, mcpArgs(f.URL, "enable", "docs")...)
	if err != nil {
		t.Fatalf("enable: %v\n%s", err, errb)
	}
	if _, set := mcpSentPolicies(t, f); set {
		t.Fatal("enable alone sent a tool list; the engine's default (every tool asks) must apply")
	}
	for _, want := range []string{
		"docs is on.\n  Ask first: search, delete_page\n",
		"The server says these tools only read: search. To let them run without approval: olivares mcp enable docs --allow-proposed\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("enable alone: missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "Run without approval") {
		t.Fatalf("enable alone let a tool run without approval:\n%s", out)
	}

	out, errb, err = execSessionCLI(t, nil, mcpArgs(f.URL, "enable", "docs", "--allow-proposed")...)
	if err != nil {
		t.Fatalf("enable --allow-proposed: %v\n%s", err, errb)
	}
	if got, _ := mcpSentPolicies(t, f); len(got) != 2 || got["search"] != false || got["delete_page"] != true {
		t.Fatalf("--allow-proposed sent %v, want search allowed and delete_page asking", got)
	}
	if !strings.Contains(out, "docs is on.\n  Run without approval: search\n  Ask first: delete_page\n") ||
		strings.Contains(out, "--allow-proposed") {
		t.Fatalf("enable --allow-proposed =\n%s", out)
	}

	out, errb, err = execSessionCLI(t, nil, mcpArgs(f.URL, "enable", "docs", "--allow", "delete_page")...)
	if err != nil {
		t.Fatalf("enable --allow: %v\n%s", err, errb)
	}
	if got, _ := mcpSentPolicies(t, f); len(got) != 2 || got["search"] != true || got["delete_page"] != false {
		t.Fatalf("--allow delete_page sent %v, want delete_page allowed and search asking", got)
	}
	if !strings.Contains(out, "  Run without approval: delete_page\n  Ask first: search\n") {
		t.Fatalf("enable --allow =\n%s", out)
	}
}

// TestMCPEnableAllowRefusesWhatItCannotCheck: a tool name the test did not find, or a
// server that was never tested, is refused before anything is written.
func TestMCPEnableAllowRefusesWhatItCannotCheck(t *testing.T) {
	f := newFakeMCPRoster(t)
	f.servers = []map[string]any{mcpTrustServer()}
	_, _, err := execSessionCLI(t, nil, mcpArgs(f.URL, "enable", "docs", "--allow", "serch")...)
	want := `docs has no tool named "serch". Its tools: search, delete_page`
	if exitcode.From(err) != exitcode.Usage || err == nil || err.Error() != want {
		t.Fatalf("err = %v (exit %d), want %q", err, exitcode.From(err), want)
	}
	untested := mcpTrustServer()
	untested["probe"] = map[string]any{"state": "never_tested", "tools": []any{}}
	delete(untested, "proposed_allow")
	f.servers = []map[string]any{untested}
	_, _, err = execSessionCLI(t, nil, mcpArgs(f.URL, "enable", "docs", "--allow-proposed")...)
	want = "Test docs first, so its tools are known: olivares mcp test docs"
	if exitcode.From(err) != exitcode.Usage || err == nil || err.Error() != want {
		t.Fatalf("err = %v (exit %d), want %q", err, exitcode.From(err), want)
	}
	for _, b := range f.bodies {
		if _, write := b["server"]; write {
			t.Fatalf("a refused enable wrote %v", b)
		}
	}
}
