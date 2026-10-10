// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"context"
	"io"
	"log/slog"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func decisionOf(m map[string]any) string {
	if v, ok := m["permissionDecision"].(string); ok {
		return v
	}
	return ""
}

// reasonOf returns the hook's permissionDecisionReason so a test can pin WHY the PEP
// refused, not just that it did. The verdict alone cannot carry that: identity, policy,
// firewall and fallback denials all render the same "deny".
func reasonOf(m map[string]any) string {
	if v, ok := m["permissionDecisionReason"].(string); ok {
		return v
	}
	return ""
}

type erroringEval struct{ err error }

func (e erroringEval) Evaluate(_ context.Context, _ auth.Request) (auth.Decision, error) {
	return auth.Decision{}, e.err
}

type failingKillSwitchGuard struct{}

func (failingKillSwitchGuard) KillSwitchState(context.Context, model.TenantID) (governance.StopState, error) {
	return governance.StopState{}, context.DeadlineExceeded
}

type fakeOpener struct {
	status string
	err    error
	calls  int
	// consume* back the single-use spend. The zero value grants (granted=true,
	// no replay, no error), so a fake reporting GateStatusApproved still allows — tests that
	// need a replay/deny set these explicitly.
	consumeReplay bool
	consumeErr    error
	consumeCalls  int
}

func (f *fakeOpener) GateOnce(_ context.Context, _ model.TenantID, _, _, _, _, _, _ string) (string, string, string, error) {
	f.calls++
	return "appr-1", f.status, "ph", f.err
}

func (f *fakeOpener) ConsumeApproval(_ context.Context, _ model.TenantID, _, _, _ string) (bool, bool, error) {
	f.consumeCalls++
	if f.consumeErr != nil {
		return false, false, f.consumeErr
	}
	if f.consumeReplay {
		return false, true, nil
	}
	return true, false, nil
}

func (f *fakeOpener) GateOnceForSession(ctx context.Context, tenant model.TenantID, action, kind, ref, hash, reason, actor, session string) (string, string, string, error) {
	return f.GateOnce(ctx, tenant, action, kind, ref, hash, reason, actor)
}
func (f *fakeOpener) StatusScoped(ctx context.Context, tenant model.TenantID, ref, hash, action, kind, subject string) (string, string, error) {
	return f.status, "ph", f.err
}
