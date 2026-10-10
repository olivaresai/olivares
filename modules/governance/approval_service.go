// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// EngineApprovals is the one in-process approval service. Request, Wait,
// Decide and Cancel share the existing queue and audited transactions with REST. Decide
// reauthenticates the human and checks the same composed permission authority.
// Never expose this engine-authoritative port to an external caller.
type EngineApprovals struct{ module *Module }

func (m *Module) EngineApprovals() *EngineApprovals { return &EngineApprovals{module: m} }

type ApprovalRequest = createApprovalRequest
type Approval = approvalDTO

type approvalData interface {
	View(context.Context, func(store.Scope) error) error
	Mutate(context.Context, func(store.Scope) error) error
}

type approvalTenantData struct {
	data   api.ModuleData
	tenant model.TenantID
}

func (d approvalTenantData) View(ctx context.Context, f func(store.Scope) error) error {
	return d.data.View(ctx, d.tenant, f)
}
func (d approvalTenantData) Mutate(ctx context.Context, f func(store.Scope) error) error {
	return d.data.Mutate(ctx, d.tenant, f)
}

func (a *EngineApprovals) scope(tenant model.TenantID) (approvalTenantData, api.ModuleContext, error) {
	if a == nil || a.module == nil || a.module.data == nil || tenant.IsZero() || tenant.IsSystem() {
		return approvalTenantData{}, api.ModuleContext{}, errors.New("approval service unavailable")
	}
	p, err := auth.NewSystemOperator("engine:approval-proposer", "governed engine approval service")
	return approvalTenantData{a.module.data, tenant}, api.ModuleContext{Tenant: tenant, Principal: p}, err
}

func (a *EngineApprovals) Request(ctx context.Context, tenant model.TenantID, principal auth.Principal, in ApprovalRequest) (Approval, error) {
	requester, authenticated := api.RequestPrincipal(ctx)
	ctx = api.DetachRequestContext(ctx)
	d, mc, err := a.scope(tenant)
	if err != nil {
		return Approval{}, err
	}
	if err := normalizeApprovalRequest(&in); err != nil {
		return Approval{}, err
	}
	if in.SessionRef != "" && principal.SessionIdentity == "" {
		return Approval{}, errors.New("approval session principal is required")
	}
	actor, actorKind := mc.Principal.Actor(), mc.Principal.ActorKind()
	requesterUser := ""
	if principal.SessionIdentity != "" {
		if principal.SessionIdentity != in.SessionRef || (!principal.SessionScope().IsZero() && principal.SessionScope() != tenant) {
			return Approval{}, errors.New("approval session scope mismatch")
		}
		// The launcher remains the authority ceiling, not the agent's person.
		// Separation of duty compares humans; the session proposes as itself.
		actor, actorKind = "session:"+principal.SessionIdentity, model.ActorAgent
	} else if !principal.UserID.IsZero() {
		return Approval{}, errors.New("an engine approval must be proposed by the session or automation")
	} else if authenticated && !requester.UserID.IsZero() {
		// Keep the initiating person for separation of duty. This is attribution,
		// not inherited authority: the transaction still uses the detached engine
		// context, and session proposals above remain attributed to the session.
		actor, actorKind, requesterUser = requester.Actor(), requester.ActorKind(), requester.UserID.String()
	}
	capacity, err := a.module.sessionApprovalQuorum(ctx, tenant, maxApprovalCount)
	if err != nil {
		return Approval{}, err
	}
	var out Approval
	err = d.Mutate(ctx, func(sc store.Scope) error {
		var err error
		out, err = a.module.openApprovalRecord(ctx, sc, actor, actorKind, requesterUser, in, 0, a.module.clock.Now(), capacity)
		return err
	})
	if err == nil {
		a.module.emitApprovalRequested(ctx, tenant, out)
	}
	return out, err
}

