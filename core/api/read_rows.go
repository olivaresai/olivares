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

// ReadRowAuthorizationPort adds legacy directory-only receipts and a narrowly scoped
// refresh from the original opaque credential. Complete human reads require
// CompleteReadRowAuthorizationPort. It never accepts replacement
// roles, workspace filters, credentials, or a caller-asserted decision.
type ReadRowAuthorizationPort interface {
	RowAuthorizationPort
	RefreshReadPrincipal(context.Context, auth.Principal, model.TenantID) (context.Context, auth.Principal, error)
}
type readAuthorizerRows struct {
	authorizerRows
	producer PrincipalEvidenceProducer
}

func NewReadRowAuthorizationPort(az *auth.Authorizer, producer PrincipalEvidenceProducer) ReadRowAuthorizationPort {
	return readAuthorizerRows{authorizerRows: authorizerRows{az: az}, producer: producer}
}
func (a readAuthorizerRows) RefreshReadPrincipal(ctx context.Context, old auth.Principal, tenant model.TenantID) (context.Context, auth.Principal, error) {
	ref, ok := old.Ref()
	if !ok || a.producer == nil {
		return ctx, auth.Principal{}, auth.ErrRouteUndecided
	}
	// Authorization evaluates with engine scope, then installs the fresh module
	// boundary. A removed confinement cannot leave the previous context's filter.
	ctx = context.WithValue(ctx, ctxKeyModuleBoundary, moduleRequestBoundary{})
	p, err := a.producer.ResolvePrincipalScope(ctx, ref, tenant)
	if err != nil {
		return ctx, auth.Principal{}, err
	}
	got, ok := p.Ref()
	if !ok || got != ref || a.az.PrincipalAuthorityAvailable(p, tenant) != nil {
		return ctx, auth.Principal{}, auth.ErrRouteUndecided
	}
	return withModuleRequestBoundary(withPrincipal(ctx, p), tenant, p), p, nil
}
func (a readAuthorizerRows) DecideRows(ctx context.Context, p auth.Principal, tenant model.TenantID, perm auth.Permission, meta RouteMetadata, resources []auth.ResourceAttrs) (AuthorizedRowSet, error) {
	if p.Kind == auth.KindUser {
		return AuthorizedRowSet{}, fmt.Errorf("api: human read requires complete authority: %w", auth.ErrRouteUndecided)
	}
	// This is the same engine scope the outer governed door uses before it marks
	// handler confinement. The complete question still contains the actual p.
	ctx = context.WithValue(ctx, ctxKeyModuleBoundary, moduleRequestBoundary{})
	out := AuthorizedRowSet{Allowed: make([]bool, len(resources)), Witnesses: make([]auth.RouteAuthorizationWitness, len(resources)), ReadDecisions: make([]auth.RouteReadDecision, len(resources))}
	for i, res := range resources {
		decision, err := a.az.DecideRouteRead(ctx, auth.Request{Principal: p, Tenant: tenant, Permission: perm, Resource: res, Route: meta.RouteMetadata})
		if err != nil {
			return AuthorizedRowSet{}, fmt.Errorf("api: read row %d: %w", i, err)
		}
		out.ReadDecisions[i] = decision
		out.Allowed[i] = decision.Allowed()
		out.Witnesses[i] = decision.AllowWitness()
	}
	return out, nil
}

// ReadRowBatch retains both accepted and omitted candidates. A page's final
// View validates every batch, including its look-ahead, at the same DB time.
type ReadRowBatch struct {
	Set       AuthorizedRowSet
	Resources []auth.ResourceAttrs
}

func ValidateReadRowBatches(ctx context.Context, sc store.Scope, now time.Time, req auth.Request, batches []ReadRowBatch) error {
	if sc == nil || sc.Tenant() != req.Tenant {
		return auth.ErrRouteUndecided
	}
	facts := make(map[string]store.AuthorizationFactRef)
	for _, batch := range batches {
		if len(batch.Set.ReadDecisions) != len(batch.Resources) || len(batch.Set.Allowed) != len(batch.Resources) || len(batch.Set.Witnesses) != len(batch.Resources) {
			return ErrRowDecisionMismatch
		}
		for i, res := range batch.Resources {
			decision := batch.Set.ReadDecisions[i]
			if decision.Allowed() != batch.Set.Allowed[i] {
				return ErrRowWitnessUnverified
			}
			if decision.Allowed() && decision.AllowWitness().EvidenceDigest != batch.Set.Witnesses[i].EvidenceDigest {
				return ErrRowWitnessUnverified
			}
			question := req
			question.Resource = res
			refs, err := decision.FactsFor(now, question)
			if err != nil {
				return err
			}
			for _, f := range refs {
				key := string(f.Kind) + "\x00" + f.ID.String()
				if old, ok := facts[key]; ok && old != f {
					return store.ErrConflict
				}
				facts[key] = f
			}
		}
	}
	if len(facts) == 0 {
		return auth.ErrRouteUndecided
	}
	leased := make(map[string]model.ID)
	for _, f := range facts {
		if subject, _, _, ok := f.LeaseFenceWitness(); ok {
			key := string(f.Kind) + "\x00" + subject
			if old, exists := leased[key]; exists && old != f.ID {
				return ErrRowWitnessUnverified
			}
			leased[key] = f.ID
		}
	}
	// The store grammar retains its bounded call size. All chunks run in this
	// same stable View, after cross-batch contradictions have been rejected.
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	refs := make([]store.AuthorizationFactRef, 0, 64)
	for _, key := range keys {
		f := facts[key]
		refs = append(refs, f)
		if len(refs) == 64 {
			if err := store.ValidateReadAuthority(ctx, sc, refs); err != nil {
				return err
			}
			refs = refs[:0]
		}
	}
	if len(refs) > 0 {
		return store.ValidateReadAuthority(ctx, sc, refs)
	}
	return nil
}
