// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CompleteReadRowAuthorizationPort retains all authority channels for a final
// read barrier, including definite denials. It produces no mutation witnesses.
type CompleteReadRowAuthorizationPort interface {
	ReadRowAuthorizationPort
	DecideReadRows(context.Context, auth.Principal, model.TenantID, auth.Permission, RouteMetadata, []auth.ResourceAttrs) (CompleteReadRowSet, error)
}

type CompleteReadRowSet struct {
	Allowed   []bool
	Decisions []auth.RouteReadDecision
}

type CompleteReadRowBatch struct {
	Set       CompleteReadRowSet
	Resources []auth.ResourceAttrs
}

var _ CompleteReadRowAuthorizationPort = readAuthorizerRows{}

func (a readAuthorizerRows) DecideReadRows(ctx context.Context, p auth.Principal, tenant model.TenantID, perm auth.Permission, meta RouteMetadata, resources []auth.ResourceAttrs) (CompleteReadRowSet, error) {
	ctx = context.WithValue(ctx, ctxKeyModuleBoundary, moduleRequestBoundary{})
	out := CompleteReadRowSet{Allowed: make([]bool, len(resources)), Decisions: make([]auth.RouteReadDecision, len(resources))}
	for i, res := range resources {
		decision, err := a.az.DecideRouteRead(ctx, auth.Request{Principal: p, Tenant: tenant, Permission: perm, Resource: res, Route: meta.RouteMetadata})
		if err != nil {
			return CompleteReadRowSet{}, fmt.Errorf("api: complete read row %d: %w", i, err)
		}
		out.Decisions[i], out.Allowed[i] = decision, decision.Allowed()
	}
	return out, nil
}

// CheckCompleteReadRowSet verifies every decision before a caller materializes
// rows. It does not replace the final transaction's current-authority check.
func CheckCompleteReadRowSet(set CompleteReadRowSet, now time.Time, req auth.Request, resources []auth.ResourceAttrs) error {
	return visitCompleteReadRowSet(set, now, req, resources, nil)
}

func visitCompleteReadRowSet(set CompleteReadRowSet, now time.Time, req auth.Request, resources []auth.ResourceAttrs, collect func(store.AuthoritySnapshotBundle) error) error {
	if len(set.Allowed) != len(resources) || len(set.Decisions) != len(resources) {
		return ErrRowDecisionMismatch
	}
	for i, resource := range resources {
		if set.Allowed[i] != set.Decisions[i].Allowed() {
			return ErrRowWitnessUnverified
		}
		question := req
		question.Resource = resource
		bundle, err := set.Decisions[i].AuthorityFor(now, question)
		if err != nil {
			return err
		}
		if collect != nil {
			if err := collect(bundle); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateCompleteReadRowBatches checks all contributions before issuing any
// store validation. Every bounded call uses the same caller-owned View and time.
func ValidateCompleteReadRowBatches(ctx context.Context, sc store.Scope, now time.Time, req auth.Request, batches []CompleteReadRowBatch) error {
	if sc == nil || sc.Tenant() != req.Tenant {
		return auth.ErrRouteUndecided
	}
	facts := make(map[string]store.AuthorizationFactRef)
	users := make(map[model.ID]store.UserAuthorityFactRef)
	collect := func(bundle store.AuthoritySnapshotBundle) error {
		for _, f := range bundle.Facts {
			key := string(f.Kind) + "\x00" + f.ID.String()
			if old, ok := facts[key]; ok && old != f {
				return store.ErrConflict
			}
			facts[key] = f
		}
		for _, h := range bundle.UserAuthorities {
			if old, ok := users[h.UserID]; ok && old != h {
				return store.ErrConflict
			}
			users[h.UserID] = h
		}
		return nil
	}
	for _, batch := range batches {
		if err := visitCompleteReadRowSet(batch.Set, now, req, batch.Resources, collect); err != nil {
			return err
		}
	}
	if len(facts) == 0 || len(users) > 1 {
		return auth.ErrRouteUndecided
	}
	leased := make(map[string]model.ID)
	keys := make([]string, 0, len(facts))
	for key, f := range facts {
		keys = append(keys, key)
		if subject, _, _, ok := f.LeaseFenceWitness(); ok {
			leaseKey := string(f.Kind) + "\x00" + subject
			if old, exists := leased[leaseKey]; exists && old != f.ID {
				return ErrRowWitnessUnverified
			}
			leased[leaseKey] = f.ID
		}
	}
	sort.Strings(keys)
	var userAuthorities []store.UserAuthorityFactRef
	for _, h := range users {
		userAuthorities = append(userAuthorities, h)
	}
	for start := 0; start < len(keys); start += 64 {
		end := min(start+64, len(keys))
		refs := make([]store.AuthorizationFactRef, 0, end-start)
		for _, key := range keys[start:end] {
			refs = append(refs, facts[key])
		}
		if err := store.ValidateReadAuthorityBundle(ctx, sc, store.AuthoritySnapshotBundle{Facts: refs, UserAuthorities: userAuthorities}); err != nil {
			return err
		}
	}
	return nil
}
