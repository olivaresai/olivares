// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// computerusedecision.go defines the pure DATA envelope the inline-proxy decider
// (cmd/olivares, AGPL) and the OPTIONAL commercial computer-use governance gate
// (enterprise/computeruse, closed) exchange. It carries NO policy: the action-level
// rules, the OCR/DLP depth and the deny-closed decision all live in the gate
// implementation. This connector only defines the SHAPE the two sides agree on, so
// neither imports the other's package — exactly as egressdecision.go does for the
// server-tool egress gate and inspectiondecision.go for the content firewall.
//
// The zero value is a DENY (Forward=false): a gate that returns a zero value on a
// path it forgot fails closed. Minimal data (docs/SECURITY-HARDENING.md): nothing here carries a
// screenshot, a prompt, a response body, or a typed password — only action types,
// coordinate hashes, and identifiers.
//
// Computer-use tool types (Anthropic docs, 2025-2026):
//   - computer_20241022 (legacy beta)
//   - computer_20250124 (current)
//
// These are CLIENT-SIDE tools: the model proposes an action in a tool_use response
// block (screenshot, click, type, scroll, key, mouse_move), and the CLIENT executes
// it locally. Anthropic does NOT server-execute these (unlike web_search/code_execution).
// The PEP intercepts the tool declaration in req.Tools (pre-forward) and the proposed
// actions in the response tool_use blocks (post-forward).
package claudeapi

import (
	"encoding/json"
	"strings"
)

// computerUseToolFamily is the version-independent family root for computer-use
// tools. The Anthropic naming is "computer_<YYYYMMDD>" (the root is "computer",
// not "computer_use" — unlike web_search/code_execution which include the full
// family name before the date suffix).
const computerUseToolFamily = "computer"

// ComputerUseToolTypes is the set of recognized computer-use tool type identifiers.
var ComputerUseToolTypes = map[string]bool{
	"computer_20241022": true,
	"computer_20250124": true,
}

// IsComputerUseTool reports whether a tool type identifier is a recognized
// computer-use tool. It checks the known set first, then derives the family from the
// canonical "<family>_<YYYYMMDD>" shape for forward compatibility (a new dated version
// of the computer_use family still matches). Computer-use tools are CLIENT-SIDE (not
// in knownServerToolFamilies), so the family check is local to this function.
func IsComputerUseTool(typeID string) bool {
	if ComputerUseToolTypes[typeID] {
		return true
	}
	if i := strings.LastIndexByte(typeID, '_'); i > 0 {
		suffix := typeID[i+1:]
		if len(suffix) == 8 && isAllDigits(suffix) {
			root := typeID[:i]
			return root == computerUseToolFamily
		}
	}
	return false
}

// HasComputerUseTool scans a request's Tools[] for any computer-use tool declaration.
// It handles both typed ServerTool and map[string]any (hand-built).
func HasComputerUseTool(tools []any) bool {
	for _, t := range tools {
		switch v := t.(type) {
		case ServerTool:
			if IsComputerUseTool(v.Type) {
				return true
			}
		case *ServerTool:
			if v != nil && IsComputerUseTool(v.Type) {
				return true
			}
		case map[string]any:
			if ty, _ := v["type"].(string); IsComputerUseTool(ty) {
				return true
			}
		}
	}
	return false
}

// ComputerUseInput is the minimal-data input the decider hands the computer-use gate.
type ComputerUseInput struct {
	Tenant   string // resolved tenant key
	ActorRef string // resolved acting agent ref
	Tools    []any  // the request's tools[] containing computer-use declarations
}

// ComputerUseDecision is the gate's verdict for one inbound request that declares a
// computer-use tool. The zero value (Forward=false) is a DENY.
type ComputerUseDecision struct {
	Forward bool
	// Deny mapping (used only when Forward is false).
	Status    int
	ErrorType string
	Reason    string
	// Findings the decider should publish on the bus.
	Findings []ComputerUseFinding
	// ApprovalIntent for a denied computer-use action.
	ApprovalIntent *ComputerUseApprovalIntent
}

// ComputerUseApprovalIntent is the minimal-data request to open a governed approval
// for denied computer-use access.
type ComputerUseApprovalIntent struct {
	Action   string
	ToolType string
	Subject  string
	Reason   string
	PlanHash string
}

// ComputerUseFinding is a posture/forensic observation from the computer-use gate.
type ComputerUseFinding struct {
	Kind       string
	Severity   string // "high" | "medium" | "info"
	Title      string
	ActionType string // screenshot | click | type | scroll | key | mouse_move
	Detail     string // non-sensitive context, hashed by the decider
	OWASPLLM   []string
}

// ComputerUseAction is one parsed action from a model response tool_use block with
// a computer-use tool. The model proposes these; the client executes them.
type ComputerUseAction struct {
	Type       string // screenshot | click | type | scroll | key | mouse_move | drag | triple_click | double_click
	Coordinate [2]int // [x, y] for click/type/drag
	Text       string // typed text (for type action) — the decider HASHES this, never stores raw
	Key        string // key name (for key action)
	Direction  string // scroll direction (up/down/left/right)
	StartCoord [2]int // drag start
	EndCoord   [2]int // drag end
}

// ExtractComputerUseActions parses computer-use actions from a model response's
// content blocks (tool_use blocks with a computer-use tool type). It accesses the
// unexported raw field on ContentBlock (same package), which UnmarshalJSON stashes.
// Returns nil if no computer-use actions are found.
func ExtractComputerUseActions(resp MessageResponse) []ComputerUseAction {
	var actions []ComputerUseAction
	for _, block := range resp.Content {
		if block.Type != "tool_use" {
			continue
		}
		if len(block.raw) == 0 {
			continue
		}

		var tu struct {
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}
		if err := json.Unmarshal(block.raw, &tu); err != nil {
			continue
		}
		if !IsComputerUseTool(tu.Name) {
			continue
		}

		action, _ := tu.Input["action"].(string)
		if action == "" {
			continue
		}

		ca := ComputerUseAction{Type: action}

		if coord, ok := tu.Input["coordinate"].([]any); ok && len(coord) >= 2 {
			if x, ok := coord[0].(float64); ok {
				ca.Coordinate[0] = int(x)
			}
			if y, ok := coord[1].(float64); ok {
				ca.Coordinate[1] = int(y)
			}
		}

		if text, ok := tu.Input["text"].(string); ok {
			ca.Text = text
		}
		if key, ok := tu.Input["key"].(string); ok {
			ca.Key = key
		}
		if dir, ok := tu.Input["scroll_direction"].(string); ok {
			ca.Direction = dir
		}

		actions = append(actions, ca)
	}
	return actions
}
