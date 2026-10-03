// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// StopForAccessChange retires only the exact supervised generation whose session
// credential was revoked. It is a runtime action, so lost launcher authority
// cannot prevent teardown. It starts bounded graceful stop/finalize, whose
// single terminal event makes the same run resumable.
func (m *Module) StopForAccessChange(ctx context.Context, scope auth.SessionScope, user string) error {
	return m.stopForAccessLoss(ctx, scope, "Access changed for "+accessLossUser(user)+"; resume to continue with the new access", false)
}

// StopForAccessEnded gracefully stops the exact generation whose owner was
// offboarded or lost standing. Current launch authorization still refuses resume.
func (m *Module) StopForAccessEnded(ctx context.Context, scope auth.SessionScope, user string) error {
	return m.stopForAccessLoss(ctx, scope, "Access ended for "+accessLossUser(user), true)
}

// UseSessionAccessCheck binds the one issuer's owner-standing check at composition,
// before Start. It adds no per-tool read and shares the existing active stop loop.
func (m *Module) UseSessionAccessCheck(check func(context.Context, model.TenantID, string) (auth.SessionScope, string, error)) {
	m.rt.sessionAccessCheck = check
}

func accessLossUser(user string) string {
	user = strings.Join(strings.Fields(user), " ")
	if user == "" {
		return "the launcher"
	}
	return user
}

func (m *Module) stopForAccessLoss(ctx context.Context, scope auth.SessionScope, reason string, accessEnded bool) error {
	if scope.TenantID.IsZero() || scope.TenantID.IsSystem() || scope.WorkspaceID.IsZero() || scope.SessionRef == "" || scope.RunRef == "" || scope.Holder == "" || scope.Fence < 1 {
		return auth.ErrUnauthenticated
	}
	release, err := m.rt.lockRunContext(ctx, liveKey(scope.TenantID, scope.RunRef))
	if err != nil {
		return err
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	lr, ok := m.rt.getLive(scope.TenantID, scope.RunRef)
	if !ok || lr.proc == nil || lr.launchID.IsZero() || lr.claim.SID != scope.SessionRef || lr.claim.Holder != scope.Holder || lr.claim.Fence != scope.Fence {
		return auth.ErrUnauthenticated
	}
	rec, err := m.loadRun(ctx, scope.TenantID, scope.RunRef)
	if err != nil {
		return err
	}
	if rec.String(colRunAuthzWorkspaceID) != scope.WorkspaceID.String() || guardRuntimeLaunch(lr.launchID)(rec) != nil {
		return auth.ErrUnauthenticated
	}

	lr.mu.Lock()
	if lr.finalized || lr.stopRequested {
		lr.mu.Unlock()
		return nil
	}
	lr.stopRequested = true
	lr.stopReason = reason
	lr.ownerAccessEnded = accessEnded
	lr.mu.Unlock()
	// A tool call can discover this loss inside its own ResolveRun. Draining
	// that call on its resolver stack would wait on its deferred completion.
	// Transfer the operation lock to the bounded teardown, so the caller can deny
	// and unwind while resume stays serialized behind this exact generation.
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	stopRelease := release
	release = nil
	go func() {
		defer stopRelease()
		defer cancel()
		outcome, stopErr, revokeErr := m.stopLiveEffect(stopCtx, lr, reason, legacyRevocationPhase(stopCtx))
		if stopErr != nil {
			lr.cancel()
		}
		if outcome != stopEffectFinalized || stopErr != nil || revokeErr != nil {
			m.warnf("sessions: access-change teardown is incomplete", "run_ref", lr.runRef, "err", redactErr(errors.Join(stopCtx.Err(), stopErr, revokeErr)))
		}
	}()
	return nil
}
