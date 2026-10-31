// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// engineResumeDetail is the run ledger's line for a resume the engine made by itself.
const engineResumeDetail = "resumed automatically after the engine stopped"

// engineResumeWorkerKey names the one background pass in approvalWorkers, so a
// second recovery while it runs (a leader promotion) starts no duplicate and
// Stop cancels and waits for it like the approval waiters.
const engineResumeWorkerKey = "engine-stopped-runs"

// engineResumeKey carries, on the context of the engine's own resume, the launcher
// restored for that run. Only the recovery pass below sets it.
type engineResumeKey struct{}

func withEngineResume(ctx context.Context, launcher auth.Principal) context.Context {
	return context.WithValue(ctx, engineResumeKey{}, launcher)
}

func engineResumeFrom(ctx context.Context) (auth.Principal, bool) {
	launcher, ok := ctx.Value(engineResumeKey{}).(auth.Principal)
	return launcher, ok
}

// engineResumeNote is the ledger detail of a resume made by the engine's own recovery.
func engineResumeNote(ctx context.Context) string {
	if _, ok := ctx.Value(limitResumeKey{}).(limitResumeAttempt); ok {
		return "resumed by schedule after usage reset"
	}
	if _, ok := engineResumeFrom(ctx); ok {
		return engineResumeDetail
	}
	return ""
}

// resumeEngineStoppedRuns runs again, in the background, every run that the engine's
// own stop ended (stopAllRuns marks it with engineStoppedReason) or that a crashed
// engine left behind (engineDiedReason). A run stopped by a person, by the idle policy
// or by its own exit carries another reason and stays stopped. Each run resumes as its
// launcher, restored the way a queued launch restores its own: the credential the run
// was launched with is revalidated now (Dependencies.QueuedLaunchAuthorization), so a
// login that was revoked, expired or rotated, or a launcher no longer allowed to write
// the run, leaves it stopped. The same gates as a person's resume then apply: a run they
// refuse (full permissions or vault secrets without the launcher's administration, a
// retired profile, a launch gate) stays stopped, says why in the log, and is tried again
// at the next start; a launch that began and failed does not return. A run that can never
// resume by itself (no kept credential, an agent's, a login that ended) is counted in one
// line per tenant, not logged one by one at every start.
func (m *Module) resumeEngineStoppedRuns(tenants []model.TenantID) {
	m.rt.mu.Lock()
	if m.rt.approvalWorkersStopped {
		m.rt.mu.Unlock()
		return
	}
	if m.rt.approvalWorkers == nil {
		m.rt.approvalWorkers = map[string]context.CancelFunc{}
	}
	if _, running := m.rt.approvalWorkers[engineResumeWorkerKey]; running {
		m.rt.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.rt.approvalWorkers[engineResumeWorkerKey] = cancel
	m.rt.approvalWG.Add(1)
	m.rt.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			m.rt.mu.Lock()
			delete(m.rt.approvalWorkers, engineResumeWorkerKey)
			m.rt.mu.Unlock()
			m.rt.approvalWG.Done()
		}()
		for _, tenant := range tenants {
			m.resumeTenantEngineStoppedRuns(ctx, tenant)
		}
	}()
}

func (m *Module) resumeTenantEngineStoppedRuns(ctx context.Context, tenant model.TenantID) {
	// What was read is resumed even when a listing failed: the others are tried again at the next start.
	rows, err := m.engineStoppedRuns(ctx, tenant)
	if err != nil && ctx.Err() == nil {
		m.warnf("sessions: cannot list all the runs the engine stopped; those not read stay stopped until the next start or a person resumes them",
			"tenant", tenant.String(), "err", redactErr(err))
	}
	waiting := 0
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		runRef := row.String(colRunRef)
		// A run only a person can resume now: counted below, named at debug level.
		stays := func(why string, args ...any) {
			waiting++
			m.debugf("sessions: a run the engine stopped stays stopped: "+why,
				append([]any{"tenant", tenant.String(), "run_ref", runRef}, args...)...)
		}
		credential, ok := resumeCredentialOf(row)
		switch {
		case m.rt.QueuedLaunchAuthorization == nil:
			stays("this engine has no way to restore its launcher")
			continue
		case !ok:
			stays("it records no launcher credential to resume it with")
			continue
		case row.String(colRunAgentRef) != "":
			stays("an agent drives it, and only its driver resumes it")
			continue
		}
		principal, err := m.rt.QueuedLaunchAuthorization(ctx, tenant, credential, row.String(model.ColID), model.ID(row.String(colRunAuthzWorkspaceID)))
		if errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, store.ErrNotFound) {
			stays("its launcher's credential no longer authorizes the resume", "err", redactErr(err))
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				m.warnf("sessions: a run the engine stopped could not have its launcher restored; it is tried again at the next start",
					"tenant", tenant.String(), "run_ref", runRef, "err", redactErr(err))
			}
			continue
		}
		// The launcher's account is fenced as a queued launch's is: an offboard that commits
		// between the revalidation above and the resume's writes refuses them.
		resumeCtx := withQueuedLaunchUser(withEngineResume(ctx, principal), principal.UserID)
		_, err = m.resumeRun(resumeCtx, tenant, runRef, principal.Actor(), principal.ActorKind(), "")
		// A 409 is also what a launch gate's approval and an account fence answer. It means a
		// person got there first only when the run no longer awaits the engine's resume.
		if err != nil && ctx.Err() == nil && (!isRunConflict(err) || m.stillAwaitsEngineResume(ctx, tenant, runRef)) {
			m.warnf("sessions: the engine's resume of a run did not complete; a run still stopped is tried again at the next start or a person resumes it",
				"tenant", tenant.String(), "run_ref", runRef, "err", redactErr(err))
		}
	}
	if waiting > 0 {
		m.warnf("sessions: runs the engine stopped cannot resume by themselves and stay stopped until a person resumes them or deletes them; debug logging names each",
			"tenant", tenant.String(), "runs", waiting)
	}
}

