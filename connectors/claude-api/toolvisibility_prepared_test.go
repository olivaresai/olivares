// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import "testing"

func TestPreparedToolVisibilityUsesFrozenDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		tools                []any
		programmatic, search bool
	}{
		{"ordinary", nil, false, false},
		{"direct", []any{map[string]any{"name": "fixture_tool", "allowed_callers": []string{"direct"}}}, false, false},
		{"programmatic", []any{map[string]any{"name": "fixture_tool", "allowed_callers": []string{"code_execution_20260120"}}}, true, false},
		{"programmatic_latest", []any{map[string]any{"name": "fixture_tool", "allowed_callers": []string{"direct", "code_execution_20260521"}}}, true, false},
		{"search", []any{map[string]any{"type": "tool_search_tool_regex_20251119", "name": "tool_search_tool_regex"}}, false, true},
		{"deferred", []any{map[string]any{"name": "fixture_tool", "defer_loading": true}}, false, true},
		{"not_deferred", []any{map[string]any{"name": "fixture_tool", "defer_loading": false}}, false, false},
		{"mcp_default_deferred", []any{map[string]any{"type": "mcp_toolset", "default_config": map[string]any{"defer_loading": true}}}, false, true},
		{"computer_members_deferred", []any{map[string]any{"type": "computer_toolset_20260801", "configs": map[string]any{"screenshot": map[string]any{"defer_loading": true}}}}, false, true},
		{"browser_members_deferred", []any{map[string]any{"type": "browser_toolset_20260801", "configs": map[string]any{"javascript_exec": map[string]any{"defer_loading": true}}}}, false, true},
		{"mcp_one_deferred", []any{map[string]any{"type": "mcp_toolset", "configs": map[string]any{"fixture_tool": map[string]any{"defer_loading": true}}}}, false, true},
		{"mcp_disabled_default_overridden", []any{map[string]any{"type": "mcp_toolset", "default_config": map[string]any{"enabled": false, "defer_loading": true}, "configs": map[string]any{"fixture_tool": map[string]any{"enabled": true, "defer_loading": false}}}}, false, false},
		{"mcp_disabled_member", []any{map[string]any{"type": "mcp_toolset", "configs": map[string]any{"fixture_tool": map[string]any{"enabled": false, "defer_loading": true}}}}, false, false},
		{"mcp_enabled_member_inherits_deferred", []any{map[string]any{"type": "mcp_toolset", "default_config": map[string]any{"enabled": false, "defer_loading": true}, "configs": map[string]any{"fixture_tool": map[string]any{"enabled": true}}}}, false, true},
		{"computer_disabled_member", []any{map[string]any{"type": "computer_toolset_20260801", "configs": map[string]any{"screenshot": map[string]any{"enabled": false, "defer_loading": true}}}}, false, false},
		{"browser_disabled_member", []any{map[string]any{"type": "browser_toolset_20260801", "configs": map[string]any{"javascript_exec": map[string]any{"enabled": false, "defer_loading": true}}}}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepared, err := MarshalPrepared(MessageRequest{Tools: tc.tools})
			if err != nil {
				t.Fatal(err)
			}
			got := prepared.ToolVisibilityFeatures()
			if got.ProgrammaticToolCalling != tc.programmatic || got.ToolSearchActive != tc.search {
				t.Fatalf("features = %+v, want programmatic=%t search=%t", got, tc.programmatic, tc.search)
			}
		})
	}
	tool := map[string]any{"name": "fixture_tool", "defer_loading": true}
	prepared, err := MarshalPrepared(MessageRequest{Tools: []any{tool}})
	if err != nil {
		t.Fatal(err)
	}
	tool["defer_loading"] = false
	if !prepared.ToolVisibilityFeatures().ToolSearchActive {
		t.Fatal("caller mutation changed frozen visibility")
	}
}

func TestPreparedBatchToolVisibilityIncludesLaterEntries(t *testing.T) {
	prepared, err := MarshalPreparedBatch([]BatchRequest{
		{CustomID: "ordinary", Params: MessageRequest{}},
		{CustomID: "programmatic", Params: MessageRequest{Tools: []any{map[string]any{"allowed_callers": []string{"code_execution_20260521"}}}}},
		{CustomID: "search", Params: MessageRequest{Tools: []any{map[string]any{"defer_loading": true}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := prepared.ToolVisibilityFeatures()
	if !got.ProgrammaticToolCalling || !got.ToolSearchActive {
		t.Fatalf("batch dropped a later entry's coverage caveat: %+v", got)
	}
}
