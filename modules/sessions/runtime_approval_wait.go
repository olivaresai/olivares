// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type waitingApprovalError struct{ ref string }

func (e *waitingApprovalError) Error() string { return "waiting for approval: " + e.ref }
func approvalURL(ref string) string {
	if ref == "" {
		return ""
	}
	return "/v1/m/governance/approvals/" + ref
}

// RecoverWaitingLaunches re-arms readable waiting runs. An unreadable question
// stays waiting for repair and is logged without blocking the tenant's other runs.
func (m *Module) RecoverWaitingLaunches(ctx context.Context, tenant model.TenantID) error {
	q := model.Query{Limit: 200, Filters: []model.Filter{eq(colState, stateWaitingApproval)}}
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
			return err
		}
		for _, row := range rows {
			var intent LaunchIntent
			if err := json.Unmarshal([]byte(row.String(colRunQueuedIntent)), &intent); err != nil {
				m.warnf("waiting launch question is unreadable; leaving this launch waiting",
					"tenant", tenant.String(), "run_ref", row.String(colRunRef))
				continue
			}
			intent.Actor = row.String(colRunQueuedActor)
			intent.ActorKind = row.String(colRunQueuedActorKind)
			intent.AgentRef = row.String(colRunAgentRef)
			m.watchApproval(tenant, row.String(colRunRef), intent)
		}
		if !page.HasMore || page.Cursor == "" {
			return nil
		}
		if q.Cursor == page.Cursor {
			return errors.New("waiting launch recovery cursor did not advance")
		}
		q.Cursor = page.Cursor
	}
}

// Every worker owns a durable run, never a request context. Reading approval
// status conveys no authority; the approved continuation re-enters admission.
func (m *Module) watchApproval(tenant model.TenantID, runRef string, intent LaunchIntent) {
	reader, ok := m.rt.LaunchGate.(LaunchApprovalReader)
	if !ok {
		m.warnf("waiting launch has no approval status reader", "run_ref", runRef)
		return
	}
	key := liveKey(tenant, runRef)
	m.rt.mu.Lock()
	if m.rt.approvalWorkersStopped {
		m.rt.mu.Unlock()
		return
	}
	if m.rt.approvalWorkers == nil {
		m.rt.approvalWorkers = map[string]context.CancelFunc{}
	}
	if _, ok := m.rt.approvalWorkers[key]; ok {
		m.rt.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.rt.approvalWorkers[key] = cancel
	m.rt.approvalWG.Add(1)
	m.rt.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			m.rt.mu.Lock()
			delete(m.rt.approvalWorkers, key)
			m.rt.mu.Unlock()
			m.rt.approvalWG.Done()
		}()
		timer := time.NewTicker(250 * time.Millisecond)
		defer timer.Stop()
		warned := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			row, err := m.loadRun(ctx, tenant, runRef)
			if err != nil {
				if !warned {
					m.warnf("waiting launch cannot be read", "run_ref", runRef)
					warned = true
				}
				continue
			}
			if row.String(colState) != stateWaitingApproval {
				return
			}
			ref := row.String(colApprovalRef)
			status, err := reader.ApprovalStatus(ctx, tenant, intent, ref)
			if err != nil {
				if !warned {
					m.warnf("waiting launch approval temporarily unavailable", "run_ref", runRef)
					warned = true
				}
				continue
			}
			warned = false
			switch status {
			case "approved", "break_glass":
				_, err := m.resumeRunInternal(ctx, tenant, runRef, "", "", "", true, callerAsks{})
				if err == nil {
					return
				}
				if retryableApprovalWait(err) {
					continue
				}
				if ctx.Err() != nil {
					return
				} // shutdown preserves the waiting run for recovery
				// A fresh gate refusal ends the queued attempt; it cannot silently open a
				// different approval or execute a changed plan under the original grant.
				reason := "approved launch refused by current launch gates"
				var refusal *runErr
				if errors.As(err, &refusal) && refusal.status < http.StatusInternalServerError {
					// The engine's own sentence says why and what to do (no secret value).
					reason = "approved launch refused: " + refusal.msg
				}
				if m.finishWaitingLaunch(ctx, tenant, runRef, stateDeclined, reason) {
					return
				}
			case "rejected", "canceled":
				if m.finishWaitingLaunch(ctx, tenant, runRef, stateDeclined, "human review "+status) {
					return
				}
			case "expired":
				if m.finishWaitingLaunch(ctx, tenant, runRef, stateExpired, "approval expired") {
					return
				}
			case "pending":
			default:
				if m.finishWaitingLaunch(ctx, tenant, runRef, stateDeclined, "approval is no longer valid") {
					return
				}
			}
		}
	}()
}

func (m *Module) finishWaitingLaunch(ctx context.Context, tenant model.TenantID, ref, state, reason string) bool {
	_, err := m.transition(ctx, tenant, ref, transitionInput{event: state, toState: state, actor: "engine:approval-waiter", actorKind: model.ActorSystem, detail: reason, mutate: func(r model.Record) { r[colReason] = reason; r[colStoppedAt] = model.NewTimestamp(m.now()).String() }})
	if err != nil && !isRunConflict(err) {
		m.warnf("waiting launch terminal transition failed", "run_ref", ref)
	}
	return err == nil || isRunConflict(err)
}

func (m *Module) stopApprovalWorkers(ctx context.Context) {
	m.rt.mu.Lock()
	m.rt.approvalWorkersStopped = true
	for _, cancel := range m.rt.approvalWorkers {
		cancel()
	}
	m.rt.mu.Unlock()
	done := make(chan struct{})
	go func() { m.rt.approvalWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func clearQueuedLaunchAuthority(row model.Record) {
	for _, column := range []string{colRunQueuedUserID, colRunQueuedActor, colRunQueuedActorKind, colRunQueuedCredentialID, colRunQueuedCredentialKind, colRunQueuedCredentialVersion, colRunQueuedCredentialSeal, colRunQueuedIntent, colRunEnvAllow} {
		row[column] = nil
	}
}

type queuedLaunchUserKey struct{}

func withQueuedLaunchUser(ctx context.Context, user model.ID) context.Context {
	return context.WithValue(ctx, queuedLaunchUserKey{}, user)
}
func (m *Module) queuedLaunchMutate(ctx context.Context, tenant model.TenantID, write func(store.Scope) error) error {
	user, _ := ctx.Value(queuedLaunchUserKey{}).(model.ID)
	if user.IsZero() {
		return m.runtimeData(ctx).Mutate(ctx, tenant, write)
	}
	return auth.FencedWrite(ctx, m.standingFor(ctx), tenant, []model.ID{user}, auth.FenceDirectory, func(fn func(store.Scope) error) error { return m.runtimeData(ctx).Mutate(ctx, tenant, fn) }, func(sc store.Scope, _ bool) error { return write(sc) })
}

func retryableApprovalWait(err error) bool {
	var refusal *runErr
	if errors.As(err, &refusal) {
		return refusal.status >= http.StatusInternalServerError
	}
	return !errors.Is(err, store.ErrNotFound) && !errors.Is(err, ErrLeaseLost) && !errors.Is(err, ErrNoClaim)
}
