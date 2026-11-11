// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Each entry exists only while the registered process waits on the existing
// governance queue. The pointer identifies this wait, including during cleanup.
type runApprovalWait struct {
	ref       string
	expiresAt time.Time
}

// BeginApprovalWait projects one engine-requested human wait onto its supervised
// run. It accepts the resolved session principal; user and stale generations
// cannot attach an approval to another run. The caller ends this wait on every
// terminal return from the shared approval service. No queue or grant lives here.
func (m *Module) BeginApprovalWait(ctx context.Context, principal auth.Principal, ref string, expiresAt time.Time) (func(), error) {
	tenant := principal.SessionScope()
	if tenant.IsZero() || tenant.IsSystem() || !principal.IsMember(tenant) || principal.SessionIdentity == "" || principal.SessionRunRef == "" || principal.SessionFence < 1 || ref == "" || !m.now().Before(expiresAt) {
		return nil, auth.ErrUnauthenticated
	}
	lr, ok := m.rt.getLive(tenant, principal.SessionRunRef)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	lr.authorityMu.RLock()
	defer lr.authorityMu.RUnlock()
	if current, ok := m.rt.getLive(tenant, lr.runRef); !ok || current != lr || lr.proc == nil || lr.claim.SID != principal.SessionIdentity || lr.claim.Fence != principal.SessionFence || lr.launchID.IsZero() {
		return nil, auth.ErrUnauthenticated
	}
	if err := m.Authority(ctx, tenant, lr.claim.SID, lr.claim.Holder, lr.claim.Fence); err != nil {
		return nil, err
	}
	rec, err := m.loadRun(ctx, tenant, lr.runRef)
	if err != nil {
		return nil, err
	}
	if err := guardRuntimeLaunch(lr.launchID)(rec); err != nil {
		return nil, err
	}
	if rec.String(colState) != stateRunning || rec.String(colRunClaimSID) != principal.SessionIdentity || rec.Int(colClaimFence) != principal.SessionFence || rec.String(colRunAuthzWorkspaceID) != principal.SessionWorkspaceID.String() {
		return nil, auth.ErrUnauthenticated
	}
	wait := &runApprovalWait{ref: ref, expiresAt: expiresAt}
	lr.mu.Lock()
	if lr.finalized || lr.stopRequested || lr.launchFailed {
		lr.mu.Unlock()
		return nil, auth.ErrUnauthenticated
	}
	lr.pendingApprovals = append(lr.pendingApprovals, wait)
	lr.mu.Unlock()
	m.pushAttention(tenant, rec, pushApproval)
	liveRef := rec.String(colRunLiveRef)
	m.publishRunChanged(ctx, tenant, lr.runRef, liveRef)
	var once sync.Once
	return func() {
		once.Do(func() {
			lr.mu.Lock()
			lr.pendingApprovals = slices.DeleteFunc(lr.pendingApprovals, func(candidate *runApprovalWait) bool { return candidate == wait })
			lr.mu.Unlock()
			// The wait may end after its request's context was cancelled.
			m.publishRunChanged(context.WithoutCancel(ctx), tenant, lr.runRef, liveRef)
		})
	}, nil
}

// publishRunChanged puts the run's managed live row on the live stream, so a console
// watching it reads the run again now instead of on its next poll. A run whose managed
// row is not proven yet has none to announce; the console's poll covers it.
func (m *Module) publishRunChanged(ctx context.Context, tenant model.TenantID, runRef, liveRef string) {
	id, ok := parseLiveRef(liveRef)
	if !ok {
		return
	}
	var snap liveSnapshot
	if err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		live, err := findLiveByID(ctx, sc, id)
		if err != nil {
			return err
		}
		snap = m.snapshot(live, tenant)
		return nil
	}); err != nil {
		m.warnf("sessions: the run's approval wait could not be announced on the live stream; the console reads it on its next poll", "run_ref", runRef, "cause", redactErr(err))
		return
	}
	m.broker.publish(snap)
}

// currentLiveOf is the supervised handle of a running row: the same launch, session and
// fence the row records. A row this runtime does not supervise has none.
func (m *Module) currentLiveOf(rec model.Record) (*liveRun, bool) {
	if rec.String(colState) != stateRunning {
		return nil, false
	}
	lr, ok := m.rt.getLive(model.TenantID(rec.String(model.ColTenantID)), rec.String(colRunRef))
	if !ok || lr.launchID.String() != rec.String(colRuntimeLaunchID) || lr.claim.SID != rec.String(colRunClaimSID) || lr.claim.Fence != rec.Int(colClaimFence) {
		return nil, false
	}
	return lr, true
}

func (m *Module) pendingRunApproval(rec model.Record) string {
	refs := m.pendingRunApprovals(rec)
	if len(refs) == 0 {
		return ""
	}
	return refs[0]
}

// pendingRunApprovals is every human request the run currently awaits, oldest first.
func (m *Module) pendingRunApprovals(rec model.Record) []string {
	lr, ok := m.currentLiveOf(rec)
	if !ok {
		return nil
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.finalized || lr.stopRequested || lr.launchFailed {
		return nil
	}
	now := m.now()
	lr.pendingApprovals = slices.DeleteFunc(lr.pendingApprovals, func(wait *runApprovalWait) bool { return !now.Before(wait.expiresAt) })
	refs := make([]string, 0, len(lr.pendingApprovals))
	for _, wait := range lr.pendingApprovals {
		refs = append(refs, wait.ref)
	}
	return refs
}
