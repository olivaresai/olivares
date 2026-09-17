// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// The CORE-ENTITY authorization seam (V269 / docs/contracts/COCKPIT-02-authz.md §3).
//
// ⛔ THE HOLE IT FILLS, measured before it was written. A module route authorizes
// against the STORED lineage of its target row, and it reaches that row through
// Scope.Ext — which REFUSES the `core` namespace outright
// (core/internal/store/sqlstore/scope.go, tenantScope.Ext) with the reason written
// beside it: "a module that holds a Scope must not be able to read or write
// engine-owned entities generically", and those entities include the credential
// tables. So today a module CANNOT authorize against a core.Session row, and an
// add-on whose whole authorization column is "the session row decides" has no floor
// to stand on.
//
// ⛔ AND THE CURE IS NOT TO OPEN Ext. That refusal is a control, not an omission:
// widening it to reach sessions would hand every module generic access to users and
// api_tokens to fix a case about sessions. Instead this seam is TYPED and SEALED —
// the module names a kind from a closed enum, the ENGINE reads the row with its own
// typed accessor, and what comes back is five facts. No repository, no column
// choice, no row.

// CoreKind names a CORE entity kind a module route may declare as its authorization
// resource. It is a closed enum on purpose: a module cannot name a kind the engine
// has not registered, so the reachable surface is decided here and not by whatever
// string a module puts in a struct field.
type CoreKind uint8

const (
	// CoreKindNone is the zero value: the route declares no core entity.
	CoreKindNone CoreKind = iota
	// CoreKindSession is core/model.Session — the row the session cockpit
	// authorizes on (COCKPIT-01: the SID is never the Cedar resource id).
	CoreKindSession
)

// String renders the kind for errors and evidence.
func (k CoreKind) String() string {
	switch k {
	case CoreKindSession:
		return "core.session"
	default:
		return "core.unknown"
	}
}

// valid reports whether k is a registered kind. CoreKindNone is NOT valid here: it
// means "no core entity declared", which callers check separately.
func (k CoreKind) valid() bool { return k == CoreKindSession }

// CoreEntityFacts is EVERYTHING the resolver returns about a core row.
//
// The list is short by design, and it is the whole point of the seam: an
// authorization decision needs the row's tenant and its lineage, and nothing
// else. Adding a field here widens what every module can read about a core entity,
// so a field is added only when a DECISION needs it — not when a handler would find
// it convenient.
type CoreEntityFacts struct {
	// ID is the row's id, echoed so a caller cannot mismatch request and answer.
	ID model.ID
	// Tenant is the tenant the row belongs to.
	Tenant model.TenantID
	// WorkspaceID is the workspace the row BELONGS to (zero = the tenant default).
	WorkspaceID model.ID
	// AgentID is the owning agent, when the kind has one. It is what makes the
	// row reachable from an AgentGroup grant (modules/governance/grants.go).
	AgentID model.ID
	// Exists distinguishes "no such row" from a zero-valued row. A caller that
	// read only the ids could not tell them apart, and conceal-404 depends on the
	// difference.
	Exists bool
}

// CoreEntityAuthorizationResolver reads the authorization facts of one core row.
//
// It is implemented by the COMPOSITION ROOT, over the engine's typed accessors, and
// handed to the server as an option. It is never handed to a module.
//
// An error means the fact could not be obtained, and the caller must treat that as
// UNAVAILABLE, never as absence — the same rule EntityRef.ConcealDeniedAsNotFound
// already states for its own path: "an unavailable authority fact must never be
// disguised as clean absence".
type CoreEntityAuthorizationResolver interface {
	ResolveCoreEntity(ctx context.Context, tenant model.TenantID, kind CoreKind, id model.ID) (CoreEntityFacts, error)
}

