// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// recordingInspector keeps the last input it was shown.
type recordingInspector struct {
	got claudeapi.ContentInspectionInput
}

func (r *recordingInspector) Inspect(_ context.Context, in claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	r.got = in
	return claudeapi.ContentInspectionDecision{Forward: true}
}

// The inspector is told which run a tool-call belongs to only when the session credential
// proved it. The payload's own session id is the caller's word and never reaches it.
func TestHookFirewallNamesTheProvenRunOnly(t *testing.T) {
	in := claude.HookDecisionInput{Tool: "Bash", Event: "PreToolUse", SessionID: "claimed-by-the-payload"}

	proven := &recordingInspector{}
	d := &Decider{Inspector: proven}
	ctx := context.WithValue(context.Background(), sessionHookScopeKey{}, auth.SessionScope{RunRef: "run-proven"})
	d.runHookFirewall(ctx, model.TenantID("t_x"), "actor", hookAgent{}, in)
	if proven.got.SessionRef != "run-proven" {
		t.Fatalf("SessionRef = %q, want the run the credential proved", proven.got.SessionRef)
	}

	unproven := &recordingInspector{}
	d = &Decider{Inspector: unproven}
	d.runHookFirewall(context.Background(), model.TenantID("t_x"), "actor", hookAgent{}, in)
	if unproven.got.SessionRef != "" {
		t.Fatalf("SessionRef = %q for a call whose credential proved no run; the payload's id must not be used", unproven.got.SessionRef)
	}
}
