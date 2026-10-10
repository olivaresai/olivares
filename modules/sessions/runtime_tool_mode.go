// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// The session's mode as the TOOL reports it: plan or act, and how much it may do
// without asking. Olivares does not derive it, it records what the tool said:
//
//   - Claude Code: `permissionMode` on the stream-json init frame, and again on the
//     system/status frame it sends when the mode changes (streamjson.go).
//   - Codex: the `approvalPolicy` and `sandbox` its thread/start or thread/resume
//     answer carries (driver_codex.go), "on-request · workspaceWrite".

// maxToolModeWord bounds one word of a mode. Every mode either tool reports is a
// short identifier; anything else is not a mode and is not stored.
const maxToolModeWord = 32

// toolModeValue keeps a reported mode word only when it looks like one: letters,
// digits, '-' and '_', at most maxToolModeWord long. The child is not trusted to
// put prose on the run row.
func toolModeValue(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxToolModeWord {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ""
		}
	}
	return s
}

// recordToolMode writes the mode the tool reported on the run row. Best-effort,
// like every other bridge-side write: a mode that could not be written is
// written again by the next frame that reports one.
func (m *Module) recordToolMode(ctx context.Context, lr *liveRun, mode string) {
	if mode == "" {
		return
	}
	m.mutateRunBest(ctx, lr, func(rec model.Record) {
		rec[colRunToolMode] = mode
	})
}