// validateCoreEntityRef checks one route's core-entity declaration AT MOUNT TIME.
//
// ⛔ AT MOUNT AND NOT PER REQUEST, and the precedent is in this package: an entity
// route that declares both IDParam and BodyIDField, or conceals without a Kind, is
// refused when it is registered (server.go entityResource). A misdeclared route that
// failed in flight would serve requests until somebody touched that path — which is
// the same as not checking.
func validateCoreEntityRef(ref EntityRef, resolver CoreEntityAuthorizationResolver) error {
	if ref.CoreKind == CoreKindNone {
		return nil
	}
	if ref.Kind != "" {
		return fmt.Errorf(
			"api: entity route declares both an external Kind (%q) and a CoreKind (%s); "+
				"they are mutually exclusive — a route authorizes against ONE row", ref.Kind, ref.CoreKind)
	}
	if !ref.CoreKind.valid() {
		return fmt.Errorf("api: entity route declares an unregistered CoreKind (%d)", uint8(ref.CoreKind))
	}
	if resolver == nil {
		return fmt.Errorf(
			"api: entity route declares CoreKind %s but no CoreEntityAuthorizationResolver is wired; "+
				"refusing to start rather than authorize such a route without its lineage", ref.CoreKind)
	}

	// ⛔ THE LOCATOR CHECK LIVES HERE NOW, AND IT USED TO LIVE ONLY IN RouteMetadata.Validate.
	// That put it on ONE of the two doors: HandleEntity uses this function as its whole mount
	// validation, so a route registered through it could declare a CoreKind with no way to
	// find its row - the very shape the policy door refuses - and the engine would deny every
	// caller BEFORE the handler, wearing the appearance of an ordinary authorization decision.
	// A guard that only one entrance runs is a guard on the entrance, not on the property.
	hasParam, hasBody := ref.IDParam != "", ref.BodyIDField != ""
	switch {
	case !hasParam && !hasBody:
		return fmt.Errorf(
			"api: entity route declares CoreKind %s and no IDParam or BodyIDField; the engine "+
				"cannot locate the row and would deny every caller before the handler runs",
			ref.CoreKind)
	case hasParam && hasBody:
		return fmt.Errorf(
			"api: entity route declares both IDParam %q and BodyIDField %q; two locators are two "+
				"answers to \"which row\", and the route means one", ref.IDParam, ref.BodyIDField)
	}
	return nil
}

// coreEntityResource resolves the authorization resource for a route that declared a
// CoreKind. Its five return values match entityResource's contract exactly:
//
//	(resource, tenant, found, locatorError, lineageError)
//
// The two error slots are NOT interchangeable, and keeping them apart is the whole
// honesty of this path: a locator error is the caller's malformed input, a lineage
// error is "I could not obtain the authority fact" — 503, never a clean 404.
func (cr chiRegistrar) coreEntityResource(
	r *http.Request,
	res auth.ResourceAttrs,
	ref EntityRef,
	id model.ID,
) (auth.ResourceAttrs, model.TenantID, bool, error, error) {
	// The resolver is verified present when the route mounts, so nil here would mean
	// the server was mutated after boot. Deny-closed rather than dereference.
	if cr.s.coreEntityResolver == nil {
		return res, "", false, nil, fmt.Errorf(
			"api: no core-entity resolver for a route declaring CoreKind %s", ref.CoreKind)
	}
	p, ok := principalFrom(r.Context())
	if !ok {
		return res, "", false, nil, nil // the shared authz path writes the 401
	}
	tenant, err := cr.s.resolveTenant(r, p)
	if err != nil {
		return res, "", false, nil, nil // likewise: let the shared path map the error
	}
	facts, err := cr.s.coreEntityResolver.ResolveCoreEntity(r.Context(), tenant, ref.CoreKind, id)
	if err != nil {
		// UNAVAILABLE, not absent. Returning (found=false, nil) here would let a
		// concealing route answer 404 — "there is no such session" — on the strength of
		// a store that would not answer, which is the exact substitution
		// EntityRef.ConcealDeniedAsNotFound forbids in its own doc.
		return res, tenant, false, nil, err
	}
	if !facts.Exists {
		// No row: stay at collection level and let the handler answer 404, which is
		// what the external-entity path does for store.ErrNotFound.
		return res, tenant, false, nil, nil
	}

	// ⛔ THE ECHO IS COMPARED, AND IT WAS NOT. CoreEntityFacts documents ID as an echo of the
	// id that was asked for, and Tenant as the row's owner, precisely so a resolver cannot
	// answer about a different row - and then nothing compared either one. This consumer read
	// Exists and copied workspace and agent, so a resolver returning another row's facts handed
	// the route that row's lineage, and the decision was taken about a session the caller never
	// named. Two documented guarantees, zero comparisons: the mutant "the resolver answers about
	// a neighbouring row" survived every test in the package.
	//
	// It is an ERROR and not a 404, and the difference is canon rule 5. A resolver that answers
	// about the wrong row is broken; a row that is absent is a fact about the world. Answering
	// 404 here would let a concealing route say "there is no such session" on the strength of a
	// resolver that had just proved it cannot be trusted to say which session it read.
	if facts.ID != id || facts.Tenant != tenant {
		return res, tenant, false, nil, fmt.Errorf(
			"api: the core-entity resolver answered about a different row for CoreKind %s; "+
				"refusing to use its lineage", ref.CoreKind)
	}

	res.WorkspaceID = facts.WorkspaceID
	// The owning agent travels as an ATTRIBUTE, not as the resource id: it is what
	// makes an AgentGroup grant reach this row (modules/governance/grants.go resolves
	// the same fact from the stored row for its own kinds), and it must never displace
	// the session id the policy is written against.
	if !facts.AgentID.IsZero() {
		if res.Extra == nil {
			res.Extra = map[string]string{}
		}
		res.Extra["agent"] = facts.AgentID.String()
	}
	return res, tenant, true, nil, nil
}
