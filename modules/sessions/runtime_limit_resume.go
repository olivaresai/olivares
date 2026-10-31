// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const colRunResumeAt = "resume_at"
const colLimitResumeUser = "limit_resume_user_id"
const colLimitResumeCredentialID = "limit_resume_credential_id"
const colLimitResumeCredentialKind = "limit_resume_credential_kind"
const colLimitResumeCredentialVersion = "limit_resume_credential_version"
const colLimitResumeCredentialSeal = "limit_resume_credential_seal"
const colRunResumeDismissed = "resume_limit_dismissed"
const colRunResumeCancelled = "resume_cancelled"
const colRunResumeAfterLimit = "resume_after_limit"
const limitResumeMargin = 5 * time.Second
const limitResumeMessage = "[Scheduled resume] Continue the interrupted work."

type limitResumeKey struct{}
type limitResumeAttempt struct{ at string }

func (m *Module) scheduleLimitResume(ctx context.Context, tenant model.TenantID, ref string, principal auth.Principal, credential auth.QueuedCredential) (runDTO, error) {
	release, err := m.rt.lockRunContext(ctx, liveKey(tenant, ref))
	if err != nil {
		return runDTO{}, err
	}
	defer release()
	return m.scheduleLimitResumeLoaded(ctx, tenant, ref, principal, credential)
}

func (m *Module) scheduleLimitResumeLoaded(ctx context.Context, tenant model.TenantID, ref string, principal auth.Principal, credential auth.QueuedCredential) (runDTO, error) {
	rec, err := m.loadRun(ctx, tenant, ref)
	if err != nil {
		return runDTO{}, err
	}
	if err := m.refuseLegacyControlUnderWork(ctx, tenant, rec); err != nil {
		return runDTO{}, err
	}
	fact := storedRunLimit(rec)
	if fact == nil || fact.Kind != limitKindUsage {
		return runDTO{}, conflictErr("this session has no usage reset")
	}
	reset, err := time.Parse(time.RFC3339, fact.ResetsAt)
	if err != nil || !reset.After(m.now()) {
		return runDTO{}, conflictErr("this session has no future usage reset")
	}
	if rec.String(colState) != stateStopped && rec.String(colState) != stateFailed && rec.String(colState) != stateRunning {
		return runDTO{}, conflictErr("stop the session before scheduling its resume")
	}
	if rec.String(colRunArchivedAt) != "" || rec.String(colRunAgentRef) != "" {
		return runDTO{}, conflictErr("this session cannot resume automatically")
	}
	if principal.UserID.IsZero() || credential.UserID != principal.UserID || m.rt.QueuedCredentialCapture == nil || m.rt.QueuedLaunchAuthorization == nil {
		return runDTO{}, forbiddenErr("sign in with a credential that can authorize a scheduled resume")
	}
	credential, err = m.rt.QueuedCredentialCapture(ctx, credential)
	if err != nil {
		return runDTO{}, err
	}
	if credential.Seal == "" {
		return runDTO{}, forbiddenErr("the scheduling credential could not be kept")
	}
	if fact.AccountRef != "" && fact.AccountRef != rec.String(colRunProfileID) {
		return runDTO{}, conflictErr("the limited account changed")
	}
	if rec.String(colState) == stateRunning {
		resource := auth.ResourceAttrs{Kind: permRunRead.Resource(), ID: rec.String(model.ColID), WorkspaceID: model.ID(rec.String(colRunAuthzWorkspaceID))}
		if m.WorkAuthorizer == nil || !m.WorkAuthorizer.Authorize(ctx, auth.Request{Principal: principal, Tenant: tenant, Permission: permRunStop, Resource: resource}).Allow {
			return runDTO{}, forbiddenErr("stopping a live session for its usage reset requires stop permission")
		}
		if _, _, err := m.stopRunLoaded(ctx, tenant, ref, principal.Actor(), principal.ActorKind(), "waiting for usage reset", rec); err != nil {
			return runDTO{}, err
		}
	}
	stamp := model.NewTimestamp(reset.Add(limitResumeMargin)).String()
	if rec.String(colRunResumeAt) == stamp {
		return m.toRunDTO(rec), nil
	}
	updated, err := m.transition(withQueuedLaunchUser(ctx, credential.UserID), tenant, ref, transitionInput{event: "resume_scheduled", actor: principal.Actor(), actorKind: principal.ActorKind(), keepReason: true, detail: "resumes after usage reset", mutate: func(r model.Record) {
		r[colRunResumeCancelled] = false
		r[colRunResumeAfterLimit] = true
		r[colRunResumeDismissed] = false
		r[colRunResumeAt] = stamp
		r[colLimitResumeCredentialID] = credential.ID.String()
		r[colLimitResumeCredentialKind] = string(credential.Kind)
		r[colLimitResumeCredentialVersion] = credential.Version
		r[colLimitResumeCredentialSeal] = credential.Seal
		r[colLimitResumeUser] = credential.UserID.String()
	}})
	return m.toRunDTO(updated), err
}

