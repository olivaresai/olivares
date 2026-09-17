// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// lineageEvidenceScope records the generation before every resolver query,
// including an empty result. Both observations share the producer's one View.
// Only this closed resolver adapter can claim coverage; request fields cannot.
type lineageEvidenceScope struct {
	store.Scope
	facts map[model.Kind]store.AuthorizationFactRef
}

func (s *lineageEvidenceScope) observe(ctx context.Context, kind model.Kind) error {
	if _, seen := s.facts[kind]; seen {
		return nil
	}
	fact, err := store.ReadLineageFact(ctx, s.Scope, kind)
	epochKind, declared := model.LineageEpochKind(kind)
	plain := store.AuthorizationFactRef{Kind: epochKind, ID: model.ID(s.Tenant()), Version: fact.Version}
	if err != nil || !declared || fact != plain || fact.Version < 1 {
		return errEvidenceUnavailable
	}
	s.facts[kind] = fact
	return nil
}

func (s *lineageEvidenceScope) observedFacts(policy store.AuthorizationFactRef) []store.AuthorizationFactRef {
	out := []store.AuthorizationFactRef{policy}
	for _, fact := range s.facts {
		out = append(out, fact)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

type lineageEvidenceRepo[T any] struct {
	store.Repository[T]
	scope *lineageEvidenceScope
	kind  model.Kind
}

func (r *lineageEvidenceRepo[T]) Get(ctx context.Context, id model.ID) (T, error) {
	if err := r.scope.observe(ctx, r.kind); err != nil {
		var zero T
		return zero, err
	}
	return r.Repository.Get(ctx, id)
}
func (r *lineageEvidenceRepo[T]) List(ctx context.Context, q model.Query) ([]T, model.Page, error) {
	if err := r.scope.observe(ctx, r.kind); err != nil {
		return nil, model.Page{}, err
	}
	return r.Repository.List(ctx, q)
}

func (s *lineageEvidenceScope) Sessions() store.Repository[model.Session] {
	return &lineageEvidenceRepo[model.Session]{Repository: s.Scope.Sessions(), scope: s, kind: "core.session"}
}

func (s *lineageEvidenceScope) Agents() store.Repository[model.Agent] {
	return &lineageEvidenceRepo[model.Agent]{Repository: s.Scope.Agents(), scope: s, kind: "core.agent"}
}

func (s *lineageEvidenceScope) Workspaces() store.Repository[model.Workspace] {
	return &lineageEvidenceRepo[model.Workspace]{Repository: s.Scope.Workspaces(), scope: s, kind: "core.workspace"}
}

func (s *lineageEvidenceScope) AgentGroups() store.Repository[model.AgentGroup] {
	return &lineageEvidenceRepo[model.AgentGroup]{Repository: s.Scope.AgentGroups(), scope: s, kind: "core.agent_group"}
}

func (s *lineageEvidenceScope) AgentGroupMembers() store.Repository[model.AgentGroupMember] {
	return &lineageEvidenceRepo[model.AgentGroupMember]{Repository: s.Scope.AgentGroupMembers(), scope: s, kind: "core.agent_group_member"}
}

func (s *lineageEvidenceScope) DefaultWorkspace(ctx context.Context) (model.Workspace, error) {
	if err := s.observe(ctx, "core.workspace"); err != nil {
		return model.Workspace{}, err
	}
	return s.Scope.DefaultWorkspace(ctx)
}

type lineageEvidenceResources struct {
	store.ResourceRepo
	scope *lineageEvidenceScope
}

func (s *lineageEvidenceScope) Resources() store.ResourceRepo {
	return &lineageEvidenceResources{ResourceRepo: s.Scope.Resources(), scope: s}
}
func (r *lineageEvidenceResources) Get(ctx context.Context, id model.ID) (model.Resource, error) {
	if err := r.scope.observe(ctx, "core.resource"); err != nil {
		return model.Resource{}, err
	}
	return r.ResourceRepo.Get(ctx, id)
}
func (r *lineageEvidenceResources) List(ctx context.Context, q model.Query) ([]model.Resource, model.Page, error) {
	if err := r.scope.observe(ctx, "core.resource"); err != nil {
		return nil, model.Page{}, err
	}
	return r.ResourceRepo.List(ctx, q)
}
