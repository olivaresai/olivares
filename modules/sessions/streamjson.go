// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "encoding/json"

// stream-json is Claude Code's headless protocol (`--output-format stream-json`):
// newline-delimited JSON, one message object per line. We parse only the MINIMAL
// envelope needed to drive the lifecycle — the session id (to enable --resume),
// the message type (activity), and a refusal/stop hint. The frame BODY (prompts,
// completions, tool args) is NEVER decoded or persisted (minimal-data, docs/SECURITY-HARDENING.md
// §3): a frame that does not parse, or carries fields we do not read, is bridged
// to attach clients verbatim but contributes nothing to stored state.

// streamJSONFrame is the minimal envelope of one stream-json line.
type streamJSONFrame struct {
	// Type is the message type ("system","assistant","user","result","stream_event",…).
	Type string `json:"type"`
	// Subtype qualifies a system message ("init", "api_retry", …).
	Subtype string `json:"subtype"`
	// SessionID is present on the init message (and echoed on others); it is the
	// UUID `claude --resume <id>` reattaches to.
	SessionID string `json:"session_id"`
}

// parseStreamJSON decodes the minimal envelope of one NDJSON output line. ok is
// false for a non-JSON line (a stray log line on stdout) — such a line is still
// bridged to attach clients, it just yields no state signal.
func parseStreamJSON(line []byte) (streamJSONFrame, bool) {
	var f streamJSONFrame
	if err := json.Unmarshal(line, &f); err != nil {
		return streamJSONFrame{}, false
	}
	return f, true
}

// isInit reports whether the frame is the session-establishing init message that
// carries the resumable session id.
func (f streamJSONFrame) isInit() bool {
	return f.Type == "system" && f.Subtype == "init" && f.SessionID != ""
}
