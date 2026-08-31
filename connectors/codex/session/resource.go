// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"strings"
)

// resource.go derives a SANITIZED resource reference from a Codex tool call.
//
// The rule it exists to keep: raw tool arguments never leave this file. What travels
// onward is a bounded, structural reference — the command's head, a path, an MCP tool
// name — because everything downstream (the decision record, the timeline, the ledger)
// is minimal-data by contract.

// maxRefLen bounds a derived reference. A shell command line can be arbitrarily long and
// can contain anything at all, including a secret pasted by the model.
const maxRefLen = 200

// Resource kinds. They mirror the vocabulary the access graph already speaks, so a Codex
// edge and a Claude edge describe the same world.
const (
	kindShell   = "shell"
	kindFile    = "file"
	kindMCPTool = "mcp.tool"
	kindTool    = "codex.tool"
)

// Modes.
const (
	modeRead    = "read"
	modeWrite   = "write"
	modeUnknown = "unknown"
)

// resourceFromTool maps (tool_name, tool_input) to (kind, sanitized ref, mode).
//
// The tool names are the ones codex-cli 0.145.0 actually sends. "Bash" was captured live
// (tool_input {"command":"echo HELLO_S528"}); the rest are matched defensively and case
// insensitively, because a tool this connector does not recognize must still produce a
// governable reference rather than an empty one that a path-scoped policy cannot match.
func resourceFromTool(tool string, input map[string]any) (kind, ref, mode string) {
	t := strings.ToLower(strings.TrimSpace(tool))
	switch {
	case t == "bash" || t == "shell" || strings.Contains(t, "exec"):
		return kindShell, clip(commandHead(input)), modeWrite

	case strings.Contains(t, "apply_patch") || strings.Contains(t, "edit") || strings.Contains(t, "write"):
		return kindFile, clip(firstString(input, "path", "file_path", "filename", "file")), modeWrite

	case strings.Contains(t, "read") || strings.Contains(t, "view") || strings.Contains(t, "cat"):
		return kindFile, clip(firstString(input, "path", "file_path", "filename", "file")), modeRead

	case strings.HasPrefix(t, "mcp__") || strings.Contains(t, "mcp"):
		// The tool NAME is the reference for an MCP call: it names the server and the
		// tool, which is exactly what an allowlist is written against. The arguments are
		// not included — they are the part that carries payloads.
		return kindMCPTool, clip(strings.TrimSpace(tool)), modeUnknown

	case t == "":
		// A hook event with no tool (SessionStart, Stop, …) is not a resource access.
		return "", "", modeUnknown

	default:
		return kindTool, clip(strings.TrimSpace(tool)), modeUnknown
	}
}

// commandHead returns the leading, non-argument part of a shell command — enough to
// identify WHAT is being run without carrying the whole line. "git push --force origin
// main" becomes "git push"; a command with a pipe or a redirect is cut at the first one,
// because everything after it is a second command whose arguments we are not summarizing.
func commandHead(input map[string]any) string {
	cmd := firstString(input, "command", "cmd", "script")
	if cmd == "" {
		return ""
	}
	// Cut at the first shell metacharacter: what follows is a different command.
	if i := strings.IndexAny(cmd, "|;&>"); i >= 0 {
		cmd = cmd[:i]
	}
	fields := strings.Fields(cmd)
	switch len(fields) {
	case 0:
		return ""
	case 1:
		return fields[0]
	}
	// Two tokens is the sweet spot for the shape a policy is written against ("git push",
	// "rm -rf", "curl https://…"): the second token is kept ONLY when it is not an
	// argument value, so a secret passed positionally does not ride along.
	if strings.HasPrefix(fields[1], "-") || looksLikeValue(fields[1]) {
		return fields[0]
	}
	return fields[0] + " " + fields[1]
}

// looksLikeValue is a conservative filter: anything that could be a path, a URL, a token
// or a quoted string is treated as data rather than as a subcommand.
func looksLikeValue(s string) bool {
	if len(s) > 40 {
		return true
	}
	return strings.ContainsAny(s, "/\\'\"$`=:@")
}

func firstString(input map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := input[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// clip bounds a derived reference WITHOUT breaking a character.
//
// ⛔ IT WAS `s[:maxRefLen]`, WHICH SLICES BYTES. Measured on 2026-08-19 with the exact former
//
//	body: 400 CJK runes (1200 bytes) came out as 203 bytes that are NOT valid UTF-8, ending in
//	RuneError. Three of this file's five call sites derive the ref from a FILE PATH, which is
//	precisely where non-ASCII shows up in a real installation — a client name in Cyrillic, an
//	accented directory — and the result lands in an audit record nobody can read or compare
//	afterwards.
//
//	The bound is now counted in RUNES, not bytes, so it means the same thing in every language:
//	200 bytes are 200 characters in English and 66 in Japanese, and a cap that narrows with the
//	customer's alphabet is a cap that discriminates by accident.
func clip(s string) string {
	if len(s) <= maxRefLen {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxRefLen {
		return s
	}
	return string(runes[:maxRefLen]) + "…"
}