func (m *Module) cancelLimitResume(ctx context.Context, tenant model.TenantID, ref, actor, kind string) error {
	rec, err := m.loadRun(ctx, tenant, ref)
	if err != nil {
		return err
	}
	if rec.String(colRunResumeAt) == "" && (storedRunLimit(rec) == nil || rec.Bool(colRunResumeDismissed)) {
		return nil
	}
	_, err = m.transition(ctx, tenant, ref, transitionInput{event: "resume_cancelled", actor: actor, actorKind: kind, keepReason: true, detail: "scheduled resume cancelled", mutate: func(r model.Record) {
		r[colRunResumeCancelled] = r.String(colRunResumeAt) != "" || r.Bool(colRunResumeAfterLimit)
		r[colRunResumeAt] = nil
		r[colRunResumeDismissed] = true
		r[colRunResumeAfterLimit] = false
	}})
	return err
}

func (m *Module) sweepLimitResumes(ctx context.Context, tenants []model.TenantID) {
	for _, tenant := range tenants {
		m.scheduleAutomaticLimitResumes(ctx, tenant)
		q := model.Query{Limit: 100, Filters: []model.Filter{{Column: colRunResumeAt, Op: model.OpLte, Value: model.NewTimestamp(m.now()).String()}}}
		for {
			var rows []model.Record
			var page model.Page
			err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
				repo, e := sc.Ext(runKind)
				if e != nil {
					return e
				}
				rows, page, e = repo.List(ctx, q)
				return e
			})
			if err != nil {
				m.warnf("sessions: scheduled resumes could not be read", "err", redactErr(err))
				break
			}
			for _, row := range rows {
				if ctx.Err() != nil {
					return
				}
				if err := m.resumeLimitedRun(ctx, tenant, row); err != nil && ctx.Err() == nil {
					m.warnf("sessions: scheduled resume remains stopped", "run_ref", row.String(colRunRef), "err", redactErr(err))
				}
			}
			if !page.HasMore || page.Cursor == "" || page.Cursor == q.Cursor {
				break
			}
			q.Cursor = page.Cursor
		}
	}
}

func (m *Module) resumeLimitedRun(ctx context.Context, tenant model.TenantID, row model.Record) error {
	ref := row.String(colRunRef)
	if row.String(colState) != stateStopped && row.String(colState) != stateFailed {
		return nil
	}
	credential, ok := limitResumeCredentialOf(row)
	if !ok || m.rt.QueuedLaunchAuthorization == nil {
		return forbiddenErr("scheduled resume credential unavailable")
	}
	principal, err := m.rt.QueuedLaunchAuthorization(ctx, tenant, credential, row.String(model.ColID), model.ID(row.String(colRunAuthzWorkspaceID)))
	if err != nil {
		return err
	}
	if principal.UserID != credential.UserID {
		return forbiddenErr("scheduled resume owner changed")
	}
	// The port reads the same bound account's current allowance, never a different account.
	if m.rt.LimitResets != nil && m.usesOwnToolLogin(tenant, row.String(colRunProfileDriver), row.String(colRunProviderAuthSource), row.String(colRunProfileConfigHome)) {
		at, limited := m.rt.LimitResets(ctx, tenant, driverfacts.OlivaresLoginInstance(row.String(colRunProfileDriver)))
		if limited && at.After(m.now()) {
			_, err = m.transition(withQueuedLaunchUser(ctx, credential.UserID), tenant, ref, transitionInput{event: "resume_rescheduled", actor: principal.Actor(), actorKind: principal.ActorKind(), keepReason: true, detail: "account is still limited; waiting for its next reset", guard: func(r model.Record) error {
				if r.String(colRunResumeAt) != row.String(colRunResumeAt) {
					return conflictErr("scheduled resume changed")
				}
				return nil
			}, mutate: func(r model.Record) { r[colRunResumeAt] = model.NewTimestamp(at.Add(limitResumeMargin)).String() }})
			return err
		}
	}
	ctx = context.WithValue(withQueuedLaunchUser(withEngineResume(ctx, principal), principal.UserID), limitResumeKey{}, limitResumeAttempt{at: row.String(colRunResumeAt)})
	_, err = m.resumeRun(ctx, tenant, ref, principal.Actor(), principal.ActorKind(), "")
	return err
}

