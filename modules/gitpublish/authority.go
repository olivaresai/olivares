// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// errAuthorityCapability: the store scope lacks the authority snapshot
// capability. The effect refuses; the legacy validator is never substituted.
var errAuthorityCapability = errors.New("gitpublish: authority snapshot capability unavailable")

// exactAuthority is the production Authority: the exact credential rebuilt by
// the serving Authenticator and one AuthorizeRouteMutation by the composed
// Authorizer, the same pair the managed Stop uses. It adds no authorization
// model of its own.
type exactAuthority struct {
	resolver   PrincipalResolver
	authorizer *auth.Authorizer
}

type exactAdmission struct {
	sub Subject
	req auth.Request
	az  auth.RouteMutationAuthorization
}

// Admit is A1.
func (a exactAuthority) Admit(ctx context.Context, p auth.Principal, tenant model.TenantID, q Question) (Admission, error) {
	ref, ok := p.Ref()
	if !ok {
		return nil, auth.ErrRouteUndecided
	}
	principal, err := a.resolver.ResolvePrincipalScope(ctx, ref, tenant)
	if err != nil {
		return nil, err
	}
	req := auth.Request{
		Principal:  principal,
		Permission: q.Permission,
		Tenant:     tenant,
		Resource:   auth.ResourceAttrs{Kind: q.Permission.Resource(), ID: q.Target.String(), WorkspaceID: q.Workspace},
		Route:      auth.RouteMetadata{CedarAction: string(q.Action), MinimumAAL: q.MinimumAAL},
	}
	if q.Target.IsZero() {
		req.Resource.ID = ""
	}
	az, err := a.authorizer.AuthorizeRouteMutation(ctx, req)
	if err != nil {
		return nil, err
	}
	return exactAdmission{sub: SubjectOf(principal), req: req, az: az}, nil
}

func (x exactAdmission) Subject() Subject { return x.sub }

// Lock pins the complete authority bundle on the claim transaction (W1).
func (x exactAdmission) Lock(ctx context.Context, sc store.Scope, now time.Time) error {
	bundle, err := x.az.AuthorityFor(now, x.req)
	if err != nil {
		return err
	}
	locker, ok := sc.(store.AuthoritySnapshotBundleLocker)
	if !ok {
		return errAuthorityCapability
	}
	return locker.LockAuthoritySnapshotBundle(ctx, bundle)
}

// Recheck is A4: the retained authority must still verify for this exact
// question at this instant, and the bundle must still validate against the
// directory in a fresh read.
func (x exactAdmission) Recheck(ctx context.Context, view View, now time.Time) error {
	bundle, err := x.az.AuthorityFor(now, x.req)
	if err != nil {
		return err
	}
	return view(ctx, func(sc store.Scope) error {
		reader, ok := sc.(store.AuthoritySnapshotBundleReader)
		if !ok {
			return errAuthorityCapability
		}
		return reader.ValidateAuthoritySnapshotBundle(ctx, bundle)
	})
}
