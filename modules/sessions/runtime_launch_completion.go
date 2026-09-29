// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// RuntimeLaunchIdentity names the attempt that successfully reached running.
// It is historical process identity, not input authorization or a current lease.
type RuntimeLaunchIdentity struct {
	Tenant          model.TenantID
	WorkspaceID     model.ID
	RunRef          string
	RuntimeLaunchID model.ID
	SessionSID      string
}

// RuntimeLaunchCompletion is an immutable, in-process completion witness. Only
// the successful launch/resume path can construct it. A read or dispatch replay
// cannot manufacture the original attempt from the run's current row.
type RuntimeLaunchCompletion struct {
	identity RuntimeLaunchIdentity
}

// Identity copies the original attempt. The empty witness reports false.
func (c RuntimeLaunchCompletion) Identity() (RuntimeLaunchIdentity, bool) {
	return c.identity, !c.identity.RuntimeLaunchID.IsZero()
}

func runtimeLaunchCompletion(tenant model.TenantID, runRef string, attempt model.ID, committed model.Record) RuntimeLaunchCompletion {
	workspace, err := model.ParseID(committed.String(colRunAuthzWorkspaceID))
	if err != nil || workspace.IsZero() || tenant.IsZero() || runRef == "" ||
		!validRuntimeUUIDv7(attempt.String()) || committed.String(colRuntimeLaunchID) != attempt.String() ||
		committed.String(colRunRef) != runRef || committed.String(colState) != stateRunning ||
		!validCanonicalSID(committed.String(colRunClaimSID)) {
		return RuntimeLaunchCompletion{}
	}
	return RuntimeLaunchCompletion{identity: RuntimeLaunchIdentity{
		Tenant: tenant, WorkspaceID: workspace, RunRef: runRef, RuntimeLaunchID: attempt,
		SessionSID: committed.String(colRunClaimSID),
	}}
}

// LaunchRunWithCompletion is the ordinary runtime's in-process launch result.
// The orchestration owner must authorize its launch request before calling this
// port, just as for RuntimeControl.LaunchForWork. It must retain this completion
// with its original link; a lost result is not repaired from ReadRunLaunch.
func (m *Module) LaunchRunWithCompletion(ctx context.Context, tenant model.TenantID, params CreateRunParams) (RuntimeLaunchCompletion, error) {
	dto, err := m.createRun(ctx, tenant, params)
	return dto.Completion, err
}

// ResumeRunWithCompletion returns the new resume attempt's original identity.
// The caller owns the ordinary resume authorization. An old completion remains
// unchanged and will be refused by input directed at this successor attempt.
func (m *Module) ResumeRunWithCompletion(ctx context.Context, tenant model.TenantID, runRef, actor, actorKind, agentIdentity string) (RuntimeLaunchCompletion, error) {
	dto, err := m.resumeRun(ctx, tenant, runRef, actor, actorKind, agentIdentity)
	return dto.Completion, err
}
