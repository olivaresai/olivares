// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// admit is the transport-free admission seam: it decides req for a principal already
// authenticated and a tenant already resolved, and returns the outcome without writing a
// response. A nil error admits; the witness is minted only on a governed route and stays
// zero on an ungoverned one, which authorizes nothing by construction. Every refusal is an
// error the transport renders: the route's own denial (errForbidden, or store.ErrNotFound
// when the route conceals), a step-up refusal (errors.Is auth.ErrStepUpRequired) or
// auth.ErrRouteUndecided. The REST adapter is authzTenantResourcePolicy; it keys Retry-After on
// auth.ErrRouteUndecided, so a route's denial must never wrap it. The gRPC adapter is
// grpcAuthorizeResource; grpcError renders the same errors as gRPC codes.
//
// ⛔ THIS USED TO SAY "THE ZERO METADATA IS BIT-FOR-BIT THE OLD BEHAVIOR". IT WAS TRUE AND
// STOPPED BEING TRUE IN THE SAME COMMIT THIS COMMENT SURVIVED, and the suite refuted it with
// FIFTY red tests serving 503 on ordinary routes: create an agent, list, audit.
//
// What the sentence claimed about the METADATA is still true: each arm of rbacPermitted only
// REMOVES the RBAC term, and the resolver consults SessionInheritsAgentGroups only when it is
// true, so the zero is inert. But the equivalence did not live there. It lived in both doors
// calling the SAME function, and switching that function to AuthorizeRoute moved the
// divergence to another axis: `Authorize` asks "do RBAC or a grant allow?", and
// `AuthorizeEvidence` also asks "is this principal backed by a sealed, windowed
// directory-epoch fact?" (evidence.go, principalAuthorizationEvidence). A principal without
// that fact yields CheckUnknown, not CheckBroken, and an unknown is not a denial: it is
// ErrRouteUndecided, that is 503 — the whole API down, not one permission short.
//
// ⇒ THE LESSON, which is the only thing that prevents a repeat: a comment that claims
// equivalence names the axis on which it checked it. This one named the metadata, stayed true
// about the metadata, and the system had diverged elsewhere. A claim of equivalence ages when
// EITHER of its two sides changes, not only the one it cites.
//
// ⛔ AND MinimumAAL IS CHECKED HERE, BEFORE THE DECISION, NOT AS A TERM OF THE ALGEBRA. A
// step-up is a precondition of authentication: folding it in would make "prove who you are
// again" indistinguishable from "you may not do this", two answers with different remedies.
// Checking it BEFORE also means a principal who must step up learns nothing about what waited
// behind it. AuthorizeRoute checks the same floor as its own precondition: two doors on
// purpose, one predicate (auth.RouteMetadata.RequiresStepUp).
func (s *Server) admit(ctx context.Context, req auth.Request, denial error, governed routeGovernance) (auth.RouteAuthorizationWitness, error) {
	var none auth.RouteAuthorizationWitness
	if denial == nil || denial == errForbidden {
		// A refusal must stay a refusal: a nil denial would read as an admission.
		// The plain one names the permission it lacked (#491); a concealing
		// route's store.ErrNotFound is kept exactly as the route chose it.
		denial = forbiddenFor(req.Permission, req.Tenant.IsSystem())
	}
	p := req.Principal
	// Dedicated orchestration collection authority is the immutable credential
	// workspace. The work service checks its live profile and confines every row.
	if p.IsOrchestrationSessionCredential() && req.Resource.ID == "" && req.Resource.WorkspaceID.IsZero() {
		switch req.Permission {
		case "sessions:work:read", "sessions:work:write", "sessions:decision:read", "sessions:decision:write":
			req.Resource.WorkspaceID = p.SessionWorkspaceID
		}
	}
	if req.Route.RequiresStepUp(ctx, p) {
		s.authz.RecordStepUpRefusal(ctx, req)
		return none, auth.StepUpRequiredFor(auth.StepUpPolicyFrom(ctx))
	}

	// ⛔ THE DOOR CHOOSES THE PATH, NOT THE METADATA. An UNGOVERNED route declared no policy at
	// all, so demanding the sealed evidence of the witness path imposes a requirement nobody
	// wrote for it — and its failure is not "one permission short", it is 503.
	//
	// ⛔ AND IT DOES NOT BRANCH ON req.Route.IsZero(), even though today that would separate the
	// same cases. That is the "a zero value turns the check off" family: a GOVERNED route whose
	// metadata ended up empty — through a refactor, a half-filled literal — would silently
	// degrade to the boolean path without any test noticing. Governance is a property of the
	// REGISTRATION GRAMMAR (invariant IV), where it cannot drift to zero on its own, and that is
	// why it travels as a parameter with its own type instead of being inferred from a value.
	if !governed {
		authorize := s.authz.Authorize
		if errors.Is(denial, store.ErrNotFound) && req.Permission.Verb() == auth.VerbRead {
			// A concealed read must do the same policy work for missing,
			// foreign and policy-hidden rows. Preserve every native deny while
			// evaluating the overlay once; ordinary reads and action gates
			// retain their authorization path, as does governed evidence below.
			authorize = s.authz.AuthorizeDisclosure
		}
		if dec := authorize(ctx, req); !dec.Allow {
			return none, denial
		}
		return none, nil
	}

	// ⛔ AuthorizeRoute AND NOT Authorize: THIS ROUTE MUST PRODUCE A WITNESS. With the boolean,
	// nothing the witness binds survived the decision, so none of its invariants reached the
	// HTTP effect (invariant V). Now the witness travels to the handler in the ModuleContext and
	// `CheckRowSet` can require that it answer THIS request's question.
	// Bound this evidence decision without imposing a lifetime on the handler
	// (which may stream). An existing earlier request deadline remains binding.
	decisionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	witness, aerr := s.authz.AuthorizeRoute(decisionCtx, req)
	switch {
	case aerr == nil:
		ownerScopedGrant := witness.UsesOwnerScopedGrant()
		if ownerScopedGrant || p.Superadmin {
			action, source := "auth.superadmin.tenant_entry", "superadmin_tenant_owner"
			if ownerScopedGrant {
				action, source = "auth.owner_scoped_grant", "tenant_owner"
			}
			err := s.st.Mutate(decisionCtx, req.Tenant, func(sc store.Scope) error {
				event, err := sc.Audit().Append(decisionCtx, model.AuditDraft{
					Actor: p.Actor(), ActorKind: p.ActorKind(), Action: action,
					Meta: map[string]any{"grant_source": source, "role": auth.RoleOwner, "permission": string(req.Permission),
						"cedar_action": witness.CedarAction.String(), "superadmin": p.Superadmin},
				})
				if err == nil && event.Seq == 0 {
					return store.ErrAuditSpoolFull
				}
				return err
			})
			if err != nil {
				s.log.Error("api: tenant owner admission audit unavailable", "err", err)
				return none, auth.ErrRouteUndecided
			}
		}
		return witness, nil

	// ⛔ STEP-UP KEEPS ITS OWN ANSWER: "prove who you are again" is not "you may not", and the
	// two answers have different remedies. It was already served this way before the witness path.
	case errors.Is(aerr, auth.ErrStepUpRequired):
		return none, auth.ErrStepUpRequired

	// ⛔ THE THIRD STATE, which did NOT EXIST on this path before the witness: a decision that
	// could not be established used to be served as a denial. It has its own shape (503) and
	// comes BEFORE any row read, so it cannot correlate with existence; and the concealing route
	// and the plain route answer the SAME, because an undecided that told them apart would be
	// exactly the oracle concealment exists to close.
	case errors.Is(aerr, auth.ErrRouteUndecided):
		return none, aerr

	// ⛔ AND A POLICY DENIAL STILL ANSWERS THE `denial` THE ROUTE PASSED. Replacing it with the
	// typed error would turn into 403 what is 404 today on every route with
	// ConcealDeniedAsNotFound — and that is not presentation: IT CONFIRMS THE ROW EXISTS. It
	// would cure the witness and open an existence oracle in the same commit.
	default:
		return none, denial
	}
}

// Admits is the in-handler form of the same question, for a caller already past its route
// (or with none) that only needs to know whether the principal holds a permission over a
// resource: an admin-only branch inside a handler, a tool list, a recorded actor flag. It
// asks admit as an ungoverned, non-concealing door, so the answer never rests on the rank of
// a membership role alone: scoped grants, the confinement and credential ceilings and the
// deny overlay all weigh in. A request that names no workspace is asked inside the
// principal's confining workspace (auth.Request.WithinConfinement), so a workspace-confined
// admin stays an admin there. Any refusal is false, and so is an evaluation error, which the
// authorizer fails closed: a caller cannot tell an outage from a denial.
func (s *Server) Admits(ctx context.Context, req auth.Request) bool {
	_, err := s.admit(ctx, req.WithinConfinement(), errForbidden, false)
	return err == nil
}
