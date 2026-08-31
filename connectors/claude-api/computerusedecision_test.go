// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"testing"
)

func TestIsComputerUseTool(t *testing.T) {
	tests := []struct {
		typeID string
		want   bool
	}{
		{"computer_20250124", true},
		{"computer_20241022", true},
		{"computer_20260301", true},
		{"web_search_20260209", false},
		{"code_execution_20260120", false},
		{"", false},
		{"computer", false},
		{"bash_20250124", false},
	}
	for _, tt := range tests {
		if got := IsComputerUseTool(tt.typeID); got != tt.want {
			t.Errorf("IsComputerUseTool(%q) = %v, want %v", tt.typeID, got, tt.want)
		}
	}
}

func TestHasComputerUseTool_Typed(t *testing.T) {
	tools := []any{
		ServerTool{Type: "web_search_20260209", Name: "web_search"},
		ServerTool{Type: "computer_20250124", Name: "computer"},
	}
	if !HasComputerUseTool(tools) {
		t.Error("expected true for tools containing computer_20250124")
	}
}

func TestHasComputerUseTool_Map(t *testing.T) {
	tools := []any{
		map[string]any{"type": "computer_20250124", "name": "computer"},
	}
	if !HasComputerUseTool(tools) {
		t.Error("expected true for map with computer_20250124")
	}
}

func TestHasComputerUseTool_None(t *testing.T) {
	tools := []any{
		ServerTool{Type: "web_search_20260209", Name: "web_search"},
	}
	if HasComputerUseTool(tools) {
		t.Error("expected false when no computer-use tool present")
	}
}

func TestHasComputerUseTool_Empty(t *testing.T) {
	if HasComputerUseTool(nil) {
		t.Error("expected false for nil tools")
	}
	if HasComputerUseTool([]any{}) {
		t.Error("expected false for empty tools")
	}
}

func TestHasComputerUseTool_Pointer(t *testing.T) {
	tools := []any{
		&ServerTool{Type: "computer_20241022", Name: "computer"},
	}
	if !HasComputerUseTool(tools) {
		t.Error("expected true for *ServerTool with computer_20241022")
	}
}

func TestExtractComputerUseActions(t *testing.T) {
	resp := MessageResponse{
		Content: []ContentBlock{
			blockFromJSONHelper(`{"type":"tool_use","name":"computer_20250124","id":"tu_1","input":{"action":"click","coordinate":[300,200]}}`),
			blockFromJSONHelper(`{"type":"tool_use","name":"computer_20250124","id":"tu_2","input":{"action":"type","text":"hello world"}}`),
			blockFromJSONHelper(`{"type":"tool_use","name":"computer_20250124","id":"tu_3","input":{"action":"screenshot"}}`),
			blockFromJSONHelper(`{"type":"tool_use","name":"computer_20250124","id":"tu_4","input":{"action":"scroll","scroll_direction":"down"}}`),
			blockFromJSONHelper(`{"type":"tool_use","name":"computer_20250124","id":"tu_5","input":{"action":"key","key":"Return"}}`),
			blockFromJSONHelper(`{"type":"text","text":"some text"}`),
		},
	}

	actions := ExtractComputerUseActions(resp)
	if len(actions) != 5 {
		t.Fatalf("expected 5 actions, got %d", len(actions))
	}

	if actions[0].Type != "click" {
		t.Errorf("action[0].Type = %q, want click", actions[0].Type)
	}
	if actions[0].Coordinate != [2]int{300, 200} {
		t.Errorf("action[0].Coordinate = %v, want [300,200]", actions[0].Coordinate)
	}

	if actions[1].Type != "type" {
		t.Errorf("action[1].Type = %q, want type", actions[1].Type)
	}
	if actions[1].Text != "hello world" {
		t.Errorf("action[1].Text = %q, want hello world", actions[1].Text)
	}

	if actions[2].Type != "screenshot" {
		t.Errorf("action[2].Type = %q, want screenshot", actions[2].Type)
	}

	if actions[3].Type != "scroll" {
		t.Errorf("action[3].Type = %q, want scroll", actions[3].Type)
	}
	if actions[3].Direction != "down" {
		t.Errorf("action[3].Direction = %q, want down", actions[3].Direction)
	}

	if actions[4].Type != "key" {
		t.Errorf("action[4].Type = %q, want key", actions[4].Type)
	}
	if actions[4].Key != "Return" {
		t.Errorf("action[4].Key = %q, want Return", actions[4].Key)
	}
}

func TestExtractComputerUseActions_NonComputerUse(t *testing.T) {
	resp := MessageResponse{
		Content: []ContentBlock{
			blockFromJSONHelper(`{"type":"tool_use","name":"web_search_20260209","id":"tu_1","input":{"query":"test"}}`),
			blockFromJSONHelper(`{"type":"text","text":"hello"}`),
		},
	}

	actions := ExtractComputerUseActions(resp)
	if len(actions) != 0 {
		t.Fatalf("expected 0 actions for non-computer-use tools, got %d", len(actions))
	}
}

func TestExtractComputerUseActions_Empty(t *testing.T) {
	resp := MessageResponse{}
	actions := ExtractComputerUseActions(resp)
	if len(actions) != 0 {
		t.Fatalf("expected 0 actions for empty response, got %d", len(actions))
	}
}

func blockFromJSONHelper(j string) ContentBlock {
	var b ContentBlock
	if err := b.UnmarshalJSON([]byte(j)); err != nil {
		panic("blockFromJSONHelper: " + err.Error())
	}
	return b
}