func (a *EngineApprovals) Read(ctx context.Context, tenant model.TenantID, ref string) (Approval, error) {
	ctx = api.DetachRequestContext(ctx)
	d, _, err := a.scope(tenant)
	if err != nil {
		return Approval{}, err
	}
	var out Approval
	err = d.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(ref))
		if err != nil {
			return err
		}
		pols, err := loadApprovalPolicies(ctx, sc)
		if err != nil {
			return err
		}
		out = toApprovalDTO(rec, a.module.clock.Now(), liveRiskTier(pols, rec))
		return nil
	})
	return out, err
}

func (a *EngineApprovals) List(ctx context.Context, tenant model.TenantID, action, status, cursor string) ([]Approval, model.Page, error) {
	ctx = api.DetachRequestContext(ctx)
	d, _, err := a.scope(tenant)
	if err != nil {
		return nil, model.Page{}, err
	}
	items := []Approval{}
	var page model.Page
	err = d.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		q := model.Query{Limit: 200, Cursor: cursor, Filters: []model.Filter{eq(colAction, action)}}
		now := a.module.clock.Now()
		pols, err := loadApprovalPolicies(ctx, sc)
		if err != nil {
			return err
		}
		rows, p, err := listEffectiveApprovals(ctx, repo, q, status, now, pols)
		if err != nil {
			return err
		}
		for _, r := range rows {
			items = append(items, toApprovalDTO(r, now, liveRiskTier(pols, r)))
		}
		page = p
		return nil
	})
	return items, page, err
}

func (a *EngineApprovals) Consume(ctx context.Context, tenant model.TenantID, ref, consumerID, policyVersion string) (ApprovalConsumption, error) {
	ctx = api.DetachRequestContext(ctx)
	d, mc, err := a.scope(tenant)
	if err != nil {
		return ApprovalConsumption{}, err
	}
	consumerID = strings.TrimSpace(consumerID)
	if consumerID == "" || len(consumerID) > maxMatchLen || len(policyVersion) > maxMatchLen || containsInlineCredential(consumerID) || containsInlineCredential(policyVersion) {
		return ApprovalConsumption{}, errors.New("invalid approval consumer")
	}
	return a.module.consumeApproval(ctx, d, mc, model.ID(ref), consumeApprovalRequest{ConsumerID: consumerID, PolicyVersion: policyVersion})
}

func (a *EngineApprovals) ConsumeEmergency(ctx context.Context, tenant model.TenantID, action, subjectKind, subjectRef string) (EmergencyConsumption, error) {
	ctx = api.DetachRequestContext(ctx)
	d, mc, err := a.scope(tenant)
	if err != nil {
		return EmergencyConsumption{}, err
	}
	guard := ApprovalRequest{Action: action, SubjectKind: subjectKind, SubjectRef: subjectRef}
	if err := normalizeApprovalRequest(&guard); err != nil {
		return EmergencyConsumption{}, err
	}
	return a.module.consumeEmergency(ctx, d, mc, consumeBreakGlassRequest{Action: guard.Action, SubjectKind: guard.SubjectKind, SubjectRef: guard.SubjectRef})
}

