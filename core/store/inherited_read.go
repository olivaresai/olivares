// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

// ListInheritedExtension reads append-only children of one parent, never a child
// collection or a repository handle. Call it within View: the parent recheck and
// child query then share the tenant's stable read snapshot. parentID is the
// canonical stored row ID already authorized by the caller's row gate.
//
// A confined scope's ordinary Ext remains closed for these children. Only this
// engine-owned reader can access the raw child repository, after validating its
// declared relation and re-reading the parent through the ORIGINAL confinement.
// Its mandatory child filter comes from that stored parent, not caller input.
func ListInheritedExtension(ctx context.Context, sc Scope, kind model.Kind, parentID model.ID, q model.Query) ([]model.Record, model.Page, error) {
	if sc == nil {
		return nil, model.Page{}, ErrLineageUnavailable
	}
	parsed, err := model.ParseID(parentID.String())
	if err != nil || parsed != parentID {
		return nil, model.Page{}, ErrNotFound
	}
	raw := sc
	if confined, ok := sc.(interface{ inheritedReadScope() Scope }); ok {
		raw = confined.inheritedReadScope()
	}
	children, err := raw.Ext(kind)
	if err != nil {
		return nil, model.Page{}, err
	}
	child := children.Descriptor()
	if child.Kind != kind || !kind.Valid() {
		return nil, model.Page{}, fmt.Errorf("%w: invalid inherited read child", ErrLineageUnavailable)
	}
	spec := child.WorkspaceInheritedRead
	if !spec.Declared() {
		return nil, model.Page{}, denied(string(kind))
	}
	parents, err := sc.Ext(spec.ParentKind)
	if err != nil {
		return nil, model.Page{}, err
	}
	if err := model.ValidateInheritedWorkspaceRead(child, parents.Descriptor()); err != nil {
		return nil, model.Page{}, fmt.Errorf("%w: invalid inherited read relation", ErrLineageUnavailable)
	}
	parent, err := parents.Get(ctx, parentID)
	if err != nil {
		return nil, model.Page{}, err
	}
	key := parent.String(spec.ParentColumn)
	if parent.String(model.ColID) != parentID.String() || parent.String(model.ColTenantID) != sc.Tenant().String() || key == "" {
		return nil, model.Page{}, fmt.Errorf("%w: invalid inherited read parent", ErrLineageUnavailable)
	}
	return children.List(ctx, forceQuery(q, model.Filter{Column: spec.Column, Op: model.OpEq, Value: key}))
}
