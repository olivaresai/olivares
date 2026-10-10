// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The session cockpit lists core Sessions, and a run started from the CLI or
// the console never had one, so the cockpit read "Sessions · 0" while a session ran.
// Each launch attempt now opens its own core Session in the transaction that stamps
// its launch id, keyed by that id like the cockpit's own launches, and the transition
// that stops or fails the run closes it. The managed identity (claim_sid) is reused;
// this is no second identity.

// openCoreSession opens the core Session of the launch attempt rec carries and records
// it on the run. A row with no launch id (a launch waiting for approval) has none yet.
// holder is the agent that holds a work launch's work (the item owner its lease is
// acquired for), the Session's agent as in the cockpit's own launches; zero otherwise.
func openCoreSession(ctx context.Context, sc store.Scope, rec model.Record, holder model.ID, at time.Time) error {
	launch := rec.String(colRuntimeLaunchID)
	if launch == "" {
		return nil
	}
	core, err := sc.Sessions().Create(ctx, model.Session{
		ExternalID: launch, WorkspaceID: model.ID(rec.String(colRunAuthzWorkspaceID)), AgentID: holder,
		State: model.SessionRunning, StartedAt: model.NewTimestamp(at),
	})
	if err != nil {
		return err
	}
	rec[colRunCoreSessionID] = core.ID.String()
	return nil
}

// closeCoreSession ends the run's core Session when the run stops (completed) or fails
// (failed). One already ended, or a historical run with none, is left as it is.
func closeCoreSession(ctx context.Context, sc store.Scope, rec model.Record, to string, at time.Time) error {
	id := rec.String(colRunCoreSessionID)
	if id == "" {
		return nil
	}
	core, err := sc.Sessions().Get(ctx, model.ID(id))
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil || core.EndedAt != nil {
		return err
	}
	core.State = model.SessionCompleted
	if to == stateFailed {
		core.State = model.SessionFailed
	}
	ended := model.NewTimestamp(at)
	core.EndedAt = &ended
	_, err = sc.Sessions().Update(ctx, core)
	return err
}

// RuntimeCoreSessionInScope is ValidateRuntimeInputTargetInScope that also returns the
// core Session the target's launch attempt opened, for a caller that adopts exactly
// that Session in this same mutation Scope. It is the same validator, not a second
// one; a running target with no core Session is refused like any other mismatch.
func (m *Module) RuntimeCoreSessionInScope(ctx context.Context, sc store.Scope, target RuntimeInputTarget) (model.ID, error) {
	rec, err := m.runtimeInputTargetInScope(ctx, sc, target, "")
	if err != nil {
		return "", err
	}
	id, err := model.ParseID(rec.String(colRunCoreSessionID))
	if err != nil {
		return "", ErrRuntimeInputTarget
	}
	return id, nil
}

// CoreSessionRun is what ReadCoreSessionRunInScope presents: the attempt's launch
// identity and stored display facts: the work role, who launched the attempt
// (the claim holder's actor ref) and the model name it was launched with (empty when
// the launch named none). Labels only: neither authorizes anything. The identity is
// embedded unchanged, so its equality as an attempt key is not widened. Work is the
// work-bound run's stored work observation, the one the runtime target validator
// compares (runtime_input.go); nil for a run with no work binding.
type CoreSessionRun struct {
	RuntimeLaunchIdentity
	Actor    string
	ModelRef string
	// Role is the validated stored launch scope, not current authority.
	Role string
	Work *RuntimeInputWork
}

// ReadCoreSessionRunInScope is the launch identity behind an engine-opened core Session:
// the run that stores coreSessionID, by exact equality in the caller's Scope,
// and the attempt's launch id from the Session's own ExternalID, so it stays readable
// after the attempt ended and the run released it. Nothing is searched. The cockpit
// links a CLI or console run with it when no private original exists. A Session of an
// earlier attempt (the run resumed since) or no run at all answers store.ErrNotFound.
func (m *Module) ReadCoreSessionRunInScope(ctx context.Context, sc store.Scope, coreSessionID model.ID) (CoreSessionRun, error) {
	if m == nil || sc == nil || coreSessionID.IsZero() {
		return CoreSessionRun{}, store.ErrNotFound
	}
	repo, err := sc.Ext(runKind)
	if err != nil {
		return CoreSessionRun{}, err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colRunCoreSessionID, coreSessionID.String())}, Limit: 1})
	if err != nil {
		return CoreSessionRun{}, err
	}
	if len(rows) == 0 {
		return CoreSessionRun{}, store.ErrNotFound
	}
	core, err := sc.Sessions().Get(ctx, coreSessionID)
	if err != nil {
		return CoreSessionRun{}, err
	}
	rec := rows[0]
	workspace, werr := model.ParseID(rec.String(colRunAuthzWorkspaceID))
	launch, lerr := model.ParseID(core.ExternalID)
	if held := rec.String(colRuntimeLaunchID); werr != nil || lerr != nil || (held != "" && held != core.ExternalID) {
		return CoreSessionRun{}, ErrRunAuthorityUnavailable
	}
	out := CoreSessionRun{
		RuntimeLaunchIdentity: RuntimeLaunchIdentity{Tenant: sc.Tenant(), WorkspaceID: workspace, RunRef: rec.String(colRunRef), RuntimeLaunchID: launch},
		Actor:                 rec.String(colClaimHolder), ModelRef: rec.String(colRunModelRef),
	}
	if scope := runWorkScope(rec); scope != nil {
		out.Role = scope.Role
	}
	if sid := rec.String(colRunClaimSID); validCanonicalSID(sid) {
		out.SessionSID = sid
	}
	if runHasWorkBinding(rec) {
		if out.Work, err = storedRunWork(ctx, sc, rec); err != nil {
			return CoreSessionRun{}, err
		}
	}
	return out, nil
}

// storedRunWork is a work-bound run's stored work observation: its own work stamp and
// the work lease of the item it names, as stored, nothing inferred. An incomplete
// stamp or a missing lease is unavailable evidence, never an empty observation.
func storedRunWork(ctx context.Context, sc store.Scope, rec model.Record) (*RuntimeInputWork, error) {
	// Any incomplete stamp, a launch spec hash alone included, is
	// unavailable evidence here, never a stale fence; the parser keeps its checks.
	for _, column := range []string{colRunWorkItemID, colRunWorkLeaseFence, colRunWorkDispatchKey, colRunWorkOwnerEpoch, colRunWorkLaunchSpecHash} {
		if rec.IsNull(column) {
			return nil, unknown("evidence_unavailable", nil)
		}
	}
	stamp, err := parseRunWorkStamp(rec, rec.Int(colRunWorkLeaseFence), false)
	if err != nil {
		return nil, err
	}
	lease, found, err := findWorkLease(ctx, sc, stamp.itemID)
	if err != nil {
		return nil, err
	}
	spec := rec.Bytes(colRunWorkLaunchSpecHash)
	if !found || len(spec) != sha256.Size {
		return nil, unknown("evidence_unavailable", nil)
	}
	work := &RuntimeInputWork{
		WorkItemID: stamp.itemID, LeaseFence: stamp.fence, OwnerEpoch: stamp.ownerEpoch,
		LeaseExpiresAt: lease.String(colLeaseExpiresAt), HolderSID: lease.String(colLeaseHolderSID),
		HolderRunRef: lease.String(colLeaseHolderRunRef), HolderAgentID: model.ID(lease.String(colLeaseHolderAgentRef)),
	}
	copy(work.DispatchKey[:], stamp.dispatchKey)
	copy(work.LaunchSpecHash[:], spec)
	return work, nil
}
