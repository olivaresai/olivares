// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// coreEntityResolver is the composition root's implementation of the core-entity
// authorization seam (V269 / docs/contracts/COCKPIT-02-authz.md §3).
//
// ⛔ IT LIVES HERE AND NOT IN A MODULE, and that placement IS the control. The engine
// refuses a module generic access to core rows (Scope.Ext rejects the `core`
// namespace, naming the credential tables as the reason). This resolver keeps that
// refusal intact by never handing anything back except the five facts an
// authorization decision needs: it reads the row with the engine's TYPED accessor,
// inside a read-only View pinned to the request tenant, and returns ids.
//
// A module cannot construct one: the type is unexported and reaches the server only
// through api.Options at boot.
type coreEntityResolver struct{ st store.Store }

var _ api.CoreEntityAuthorizationResolver = coreEntityResolver{}

// ResolveCoreEntity reads one core row's authorization facts.
//
// The three answers are kept apart on purpose, because collapsing any two of them is
// how a concealing route starts lying:
//
//	row read      -> facts with Exists true
//	no such row   -> facts with Exists false, nil error   (the handler answers 404)
//	could not read-> a NON-NIL error                      (the caller answers 503)
//
// store.ErrNotFound is the ONLY error mapped to absence. Anything else — a closed
// store, a timeout, a driver fault — travels as an error, because "I could not look"
// answered as "there is nothing there" is the most expensive substitution in this
// repository.
func (r coreEntityResolver) ResolveCoreEntity(
	ctx context.Context, tenant model.TenantID, kind api.CoreKind, id model.ID,
) (api.CoreEntityFacts, error) {
	if r.st == nil {
		return api.CoreEntityFacts{}, fmt.Errorf("core-entity resolver: no store wired")
	}
	facts := api.CoreEntityFacts{ID: id, Tenant: tenant}
	switch kind {
	case api.CoreKindSession:
		err := r.st.View(ctx, tenant, func(sc store.Scope) error {
			s, e := sc.Sessions().Get(ctx, id)
			if errors.Is(e, store.ErrNotFound) {
				return nil // absent, not unavailable
			}
			if e != nil {
				return e
			}
			facts.Exists = true
			facts.WorkspaceID = s.WorkspaceID
			facts.AgentID = s.AgentID
			return nil
		})
		if err != nil {
			return api.CoreEntityFacts{}, err
		}
		return facts, nil
	default:
		// A kind the engine does not implement is a REFUSAL, never an empty answer:
		// an unimplemented kind returning "no such row" would let a route that
		// declared it authorize as though the row were absent, which conceals the
		// misdeclaration instead of surfacing it. The mount-time check already
		// rejects an unregistered kind; this is the second half of the same rule.
		return api.CoreEntityFacts{}, fmt.Errorf("core-entity resolver: unsupported kind %s", kind)
	}
}
