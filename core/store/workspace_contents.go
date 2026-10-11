// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

// KindCount is how many rows of one kind a workspace holds.
type KindCount struct {
	// Count is the number of rows read, at most one store page.
	Count int
	// Capped is true when the kind holds more rows than the page carried: read
	// Count as "at least Count", never as a total.
	Capped bool
}

// contentsLimit is deliberately above the store's maxLimit: asking for more than
// a page can hold is how the contents read says "as many as you will give me".
// The store clamps it and reports the truncation in the model.Page, which is
// why every count keeps its page instead of discarding it.
const contentsLimit = 10000

// ReadWorkspaceContents is the one read of what a workspace holds, every kind
// counted through ConfineWorkspace: the confined scope is the one definition of
// a workspace's rows, including the rows without a workspace that belong to the
// default one, so no kind is filtered by hand here.
//
// The four core kinds the confined scope filters by workspace (agents,
// sessions, resources and agent groups) are always counted, through their typed
// accessors, because Ext serves module kinds only. Every module kind in
// descriptors that declares workspace lineage is counted too: pass the store's
// closed registry (CompositionCensus.CensusDescriptors) for the whole contents,
// or nil when only the core counts are needed. A core kind in descriptors that
// declares lineage and is not one of the four is an error, never a zero. A kind
// marked Internal (a module's own guard rows) is left out: it is not something
// a user put in the workspace.
func ReadWorkspaceContents(
	ctx context.Context,
	raw Scope,
	workspaceID model.ID,
	descriptors []model.EntityDescriptor,
) (map[model.Kind]KindCount, error) {
	sc, err := ConfineWorkspace(ctx, raw, workspaceID)
	if err != nil {
		return nil, err
	}
	q := model.Query{Limit: contentsLimit}
	out := make(map[model.Kind]KindCount)
	for _, core := range []struct {
		kind model.Kind
		read func() (KindCount, error)
	}{
		{"core.agent", func() (KindCount, error) { return countPage(sc.Agents().List(ctx, q)) }},
		{"core.session", func() (KindCount, error) { return countPage(sc.Sessions().List(ctx, q)) }},
		{"core.resource", func() (KindCount, error) { return countPage(sc.Resources().List(ctx, q)) }},
		{"core.agent_group", func() (KindCount, error) { return countPage(sc.AgentGroups().List(ctx, q)) }},
	} {
		c, err := core.read()
		if err != nil {
			return nil, fmt.Errorf("workspace contents %s: %w", core.kind, err)
		}
		out[core.kind] = c
	}
	for _, d := range descriptors {
		if _, counted := out[d.Kind]; counted || d.Internal || !d.WorkspaceLineage.Declared() {
			continue
		}
		if d.Kind.Namespace() == model.CoreNamespace {
			return nil, fmt.Errorf("store: workspace contents has no reader for core kind %s", d.Kind)
		}
		repo, err := sc.Ext(d.Kind)
		if err != nil {
			return nil, fmt.Errorf("workspace contents %s: %w", d.Kind, err)
		}
		c, err := countPage(repo.List(ctx, q))
		if err != nil {
			return nil, fmt.Errorf("workspace contents %s: %w", d.Kind, err)
		}
		out[d.Kind] = c
	}
	return out, nil
}

// ReadWorkspaceUserGroups counts the user groups placed in one workspace of
// tenant. User groups live in the authentication partition, outside the tenant
// Scope ConfineWorkspace filters, so they are not one of ReadWorkspaceContents'
// kinds; the contents route adds this count beside them. A group never placed is
// tenant-wide and belongs to no workspace, the default one included, and a
// zero workspace id is refused rather than read as "the unplaced groups".
func ReadWorkspaceUserGroups(
	ctx context.Context,
	as AuthScope,
	tenant model.TenantID,
	workspaceID model.ID,
) (KindCount, error) {
	if workspaceID.IsZero() {
		return KindCount{}, fmt.Errorf("%w: user groups of a zero workspace id", ErrWorkspaceConfinement)
	}
	return countPage(as.Groups().List(ctx, model.Query{
		Limit: contentsLimit,
		Filters: []model.Filter{
			{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
			{Column: "workspace_id", Op: model.OpEq, Value: workspaceID.String()},
		},
	}))
}

func countPage[T any](rows []T, page model.Page, err error) (KindCount, error) {
	return KindCount{Count: len(rows), Capped: page.HasMore}, err
}
