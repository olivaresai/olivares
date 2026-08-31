// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"regexp"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Resource kinds emitted by this connector (the ResourceKind of an
// EdgeObservation). They are open strings the engine resolves to Resource
// entities; documented in the contract for modules I/III.
const (
	resFile  = "file"        // a filesystem path
	resShell = "shell"       // a shell invocation (program only; args are never stored)
	resHTTP  = "http.url"    // a web endpoint (host+path, no query/credentials)
	resWeb   = "web.search"  // a web search (the query is not stored)
	resMCP   = "mcp.tool"    // an observed MCP tool invocation
	resAgent = "agent.task"  // a delegated subagent task
	resTool  = "claude.tool" // fallback: the tool itself, when no resource detail is known
)

// mcpToolPrefix is the Claude Code naming prefix for an MCP tool
// (mcp__<server>__<tool>).
const mcpToolPrefix = "mcp__"

// toolSpec describes how to turn one Claude Code tool's input into a resource
// reference and what read/write mode the tool implies. field is the tool-input
// key holding the resource; sanitize cleans that raw value into a safe ref.
type toolSpec struct {
	kind     string
	mode     model.AccessMode
	field    string
	sanitize func(string) string
}

// sanitizePath scrubs a filesystem path for an embedded secret while keeping its
// structure (paths are resources, not secrets, but may contain a token).
func sanitizePath(s string) string { out, _ := redact.Scrub(s); return out }

// toolRegistry maps a known Claude Code built-in tool to how its access is
// classified. Tools absent here fall back to a generic, mode-unknown tool-usage
// edge (resourceFromTool), so an unrecognized or future tool is still inventoried
// rather than dropped.
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
	// Sub-agent delegation: "Agent" is the CURRENT Claude Code tool, "Task" the legacy
	// name — both carry the same subagent_type (verified vs code.claude.com/docs/en/
	// monitoring-usage, jun-2026). Mapping both to agent.task is what makes the
	// supervisor→worker delegation observable on current traffic (module IV classifies
	// the agent.task edge); knowing only "Task" mis-filed current delegations as a
	// generic claude.tool usage edge.
	"Agent": {resAgent, model.ModeUnknown, "subagent_type", sanitizePath},
	"Task":  {resAgent, model.ModeUnknown, "subagent_type", sanitizePath},
}

// resourceFromTool derives the redacted resource (kind, ref) and the inferred
// R/RW mode for a tool invocation. input is the untrusted tool input from a hook
// or from OTEL detail; it may be nil when no detail is available, in which case
// the resource falls back to the tool itself (a usage edge), keeping the
// product's confidence honest (ARCHITECTURE.md).
func resourceFromTool(toolName string, input map[string]any) (kind, ref string, mode model.AccessMode) {
	if strings.HasPrefix(toolName, mcpToolPrefix) {
		return resMCP, mcpResourceRef(toolName), model.ModeUnknown
	}

	spec, known := toolRegistry[toolName]
	if !known {
		return resTool, toolName, model.ModeUnknown
	}

	// WebSearch and any spec without an input field have no per-call resource
	// beyond the tool itself; the query/content is deliberately not stored.
	if spec.field == "" {
		return spec.kind, spec.kind, spec.mode
	}

	raw := inputString(input, spec.field)
	if raw == "" {
		// No detail (no hook, OTEL_LOG_TOOL_DETAILS off): emit a usage edge against
		// the tool so the session's activity is still inventoried.
		return resTool, toolName, spec.mode
	}
	if spec.sanitize != nil {
		raw = spec.sanitize(raw)
	}
	return spec.kind, raw, spec.mode
}

// mcpResourceRef converts a Claude Code MCP tool name (mcp__server__tool) into a
// "server/tool" reference that aligns with the mcp connector's capability edges,
// so module III can diff the observed use against the declared (UNTRUSTED)
// capability. A malformed name is returned with its prefix stripped.
func mcpResourceRef(toolName string) string {
	rest := strings.TrimPrefix(toolName, mcpToolPrefix)
	if server, tool, ok := strings.Cut(rest, "__"); ok {
		return server + "/" + tool
	}
	return rest
}

// envAssignRe matches a leading shell env-assignment token (FOO=...), identified
// by the NAME shape alone so the value — which may contain '/', a URL or a secret
// — never defeats the skip.
var envAssignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// shellProgram extracts the program name from a shell command (the first bare
// token after any env-assignments), discarding all arguments — which can carry
// secrets. The chosen token is run through SanitizeURL, so even a credential-
// bearing token (a URL accidentally taken as the program) is stripped of its
// userinfo and query before it can become a resource reference (docs/SECURITY-HARDENING.md).
// "FOO=postgres://u:p@h/db psql" → "psql".
func shellProgram(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return resShell
	}
	prog := resShell
	for _, f := range strings.Fields(command) {
		if envAssignRe.MatchString(f) {
			continue // env assignment like KEY=val (any value, incl. URLs)
		}
		prog = f
		break
	}
	return redact.SanitizeURL(prog)
}

// inputString returns input[key] as a string when it is a string-shaped value,
// else "". It tolerates a missing key or a non-string value (returning "").
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
