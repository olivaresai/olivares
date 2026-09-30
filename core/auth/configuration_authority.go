// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ErrBadMutationReason refuses missing, overlong or non-textual effect attribution.
var ErrBadMutationReason = errors.New("auth: invalid mutation reason")

// PutConfigIdPWithAuthority consumes retained authorization for this exact write
// question. Actor and business scope come only from request. The owner transaction
// takes the login capability lock, pins the complete authority bundle, writes and
// seals one reason-bearing effect audit, then rechecks the original proof horizon.
// expectedVersion is optional: nil preserves ordinary OCC; zero requires absence;
// a positive value requires that exact owner version. It never affects legacy Put.
func (s *FederationService) PutConfigIdPWithAuthority(ctx context.Context, authority RouteMutationAuthorization, request Request, alias string, in FederationConfigInput, expectedVersion *int64, reason string) (FederationConfigView, error) {
	guard, err := newConfigurationAuthority(authority, request, reason)
	if err != nil {
		return FederationConfigView{}, err
	}
	if expectedVersion != nil {
		if *expectedVersion < 0 {
			return FederationConfigView{}, store.ErrConflict
		}
		guard.hasExpectedVersion, guard.expectedVersion = true, *expectedVersion
	}
	if s == nil || s.st == nil {
		return FederationConfigView{}, ErrRouteUndecided
	}
	return s.putConfigIdP(ctx, guard.request.Principal, guard.request.Tenant, alias, in, guard)
}

// PutWithAuthority uses the existing scoped sealer and native secret producer.
// The complete authority pin, secret write, reason-bearing audit and final proof
// horizon check belong to one owner AuthMutate; no separate actor/scope is accepted.
func (s *SecretStore) PutWithAuthority(ctx context.Context, authority RouteMutationAuthorization, request Request, name, value, description, reason string) (SecretView, error) {
	guard, err := newConfigurationAuthority(authority, request, reason)
	if err != nil {
		return SecretView{}, err
	}
	if s == nil || s.st == nil {
		return SecretView{}, ErrRouteUndecided
	}
	return s.put(ctx, guard.request.Principal, guard.request.Tenant, name, value, description, guard)
}

type configurationAuthority struct {
	authority          RouteMutationAuthorization
	request            Request
	reason             string
	hasExpectedVersion bool
	expectedVersion    int64
}

func newConfigurationAuthority(authority RouteMutationAuthorization, request Request, reason string) (*configurationAuthority, error) {
	if strings.TrimSpace(reason) == "" || len(reason) > 512 || !utf8.ValidString(reason) {
		return nil, ErrBadMutationReason
	}
	for _, character := range reason {
		if unicode.IsControl(character) {
			return nil, ErrBadMutationReason
		}
	}
	tenant, err := model.ParseTenantID(request.Tenant.String())
	if err != nil || tenant != request.Tenant || tenant.IsZero() || tenant.IsSystem() ||
		(request.Permission.Verb() != VerbWrite && request.Permission.Verb() != VerbAdmin) ||
		!authority.wellFormed() || authority.completeDigest() != authority.authorityDigest {
		return nil, ErrRouteUndecided
	}
	return &configurationAuthority{authority: authority, request: cloneEvidenceRequest(request), reason: reason}, nil
}

func configurationTransactionNow(ctx context.Context, as store.AuthScope) (model.Timestamp, error) {
	if err := ctx.Err(); err != nil {
		return model.Timestamp{}, err
	}
	clock, ok := as.(store.TransactionClock)
	if !ok {
		return model.Timestamp{}, fmt.Errorf("%w: configuration mutation requires the owner transaction clock", ErrRouteUndecided)
	}
	now, err := clock.TransactionNow(ctx)
	if err != nil {
		return model.Timestamp{}, err
	}
	if now.IsZero() {
		return model.Timestamp{}, ErrRouteUndecided
	}
	return now, nil
}

func (g *configurationAuthority) pin(ctx context.Context, as store.AuthScope) error {
	now, err := configurationTransactionNow(ctx, as)
	if err != nil {
		return err
	}
	bundle, err := g.authority.AuthorityFor(now.Time(), g.request)
	if err != nil {
		return err
	}
	barrier, ok := as.(store.AuthTenantAuthorityBarrier)
	if !ok {
		return fmt.Errorf("%w: configuration mutation requires the complete auth tenant authority barrier", ErrRouteUndecided)
	}
	return barrier.LockAuthTenantAuthority(ctx, g.request.Tenant, bundle)
}

func (g *configurationAuthority) compareOwnerVersion(ctx context.Context, as store.AuthScope, alias string) error {
	if !g.hasExpectedVersion {
		return nil
	}
	rows, _, err := as.FederationConfigs().List(ctx, model.Query{Filters: []model.Filter{
		{Column: "target_tenant_id", Op: model.OpEq, Value: g.request.Tenant.String()},
		{Column: "alias", Op: model.OpEq, Value: model.NormalizeFederationAlias(alias)},
	}, Limit: 2})
	if err != nil {
		return err
	}
	if g.expectedVersion == 0 {
		if len(rows) != 0 {
			return store.ErrConflict
		}
		return nil
	}
	if len(rows) != 1 || rows[0].Version != g.expectedVersion {
		return store.ErrConflict
	}
	return nil
}

func (g *configurationAuthority) auditAndFinalize(ctx context.Context, as store.AuthScope, action string, kind model.Kind, target model.ID) error {
	actor := g.request.Principal
	subject, err := actor.AttributableActor()
	if err != nil {
		return err
	}
	metadata := actor.AuditMeta()
	if metadata == nil {
		metadata = make(map[string]any)
	}
	metadata["reason"] = g.reason
	event, err := as.Audit().Append(ctx, model.AuditDraft{Actor: subject, ActorKind: actor.ActorKind(), Action: action, TargetKind: kind, TargetID: target, Meta: metadata})
	if err != nil {
		return err
	}
	if event.Seq < 1 {
		return errors.New("auth: configuration effect audit was not persisted")
	}
	// This is a fresh database time observation, not a refreshed proof. All pins
	// remain held by this same transaction until the write and audit commit.
	now, err := configurationTransactionNow(ctx, as)
	if err != nil {
		return err
	}
	_, err = g.authority.AuthorityFor(now.Time(), g.request)
	return err
}