// stillAwaitsEngineResume reports whether the run is still stopped with the engine's reason,
// true when it cannot be read: a refusal must then be logged, never taken for a person's.
func (m *Module) stillAwaitsEngineResume(ctx context.Context, tenant model.TenantID, runRef string) bool {
	d, err := m.getRun(ctx, tenant, runRef)
	return err != nil || (d.State == stateStopped && endedByTheEngine(d.Reason))
}

// engineStoppedRuns reads the tenant's stopped runs whose reason is the engine's own: the
// clean stop in full, and the crash by its opening (the detail after it varies). The store's
// LIKE may be case-insensitive, so endedByTheEngine has the last word on every row. A listing
// that fails returns the rows read before it with its error.
func (m *Module) engineStoppedRuns(ctx context.Context, tenant model.TenantID) ([]model.Record, error) {
	var out []model.Record
	var errs []error
	for _, reason := range []model.Filter{
		eq(colReason, engineStoppedReason),
		{Column: colReason, Op: model.OpLike, Value: engineDiedReason + "%"},
	} {
		rows, err := m.stoppedRunsWhere(ctx, tenant, reason)
		errs = append(errs, err)
		for _, row := range rows {
			if endedByTheEngine(row.String(colReason)) {
				out = append(out, row)
			}
		}
	}
	return out, errors.Join(errs...)
}

func (m *Module) stoppedRunsWhere(ctx context.Context, tenant model.TenantID, reason model.Filter) ([]model.Record, error) {
	q := model.Query{Limit: 200, Filters: []model.Filter{eq(colState, stateStopped), reason}}
	var out []model.Record
	for {
		var rows []model.Record
		var page model.Page
		if err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			rows, page, err = repo.List(ctx, q)
			return err
		}); err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		if q.Cursor == page.Cursor {
			return nil, errors.New("engine-stopped run listing cursor did not advance")
		}
		q.Cursor = page.Cursor
	}
}

// bindResumeCredential records, for the engine's own resume, a verifier fingerprint of the
// credential that launched the run: references only, never bearer material. A launch whose
// credential cannot be bound still starts; that run just does not resume by itself.
func (m *Module) bindResumeCredential(ctx context.Context, runRef string, c auth.QueuedCredential) auth.QueuedCredential {
	if c.ID.IsZero() || c.Seal != "" || m.rt.QueuedCredentialCapture == nil {
		return c
	}
	bound, err := m.rt.QueuedCredentialCapture(ctx, c)
	if err != nil {
		m.warnf("sessions: the launcher's credential could not be kept for an automatic resume; this run will not resume by itself",
			"run_ref", runRef, "err", redactErr(err))
		return c
	}
	return bound
}

// resumeCredentialOf reads the credential a run was launched with, false when it records none.
func resumeCredentialOf(row model.Record) (auth.QueuedCredential, bool) {
	c := auth.QueuedCredential{
		ID: model.ID(row.String(colRunResumeCredentialID)), UserID: model.ID(row.String(colRunLauncherUserID)),
		Kind: auth.PrincipalKind(row.String(colRunResumeCredentialKind)), Version: row.Int(colRunResumeCredentialVersion),
		Seal: row.String(colRunResumeCredentialSeal),
	}
	return c, !c.ID.IsZero() && !c.UserID.IsZero() && c.Seal != ""
}
