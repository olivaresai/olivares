// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"sort"
	"strconv"
	"strings"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
)

// hookcontent.go is the per-tool CONTENT EXTRACTOR for the Claude Code hooks path: it
// reduces an unbounded PreToolUse/PermissionRequest tool_input (including the arbitrary
// arguments of an mcp__* server tool) to the same ContentChannel vocabulary
// CollectRequestContent produces for /v1/messages (connectors/claude-api). It is the wire
// half the governed decider (cmd/olivares, AGPL) and the OPTIONAL commercial hooks-hardening
// firewall (enterprise/hookhardening, closed) both read — neither imports the other's
// package, exactly as contentscan.go does for the inline proxy firewall.
//
// THE GAP IT CLOSES. The hook PEP (pep.go) reduces tool_input to a single sanitized resource
// REFERENCE for the decision context; it never inspects the tool ARGUMENTS for sensitive
// values or dangerous structure. So a tool-call that writes a secret to a file, posts a
// credential to an exfil sink, or pipes untrusted code to a shell passed the PEP unscanned.
// This extractor surfaces the argument content as channels: extractable text feeds the
// classifier and the firewall's structural detectors (the closed add-on); anything opaque
// (a value too deep / past a bound) is marked UNSCANNED so the firewall's deny-closed
// posture can refuse it rather than wave it through.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md). The extracted text is handled IN FLIGHT only — the same posture
// as contentscan.go — and returned to the in-process caller (the deterministic detectors run
// on it and emit only redacted hashes). It is NEVER persisted, logged, or transmitted; a
// channel's Ref carries only the non-sensitive argument PATH (e.g. "command", "edits[0].
// new_string", "mcp:query"), never the value.
//
// DENY-CLOSED BY CONSTRUCTION. A tool_input nested past the depth bound, or beyond the
// channel/byte caps, is marked unscanned (not silently dropped), so a hostile or pathological
// payload can never slip argument content past inspection — the fail-safe direction.

// Hook content channel kinds. They name the tool family a channel's text came from so a
// finding is explainable; they are stable identifiers carried into findings. The "hook."
// prefix keeps them distinct from the /v1/messages content channels (claudeapi.Channel*).
const (
	// HookChannelShell is a shell command (Bash) — the highest-risk argument surface.
	HookChannelShell = "hook.shell"
	// HookChannelFileWrite is file-mutating content (Write/Edit/MultiEdit/NotebookEdit).
	HookChannelFileWrite = "hook.file_write"
	// HookChannelFileRead is a read/search argument (Read/Glob/Grep/LS) — path or pattern.
	HookChannelFileRead = "hook.file_read"
	// HookChannelWeb is a web argument (WebFetch/WebSearch) — url, prompt or query.
	HookChannelWeb = "hook.web"
	// HookChannelMCP is an mcp__server__tool argument (arbitrary, walked structurally).
	HookChannelMCP = "hook.mcp"
	// HookChannelToolInput is the catch-all for an unrecognized tool's arguments
	// (deny-closed coverage: a tool we do not model is still fully walked).
	HookChannelToolInput = "hook.tool_input"
)

// extraction bounds. tool_input is already capped at the wire by maxPEPBody (1 MiB); these
// bound the WORK the detectors do over it and are hostile-input backstops, not real limits —
// content beyond them is marked unscanned (deny-closed), never silently dropped.
const (
	maxToolInputDepth     = 8           // structural recursion bound (mcp args can nest)
	maxToolInputChannels  = 512         // distinct extracted leaves
	maxToolInputScanBytes = 1024 * 1024 // total extracted text across all channels
)