func (m *Module) startLimitResumeWorker(tenants []model.TenantID) {
	m.rt.mu.Lock()
	if m.rt.approvalWorkersStopped || m.rt.limitResumeCancel != nil {
		m.rt.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.rt.limitResumeCancel = cancel
	m.rt.limitResumeWG.Add(1)
	m.rt.mu.Unlock()
	go func() {
		defer m.rt.limitResumeWG.Done()
		defer cancel()
		timer := time.NewTicker(5 * time.Second)
		defer timer.Stop()
		for {
			m.sweepLimitResumes(ctx, tenants)
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if m.rt.ApprovalRecoveryTenants != nil {
					current, err := m.rt.ApprovalRecoveryTenants(ctx)
					if err != nil {
						m.warnf("sessions: scheduled resume tenants could not be refreshed", "err", redactErr(err))
						continue
					}
					tenants = current
				}
			}
		}
	}()
}
func (m *Module) stopLimitResumeWorker() {
	m.rt.mu.Lock()
	cancel := m.rt.limitResumeCancel
	m.rt.limitResumeCancel = nil
	m.rt.mu.Unlock()
	if cancel != nil {
		cancel()
		m.rt.limitResumeWG.Wait()
	}
}

// A preference applies on the server to the person's sessions, including while all
// browsers are closed. Cancellation suppresses this particular limit occurrence.
func (m *Module) scheduleAutomaticLimitResumes(ctx context.Context, tenant model.TenantID) {
	q := model.Query{Limit: 100, Filters: []model.Filter{{Column: colRunUsageLimit, Op: model.OpNotNull}, {Column: colRunResumeAt, Op: model.OpIsNull}, {Column: colRunResumeDismissed, Op: model.OpEqOrUnset, Value: false}}}
	for {
		var rows []model.Record
		var page model.Page
		err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			rows, page, err = repo.List(ctx, q)
			return err
		})
		if err != nil {
			m.warnf("sessions: automatic usage resets could not be read", "err", redactErr(err))
			return
		}
		for _, rec := range rows {
			if ctx.Err() != nil {
				return
			}
			fact := storedRunLimit(rec)
			if fact == nil || fact.Kind != limitKindUsage || rec.String(colRunArchivedAt) != "" || rec.String(colRunAgentRef) != "" {
				continue
			}
			at, err := time.Parse(time.RFC3339, fact.ResetsAt)
			if err != nil || !at.After(m.now()) {
				continue
			}
			credential, ok := resumeCredentialOf(rec)
			if rec.Bool(colRunResumeAfterLimit) {
				credential, ok = limitResumeCredentialOf(rec)
			}
			if !ok || m.rt.QueuedLaunchAuthorization == nil {
				continue
			}
			enabled, err := m.automaticLimitResume(ctx, tenant, credential.UserID)
			if err != nil {
				m.warnf("sessions: automatic resume preference unreadable", "err", redactErr(err))
				continue
			}
			if !enabled && !rec.Bool(colRunResumeAfterLimit) {
				continue
			}
			principal, err := m.rt.QueuedLaunchAuthorization(ctx, tenant, credential, rec.String(model.ColID), model.ID(rec.String(colRunAuthzWorkspaceID)))
			if err != nil {
				continue
			}
			ref := rec.String(colRunRef)
			release, err := m.rt.lockRunContext(ctx, liveKey(tenant, ref))
			if err != nil {
				return
			}
			current, err := m.loadRun(ctx, tenant, ref)
			if err == nil && !current.Bool(colRunResumeDismissed) && current.String(colRunUsageLimit) == rec.String(colRunUsageLimit) && current.String(colRunResumeAt) == "" {
				_, err = m.scheduleLimitResumeLoaded(ctx, tenant, ref, principal, credential)
			}
			release()
			if err != nil {
				m.warnf("sessions: automatic usage resume could not be scheduled", "run_ref", ref, "err", redactErr(err))
			}
		}
		if !page.HasMore || page.Cursor == "" || page.Cursor == q.Cursor {
			return
		}
		q.Cursor = page.Cursor
	}
}
func (m *Module) cancelLimitResumeForInput(ctx context.Context, tenant model.TenantID, ref string) error {
	actor, kind := model.ActorSystem, model.ActorSystem
	if principal, ok := api.RequestPrincipal(ctx); ok {
		actor, kind = principal.Actor(), principal.ActorKind()
	}
	return m.cancelLimitResume(ctx, tenant, ref, actor, kind)
}

func limitResumeCredentialOf(row model.Record) (auth.QueuedCredential, bool) {
	credential := auth.QueuedCredential{ID: model.ID(row.String(colLimitResumeCredentialID)), UserID: model.ID(row.String(colLimitResumeUser)), Kind: auth.PrincipalKind(row.String(colLimitResumeCredentialKind)), Version: row.Int(colLimitResumeCredentialVersion), Seal: row.String(colLimitResumeCredentialSeal)}
	return credential, !credential.ID.IsZero() && !credential.UserID.IsZero() && credential.Seal != ""
}
