// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"regexp"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Resource kinds emitted by this connector (the ResourceKind of an
// EdgeObservation). They are open strings the engine resolves to Resource
// entities. They mirror the claude connector's vocabulary so the inventory/
// access-map modules treat a Cowork file/MCP/shell access identically to a Claude
// Code one — the fallback kind is "cowork.tool" so a Cowork-specific tool (or a
// skill invocation, which Cowork surfaces as a tool_result) is still inventoried
// by name rather than dropped.
const (
	resFile      = "file"        // a filesystem path (workspace.host_paths file)
	resShell     = "shell"       // a shell invocation (program only; args are never stored)
	resHTTP      = "http.url"    // a web endpoint (host+path, no query/credentials)
	resWeb       = "web.search"  // a web search (the query is not stored)
	resMCP       = "mcp.tool"    // an observed MCP connector tool invocation
	resMCPServer = "mcp.server"  // an MCP server/connector a session used (topology)
	resAgent     = "agent.task"  // a delegated subagent/dispatch task
	resTool      = "cowork.tool" // fallback: the tool/skill itself, when no resource detail is known
)

// mcpToolPrefix is the Claude tool naming prefix for an MCP connector tool
// (mcp__<server>__<tool>); Cowork's MCP connector calls carry the same shape.
const mcpToolPrefix = "mcp__"

// toolSpec describes how to turn one tool's input into a resource reference and
// what read/write mode the tool implies. field is the tool-input key holding the
// resource; sanitize cleans that raw value into a safe ref.
type toolSpec struct {
	kind     string
	mode     model.AccessMode
	field    string
	sanitize func(string) string
}

// sanitizePath scrubs a filesystem path for an embedded secret while keeping its
// structure (paths are resources, not secrets, but may contain a token).
func sanitizePath(s string) string { out, _ := redact.Scrub(s); return out }

// toolRegistry maps a known built-in tool to how its access is classified. Tools
// absent here (a Cowork-specific tool, a skill) fall back to a generic,
// mode-unknown tool-usage edge (resourceFromTool), so an unrecognized tool is
// still inventoried rather than dropped. The mapping matches the claude connector
// so a file write or MCP call reads the same regardless of which Anthropic agent
// performed it.
var toolRegistry = map[string]toolSpec{
	"Read":         {resFile, model.ModeRead, "file_path", sanitizePath},
	"Glob":         {resFile, model.ModeRead, "path", sanitizePath},
	"Grep":         {resFile, model.ModeRead, "path", sanitizePath},
	"LS":           {resFile, model.ModeRead, "path", sanitizePath},
	"NotebookRead": {resFile, model.ModeRead, "notebook_path", sanitizePath},
	"Write":        {resFile, model.ModeWrite, "file_path", sanitizePath},
	"Edit":         {resFile, model.ModeWrite, "file_path", sanitizePath},
	"MultiEdit":    {resFile, model.ModeWrite, "file_path", sanitizePath},
	"NotebookEdit": {resFile, model.ModeWrite, "notebook_path", sanitizePath},
	"WebFetch":     {resHTTP, model.ModeRead, "url", redact.SanitizeURL},
	"WebSearch":    {resWeb, model.ModeRead, "", nil},
	"Bash":         {resShell, model.ModeUnknown, "command", shellProgram},
	"Agent":        {resAgent, model.ModeUnknown, "subagent_type", sanitizePath},
	"Task":         {resAgent, model.ModeUnknown, "subagent_type", sanitizePath},
}

// resourceFromTool derives the redacted resource (kind, ref) and the inferred R/RW
// mode for a tool invocation. input is the untrusted tool input from the
// tool_result event; it may be nil when no detail is available, in which case the
// resource falls back to the tool itself (a usage edge), keeping confidence honest
// (ARCHITECTURE.md). Cowork always sends tool detail, but the connector still degrades
// gracefully if a record omits it.
func resourceFromTool(toolName string, input map[string]any) (kind, ref string, mode model.AccessMode) {
	if strings.HasPrefix(toolName, mcpToolPrefix) {
		return resMCP, mcpResourceRef(toolName), model.ModeUnknown
	}

	spec, known := toolRegistry[toolName]
	if !known {
		// A Cowork-specific tool or a skill invocation: inventory it by name so the
		// session's activity is captured, with an honest unknown mode.
		return resTool, toolName, model.ModeUnknown
	}

	// WebSearch and any spec without an input field have no per-call resource beyond
	// the tool itself; the query/content is deliberately not stored.
	if spec.field == "" {
		return spec.kind, spec.kind, spec.mode
	}

	raw := inputString(input, spec.field)
	if raw == "" {
		return resTool, toolName, spec.mode
	}
	if spec.sanitize != nil {
		raw = spec.sanitize(raw)
	}
	return spec.kind, raw, spec.mode
}

// mcpResourceRef converts an MCP tool name (mcp__server__tool) into a
// "server/tool" reference that aligns with the mcp connector's capability edges,
// so the access map can diff observed use against declared capability. A malformed
// name is returned with its prefix stripped.
func mcpResourceRef(toolName string) string {
	rest := strings.TrimPrefix(toolName, mcpToolPrefix)
	if server, tool, ok := strings.Cut(rest, "__"); ok {
		return server + "/" + tool
	}
	return rest
}

// envAssignRe matches a leading shell env-assignment token (FOO=...), identified
// by the NAME shape alone so the value never defeats the skip.
var envAssignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// shellProgram extracts the program name from a shell command (the first bare
// token after any env-assignments), discarding all arguments — which can carry
// secrets. The chosen token is run through SanitizeURL so even a credential-bearing
// token is stripped of its userinfo/query before becoming a resource ref.
func shellProgram(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return resShell
	}
	prog := resShell
	for _, f := range strings.Fields(command) {
		if envAssignRe.MatchString(f) {
			continue
		}
		prog = f
		break
	}
	return redact.SanitizeURL(prog)
}

// inputString returns input[key] as a string when it is a string-shaped value,
// else "". It tolerates a missing key or a non-string value.
func inputString(input map[string]any, key string) string {
	if input == nil {
		return ""
	}
	v, ok := input[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// isHighRiskMode reports whether an access mode is one that MUTATES state — the
// risk dimension the auto-approval finding gates on. A write or readwrite is
// high-risk; a read is not. Unknown is treated as high-risk ONLY for a shell (see
// isHighRiskTool) — an unknown-mode MCP/usage edge is not assumed dangerous.
func isHighRiskMode(mode model.AccessMode) bool {
	return mode == model.ModeWrite || mode == model.ModeReadWrite
}

// isHighRiskTool reports whether an executed tool is HIGH-RISK for the purpose of
// the auto-approved-action finding: it mutates the host (write/readwrite mode) OR
// it is a shell (Bash — its R/RW is unknowable from the cooperative path, so an
// auto-approved shell is treated as high-risk by construction, the conservative
// stance for an un-gated command execution). A read or a benign usage edge is not.
func isHighRiskTool(kind string, mode model.AccessMode) bool {
	if kind == resShell {
		return true
	}
	return isHighRiskMode(mode)
}
