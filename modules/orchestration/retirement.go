// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"errors"
	"sort"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The orchestration retirement step. When a tenant removes an account, this step
// reports, by id, what orchestration stores in that tenant that binds the account
// to a duty or lets work act for it. It removes nothing: each of these rows is
// the tenant's to resolve.
//   - a workflow whose steps name the account as a work participant blocks the
//     retirement until the tenant edits the graph;
//   - a run that has not finished and whose initiator is the account, or one of
//     whose frozen steps names it, blocks until the run ends; a finished run is
//     history and does not;
//   - a schedule the account owns blocks until the tenant removes it.
// A stored workflow or run holding a step whose kind no registry knows blocks the
// retirement too, listed by table only. The step's transaction first pins the
// tenant's authorization epoch and the account's authority version the pass
// read, so a pass that read before a lift or a new offboard conflicts.

// retirementModule is the declared module name of this step.
const retirementModule = "orchestration"

// RetirementStep returns this module's retirement step.
func (m *Module) RetirementStep() auth.RetirementStep { return retirementStep{m: m} }

// RetirementCovers returns the counted columns this module's step reads, as
// kind.column.
func (m *Module) RetirementCovers() []string {
	var out []string
	for _, t := range retirementTables {
		for _, c := range t.columns {
			out = append(out, string(t.kind)+"."+c.name)
		}
	}
	return out
}

// retirementTable is one table the step reads: its counted columns and whether
// a row naming the account still blocks.
type retirementTable struct {
	kind    model.Kind
	columns []countedColumn
	blocks  func(model.Record) bool
}

// retirementTables are the tables the step reads, in the order it reports them.
var retirementTables = []retirementTable{
	{kind: workflowKind, columns: workflowCountedColumns, blocks: always},
	{kind: wfRunKind, columns: runCountedColumns, blocks: runUnfinished},
	{kind: scheduleKind, columns: scheduleCountedColumns, blocks: always},
}

// always is the blocks rule of a table every row of which blocks.
func always(model.Record) bool { return true }

// runUnfinished reports whether a run can still act: any status other than the
// two terminal ones.
func runUnfinished(rec model.Record) bool {
	s := rec.String(colWrStatus)
	return s != runStatusCompleted && s != runStatusFailed
}

type retirementStep struct{ m *Module }

// Module implements auth.RetirementStep.
func (s retirementStep) Module() string { return retirementModule }

// RetireUser implements auth.RetirementStep.
func (s retirementStep) RetireUser(ctx context.Context, req auth.RetirementRequest) (auth.RetirementOutcome, error) {
	m := s.m
	if m == nil || m.data == nil {
		return auth.RetirementOutcome{}, errors.New("orchestration: the retirement step has no data handle")
	}
	var out auth.RetirementOutcome
	err := m.data.Mutate(ctx, req.Tenant, func(sc store.Scope) error {
		out = auth.RetirementOutcome{}
		fact, err := auth.PinRetirement(ctx, sc, req)
		if err != nil {
			return err
		}
		out.FactVersion = fact
		for _, t := range retirementTables {
			blocking, unknown, err := t.naming(ctx, sc, req)
			if err != nil {
				return err
			}
			out.Blocking = append(out.Blocking, blocking...)
			if unknown {
				out.UnknownKinds = append(out.UnknownKinds, string(t.kind))
			}
		}
		return nil
	})
	if err != nil {
		return auth.RetirementOutcome{}, err
	}
	return out, nil
}

// naming returns the rows of t that name the account of req in a counted column,
// itself or through one of its credentials, and still block, as "<kind>:<id>" in
// id order, and whether t holds a row whose step kind no registry knows. A bare
// account id column is read only over the rows whose stored value is the
// account's; an actor reference column, which a token may hold in the account's
// stead, and a step column are read over the whole table, so a row with an
// unknown step kind is found whoever it names.
func (t retirementTable) naming(ctx context.Context, sc store.Scope, req auth.RetirementRequest) ([]string, bool, error) {
	repo, err := sc.Ext(t.kind)
	if err != nil {
		return nil, false, err
	}
	named := map[string]model.Record{}
	unknown := false
	for _, c := range t.columns {
		var filters []model.Filter
		if v, ok := storedRef(c.decl, req.User); ok {
			filters = append(filters, eq(c.name, v))
		}
		recs, unk, err := auth.RowsNamingAccount(ctx, repo, c.decl, c.name, req, filters...)
		if err != nil {
			return nil, false, err
		}
		unknown = unknown || unk
		for _, rec := range recs {
			named[rec.String(model.ColID)] = rec
		}
	}
	ids := make([]string, 0, len(named))
	for id, rec := range named {
		if t.blocks(rec) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(t.kind)+":"+id)
	}
	return out, unknown, nil
}

// storedRef is the value a bare account id column declared by decl stores for
// user, and whether decl is such a column. An actor reference column has no one
// value for the account: a token may hold it in the account's stead.
func storedRef(decl *model.ColumnDecl, user model.ID) (string, bool) {
	if decl.Form == model.FormRef && decl.Encoding == model.EncodeUserID {
		return user.String(), true
	}
	return "", false
}
