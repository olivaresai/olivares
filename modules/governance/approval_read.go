// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"sort"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// permUserRead is the permission that lets a caller read the tenant's people; the
// deciders of a request are people, so naming them needs it.
const permUserRead auth.Permission = "user:read"

// approvalReadDTO is one request as a reader sees it: the request, whether THIS reader
// may decide it, and, for a reader who may read users, who may. A console draws "needs
// you" and "who else can approve" from these two fields and never re-derives
// authorization from roles.
type approvalReadDTO struct {
	approvalDTO
	// MayDecide is true when the caller may record a decision on the request now: it is
	// open, the caller holds governance:approval:admin with a stable person behind it,
	// is not the requester's person and has not decided it. It is false on a resolved
	// request. It does not promise that approving succeeds: a step-up the caller can
	// still complete, a critical request a token can never step up for, a request the
	// caller launched under a launcher-must-differ policy (they may still reject it)
	// and the change-record gate can each refuse the approval the handler is asked for.
	MayDecide bool `json:"may_decide"`
	// Deciders are the people who may still record a decision on an open request,
	// never its requester and never anyone who already decided it. That includes a
	// platform superadmin with no membership in the tenant, who can decide in any
	// tenant. Absent for a caller who may not read users, for a workspace-confined
	// caller, where the server cannot enumerate people and on a resolved request;
	// absent means unknown, not nobody. Present and empty means nobody else can.
	Deciders []approvalPerson `json:"deciders,omitzero"`
}

type approvalPerson struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name,omitempty"`
}

// approvalReader answers the two questions of approvalReadDTO for one read request.
// It is built before the request's store transaction opens: enumerating people opens
// the auth store, which a tenant transaction on SQLite would hold up.
type approvalReader struct {
	caller    auth.PersonRef
	canDecide bool
	// holders are the people holding governance:approval:admin; nil unless the caller
	// may read users.
	holders []auth.Principal
}

func (m *Module) newApprovalReader(ctx context.Context, mc api.ModuleContext) approvalReader {
	// Reading who may decide is not an act of the caller: the checks below must not be
	// retained in the tenant's audit chain, and this read is polled by the console.
	ctx = auth.WithAuthorizationRecording(ctx, nil)
	rd := approvalReader{caller: auth.PersonRef{User: mc.Principal.UserID.String(), Actor: mc.Principal.Actor()}}
	rd.canDecide = !mc.Principal.UserID.IsZero() && mc.Admits(ctx, permApprovalAdmin, auth.ResourceFor(permApprovalAdmin))
	if m.approvalAuthenticator == nil || m.approvalAuthorizer == nil || !mc.Admits(ctx, permUserRead, auth.ResourceFor(permUserRead)) {
		return rd
	}
	// A workspace-confined caller must not enumerate the tenant's people (the members
	// roster applies the same rule).
	if _, confined := mc.Principal.ConfinedWorkspaceIn(mc.Tenant); confined {
		return rd
	}
	// AAL3: who could decide once any step-up is done, as the access review asks.
	// shortcut: one pass over the tenant's people per read; a tenant with thousands of
	// members wants the holder set cached for a few seconds.
	population, err := m.approvalAuthenticator.TenantPrincipals(ctx, mc.Tenant, auth.AAL3)
	if err != nil {
		// The roster is extra: a failure to list people must not take the queue down.
		// Deciders stay absent, which already means unknown.
		if m.log != nil {
			m.log.Warn("approval read: cannot list who may decide", "tenant", mc.Tenant.String(), "err", err)
		}
		return rd
	}
	rd.holders = []auth.Principal{}
	for _, p := range population {
		if p.Kind != auth.KindUser || p.UserID.IsZero() {
			continue
		}
		req := auth.Request{Principal: p, Tenant: mc.Tenant, Permission: permApprovalAdmin, Resource: auth.ResourceFor(permApprovalAdmin), Purpose: sdk.PurposeCurrentWhatIf}
		if m.approvalAuthorizer.Authorize(ctx, req).Allow {
			rd.holders = append(rd.holders, p)
		}
	}
	return rd
}

// view builds one request's read for the caller, inside the read's store scope.
func (rd approvalReader) view(ctx context.Context, sc store.Scope, rec model.Record, now model.Timestamp, dto approvalDTO) (approvalReadDTO, error) {
	out := approvalReadDTO{approvalDTO: dto}
	if effectiveStatus(rec, now) != statusPending {
		return out, nil
	}
	decRepo, err := sc.Ext(decisionKind)
	if err != nil {
		return out, err
	}
	decs, err := listAll(ctx, decRepo, eq(colApprovalID, rec.String(model.ColID)))
	if err != nil {
		return out, err
	}
	decided := make(map[string]bool, len(decs))
	for _, d := range decs {
		decided[d.String(colDeciderUser)] = true
	}
	out.MayDecide = rd.canDecide && !decided[rd.caller.User] && separationOfDutyRefusal(rec, rd.caller) == ""
	if rd.holders == nil {
		return out, nil
	}
	out.Deciders = []approvalPerson{}
	for _, p := range rd.holders {
		who := auth.PersonRef{User: p.UserID.String(), Actor: p.Actor()}
		if decided[who.User] || separationOfDutyRefusal(rec, who) != "" {
			continue
		}
		out.Deciders = append(out.Deciders, approvalPerson{UserID: who.User, DisplayName: p.DisplayName})
	}
	sort.Slice(out.Deciders, func(i, j int) bool {
		a, b := out.Deciders[i], out.Deciders[j]
		return a.DisplayName < b.DisplayName || a.DisplayName == b.DisplayName && a.UserID < b.UserID
	})
	return out, nil
}

// separationOfDutyRefusal is the decision handler's separation-of-duty rule: why the
// requester's person may not decide the request, or "" when decider may. The handler
// and the read share it, so what the read promises is what the handler does.
func separationOfDutyRefusal(rec model.Record, decider auth.PersonRef) string {
	requester := auth.PersonRef{User: rec.String(colRequestedByUser), Actor: rec.String(colRequestedBy)}
	ok, verdict := auth.TwoDistinctPeople(requester, decider, auth.AcceptWhenUndetermined)
	if ok {
		return ""
	}
	if verdict == auth.PersonSameCredential {
		return "separation of duty: this request was opened with the same credential you are deciding with, and no person stands behind it"
	}
	return "separation of duty: the requester cannot decide their own request"
}
