// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/modules/sessions"
)

// SessionPresetDecision is the one table of what a session's permission adds to the
// live policy, for Claude hooks and for native provider approvals. A preset it does not
// know grants nothing.
func TestSessionPresetDecisionPerPreset(t *testing.T) {
	allow, ask, deny := claude.DecisionAllow, claude.DecisionAsk, claude.DecisionDeny
	for _, tc := range []struct {
		preset                      string
		read, write, command, other string
	}{
		{sessions.PresetReadOnly, allow, deny, deny, deny},
		{sessions.PresetAsk, allow, ask, ask, ask},
		{sessions.PresetEditsOnly, allow, allow, ask, ask},
		// "Other tools are refused" holds for every tool, not only for Claude Code's
		// dontAsk allowlist.
		{sessions.PresetEditsAndCommands, allow, allow, allow, deny},
		// Auto adds nothing: the tool's own reviewed automatic mode decides, and the live
		// policy, the kill switch and the audit still apply above this table. Without this
		// case every Auto tool call on Claude Code would be refused as an unknown preset.
		{sessions.PresetAuto, allow, allow, allow, allow},
		{sessions.PresetFull, allow, allow, allow, allow},
		{sessions.PresetCustom, allow, allow, allow, allow},
	} {
		for mode, want := range map[string]string{ActionRead: tc.read, ActionWrite: tc.write, ActionCommand: tc.command, "unknown": tc.other} {
			got, err := SessionPresetDecision(tc.preset, mode)
			if err != nil || got != want {
				t.Errorf("SessionPresetDecision(%q, %q) = %q, %v; want %q", tc.preset, mode, got, err, want)
			}
		}
	}
	for _, unknown := range []string{"", "yolo", "AUTO"} {
		if got, err := SessionPresetDecision(unknown, "read"); err == nil {
			t.Errorf("SessionPresetDecision(%q) = %q, nil; an unknown preset must grant nothing", unknown, got)
		}
	}
}