// ExtractToolContent walks a tool_input map and returns the classifiable channels for the
// hooks firewall. The channel KIND is chosen from the tool family; every string leaf becomes
// one channel keyed by its argument path (the Ref). A nil/empty input yields an empty result
// (the firewall no-ops). Caps tripped ⇒ Unscanned=true (deny-closed). The output channels are
// sorted by Ref so two extractions of an equivalent input are byte-identical (reproducible
// findings).
func ExtractToolContent(tool string, input map[string]any) claudeapi.CollectedContent {
	x := &toolContentExtractor{kind: hookChannelKind(tool)}
	x.walk("", input, 0)
	x.finish()
	return claudeapi.CollectedContent{Channels: x.channels, Texts: x.texts, Unscanned: x.unscanned}
}

// hookChannelKind maps a tool name to the channel family. The known Claude Code built-ins get
// a specific kind for explainability; an mcp__* tool gets the MCP kind; everything else
// (an unmodeled or future tool) gets the catch-all so its arguments are still fully walked.
func hookChannelKind(tool string) string {
	switch tool {
	case "Bash", "BashOutput", "KillBash":
		return HookChannelShell
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		return HookChannelFileWrite
	case "Read", "Glob", "Grep", "LS", "NotebookRead":
		return HookChannelFileRead
	case "WebFetch", "WebSearch":
		return HookChannelWeb
	}
	if strings.HasPrefix(tool, "mcp__") {
		return HookChannelMCP
	}
	return HookChannelToolInput
}

type toolContentExtractor struct {
	kind      string
	channels  []claudeapi.ContentChannel
	texts     []string
	seenText  map[string]bool
	bytes     int
	unscanned bool
}

// walk recurses into a decoded JSON value collecting string leaves. Anything past the depth
// bound, or once the channel/byte caps are exhausted, is marked unscanned (deny-closed). It
// handles the shapes json.Unmarshal produces (string/float64/bool/nil/[]any/map[string]any)
// plus the typed leaves an in-process constructor may set (int/int64/json.Number).
func (x *toolContentExtractor) walk(path string, v any, depth int) {
	if depth > maxToolInputDepth {
		x.unscanned = true // too deep to parse safely — refuse, never drop
		return
	}
	switch t := v.(type) {
	case nil:
		// A JSON null carries no content.
	case string:
		x.leaf(path, t)
	case map[string]any:
		for _, k := range sortedKeys(t) {
			x.walk(joinPath(path, k), t[k], depth+1)
		}
	case []any:
		for i, e := range t {
			x.walk(path+"["+strconv.Itoa(i)+"]", e, depth+1)
		}
	case bool, float64, int, int64:
		// Scalars are not free-text content (a secret/PII value is a string). Skipped.
	default:
		// An unmodeled value kind we cannot reduce to text: deny-closed, mark unscanned.
		x.unscanned = true
	}
}

// leaf records one extractable text channel for a string argument, de-duplicating the text
// for the classifier and honoring the channel/byte caps (cap exceeded ⇒ unscanned, the rest
// of the leaf is refused). An empty/blank string yields no channel (nothing to classify).
func (x *toolContentExtractor) leaf(path, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if len(x.channels) >= maxToolInputChannels || x.bytes >= maxToolInputScanBytes {
		x.unscanned = true // more content than we will scan — deny-closed
		return
	}
	x.bytes += len(text)
	x.channels = append(x.channels, claudeapi.ContentChannel{
		Kind: x.kind, Role: "tool_input", Text: text, Scannable: true, Ref: firstNonEmpty(path, "value"),
	})
	if x.seenText == nil {
		x.seenText = map[string]bool{}
	}
	if !x.seenText[text] {
		x.seenText[text] = true
		x.texts = append(x.texts, text)
	}
}

// finish sorts the channels by Ref for reproducibility.
func (x *toolContentExtractor) finish() {
	sort.SliceStable(x.channels, func(i, j int) bool { return x.channels[i].Ref < x.channels[j].Ref })
}

// joinPath joins an argument path segment ("" root → key; nested → "parent.key").
func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// sortedKeys returns a map's keys in deterministic order (so a walk is reproducible).
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
