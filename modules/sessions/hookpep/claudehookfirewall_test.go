// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/model"
)

func TestHookFirewall_NilInspectorIsInert(t *testing.T) {
	// The default AGPL build path: a nil inspector is a clean pass — the firewall changes nothing.
	d := &Decider{}
	dec := d.runHookFirewall(context.Background(), model.TenantID("t_x"), "actor", hookAgent{}, claude.HookDecisionInput{
		Tool: "Bash", Event: "PreToolUse",
	})
	if !dec.Forward {
		t.Fatalf("nil inspector must be an inert clean pass")
	}
}