// Wait observes a queued request until a human decision, cancellation or expiry.
// The caller's deadline/cancellation always bounds the wait; no goroutine survives it.
func (a *EngineApprovals) Wait(ctx context.Context, tenant model.TenantID, ref string) (out Approval, err error) {
	ctx = api.DetachRequestContext(ctx)
	// Every return caused by expiry commits the terminal state before the caller
	// refuses its action. Live cancellation remains the exact-requester Cancel path.
	defer func() {
		deadline := errors.Is(ctx.Err(), context.DeadlineExceeded)
		if !deadline && (err != nil || out.Status != statusExpired) {
			return
		}
		if deadline {
			err = errors.Join(err, ctx.Err())
		}
		terminal, persistErr := a.persistWaitExpiry(ctx, tenant, ref, deadline)
		if persistErr != nil {
			err = errors.Join(err, persistErr)
			return
		}
		out = terminal
	}()
	timer := time.NewTicker(250 * time.Millisecond)
	defer timer.Stop()
	for {
		out, err := a.Read(ctx, tenant, ref)
		if err != nil {
			return Approval{}, err
		}
		if out.Status != statusPending {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return Approval{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// UseApprovalAuthority binds the existing authenticator/authorizer, never another
// identity service. An unwired in-process Decide refuses; REST retains its edge gate.
func (m *Module) UseApprovalAuthority(authenticator *auth.Authenticator, authorizer *auth.Authorizer) {
	m.approvalAuthenticator, m.approvalAuthorizer = authenticator, authorizer
}

func (a *EngineApprovals) Decide(ctx context.Context, tenant model.TenantID, principal auth.Principal, ref string, in ApprovalDecisionRequest) (Approval, error) {
	ctx = api.DetachRequestContext(ctx)
	d, mc, err := a.scope(tenant)
	if err != nil {
		return Approval{}, err
	}
	if a.module.approvalAuthenticator == nil || a.module.approvalAuthorizer == nil {
		return Approval{}, errors.New("approval decision authority unavailable")
	}
	queued, ok := auth.QueuedCredentialFrom(principal)
	if !ok {
		return Approval{}, auth.ErrUnauthenticated
	}
	credential, err := a.module.approvalAuthenticator.BindQueuedCredential(ctx, queued)
	if err != nil {
		return Approval{}, err
	}
	current, err := a.module.approvalAuthenticator.RevalidateQueuedCredential(ctx, credential)
	if err != nil {
		return Approval{}, err
	}
	ctx = auth.WithStepUpSource(ctx, a.module.approvalAuthenticator.CurrentStepUp)
	_ = auth.StepUpPolicyFrom(ctx) // Resolve before SQLite opens the decision writer.
	decision := a.module.approvalAuthorizer.Authorize(ctx, auth.Request{Principal: current, Tenant: tenant, Permission: permApprovalAdmin, Resource: auth.ResourceFor(permApprovalAdmin)})
	if !decision.Allow {
		return Approval{}, approvalDecisionError{Status: http.StatusForbidden, Message: "approval decision is not authorized"}
	}
	mc.Principal = current
	return a.module.decideApproval(ctx, d, mc, model.ID(ref), in)
}

// ReviewPolicy answers whether an authored, enabled approval policy asks for
// human review. It uses the same ordered policy match as Request; unavailable
// or unreadable policy state is an error, never an implicit allow.
func (a *EngineApprovals) ReviewPolicy(ctx context.Context, tenant model.TenantID, action, subjectKind string) (policyRef string, err error) {
	ctx = api.DetachRequestContext(ctx)
	d, _, err := a.scope(tenant)
	if err != nil {
		return "", err
	}
	err = d.View(ctx, func(sc store.Scope) error {
		policies, err := loadApprovalPolicies(ctx, sc)
		if err != nil {
			return err
		}
		for _, policy := range policies {
			if _, err := parseApprovalSpec(policy.Spec); err != nil {
				return errors.New("approval policy is unreadable")
			}
		}
		policyRef, _, _ = matchApprovalSpec(policies, action, subjectKind)
		return nil
	})
	return policyRef, err
}

// Cancel lets a resolved session withdraw its own pending request. It shares
// REST's audited transition; a terminal request returns its unchanged verdict
// and a conflict error. The launching human's administrator role is not used.
func (a *EngineApprovals) Cancel(ctx context.Context, tenant model.TenantID, principal auth.Principal, ref string) (Approval, error) {
	ctx = api.DetachRequestContext(ctx)
	d, mc, err := a.scope(tenant)
	if err != nil {
		return Approval{}, err
	}
	if principal.SessionIdentity == "" || principal.SessionScope() != tenant {
		return Approval{}, auth.ErrUnauthenticated
	}
	mc.Principal = principal
	return a.module.cancelApproval(ctx, d, mc, model.ID(ref))
}
