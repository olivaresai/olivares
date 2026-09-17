// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

// DistinctProjection selects the DISTINCT values of one declared column of a
// module entity, in ascending column order, under the pinned tenant, the
// caller's ANDed Filters and one ORed group of alternatives. It is the narrow
// store capability behind a grant-first catalog: "which channel_id values carry a
// current read grant for ANY of the caller's closure subjects", answered by one
// parameterized statement per bounded subject batch instead of one grant query
// per Channel.
//
// It is deliberately a projection, not a row list: it returns column values
// only, so a caller cannot use it to read another entity's payload, and the
// engine applies the same tenant predicate, soft-delete predicate and workspace
// confinement (through the confined repository wrappers) as List.
//
// Cost contract: with an index led by the common equality columns and the
// alternative's equality columns ahead of Column, the engine examines rows in
// proportion to the number of alternatives and the requested page, because each
// alternative is rendered as its own bounded ordered arm (see AnyOf). It never
// degrades to scanning every row of the confinement to filter the alternatives
// out, and it never issues one statement per projected value.
//
// Ordering is the column's own ASC order on both engines. Canonical lowercase
// UUID text and canonical timestamp text order identically on SQLite TEXT and
// PostgreSQL TEXT, so a keyset anchor (After) is portable. NULL values are never
// projected.
type DistinctProjection struct {
	// Column is the declared entity column whose distinct values are returned.
	Column string
	// Filters are ANDed predicates applied to every row (validated columns and
	// operators, bound values).
	Filters []model.Filter
	// AnyOf is one ORed group: a row matches when ALL filters of at least one
	// alternative hold. Each alternative must be non-empty. Empty AnyOf means no
	// alternative restriction.
	//
	// The engine renders the group as one statement whose arms are the
	// alternatives: every arm carries the tenant predicate, the soft-delete
	// predicate, Filters and After PLUS its own alternative, reads its own
	// ordered limit+1 distinct values, and the arms are unioned, deduplicated,
	// ordered and capped again at limit+1. That is the same result one ORed
	// predicate would produce (a value among the first limit+1 of the union is
	// among the first limit+1 of every alternative that carries it), but each arm
	// keeps the alternative's exact equality columns as leading index bounds, so
	// the rows an engine examines scale with the alternatives and the page, never
	// with the rows other subjects hold in the same confinement. The group is
	// bounded so one statement never renders more bound values than either
	// engine accepts even with the common predicates repeated per arm (see
	// BoundValues); a caller with a larger set batches it.
	AnyOf [][]model.Filter
	// After is an exclusive keyset anchor on Column (Column > After). Empty means
	// start from the smallest value.
	After string
	// Limit caps the page (1..DistinctProjectionMaxLimit). The engine reads one
	// extra value to report HasMore.
	Limit int
}

// DistinctPage is the projected page: Values are ascending and distinct;
// HasMore reports whether at least one further value exists beyond the page.
type DistinctPage struct {
	Values  []string
	HasMore bool
}

// DistinctProjector is an OPTIONAL GenericRepo capability. A repository that
// cannot answer a projection with one parameterized statement must not
// implement it, so a caller fails closed on the type assertion instead of
// degrading to a row scan. Workspace confinement preserves it exactly like
// RowLocker: the confined repository forces the declared lineage predicate into
// Filters and delegates.
type DistinctProjector interface {
	ProjectDistinct(ctx context.Context, p DistinctProjection) (DistinctPage, error)
}

const (
	// DistinctProjectionMaxAlternatives bounds AnyOf, and with it the number of
	// unioned arms one statement renders; a larger closure is batched by the
	// caller. Together with the two filter bounds below it fixes the worst-case
	// bound-value count of one statement (DistinctProjectionWorstCaseBoundValues),
	// which stays far below DistinctProjectionMaxBoundValues.
	DistinctProjectionMaxAlternatives = 256
	// DistinctProjectionMaxAlternativeFilters bounds one alternative's ANDed
	// equality set.
	DistinctProjectionMaxAlternativeFilters = 8
	// DistinctProjectionMaxFilters bounds the common ANDed set. Workspace
	// confinement forces its lineage predicate into this set and the confined
	// projection is validated AFTER that, so the bound holds for the statement
	// the engine renders: a caller that fills the bound leaves no room for the
	// forced predicate and is refused, never rendered over the ceiling.
	DistinctProjectionMaxFilters = 16
	// DistinctProjectionMaxLimit mirrors the generic List ceiling.
	DistinctProjectionMaxLimit = 1000
	// DistinctProjectionMaxCompoundArms is SQLite's ceiling on the number of
	// terms in one compound SELECT (SQLITE_MAX_COMPOUND_SELECT, 500 as compiled
	// into modernc.org/sqlite). Every alternative renders as one compound term,
	// so DistinctProjectionMaxAlternatives stays strictly below it; PostgreSQL
	// has no comparable ceiling. It is a named constant so the relation is
	// checked by a test instead of remembered.
	DistinctProjectionMaxCompoundArms = 500
	// DistinctProjectionMaxBoundValues caps the bound values ONE rendered
	// statement may carry. It is the lower of the two engines' statement
	// parameter ceilings: SQLite's SQLITE_MAX_VARIABLE_NUMBER default of 32766
	// (since 3.32.0); PostgreSQL accepts 65535 per Bind message. It is enforced
	// by Validate on the exact count the engine will render, with the common
	// predicates repeated once per arm.
	DistinctProjectionMaxBoundValues = 32766
	// DistinctProjectionWorstCaseBoundValues is the largest count Validate can
	// admit: every arm repeats the tenant predicate, the full common filter set
	// and the anchor, and carries the largest alternative. It is a compile-time
	// constant so the relation to DistinctProjectionMaxBoundValues is provable
	// without rendering.
	DistinctProjectionWorstCaseBoundValues = DistinctProjectionMaxAlternatives *
		(1 + DistinctProjectionMaxFilters + 1 + DistinctProjectionMaxAlternativeFilters)
)

// BoundValues is the exact number of bound values the engine renders for this
// projection: the tenant predicate, one value per common filter that binds
// (IS NULL / IS NOT NULL bind nothing; the composite unset-or-after and
// equal-or-unset predicates bind one), the anchor when set, all repeated once
// per alternative arm, plus each arm's own binding filters. Without
// alternatives the statement is the single common arm.
func (p DistinctProjection) BoundValues() int {
	common := 1
	for _, f := range p.Filters {
		common += filterBoundValues(f)
	}
	if p.After != "" {
		common++
	}
	if len(p.AnyOf) == 0 {
		return common
	}
	total := 0
	for _, alternative := range p.AnyOf {
		total += common
		for _, f := range alternative {
			total += filterBoundValues(f)
		}
	}
	return total
}

func filterBoundValues(f model.Filter) int {
	if f.Op == model.OpIsNull || f.Op == model.OpNotNull {
		return 0
	}
	return 1
}

// Validate checks the projection's shape before any SQL is rendered. Column
// existence and filter columns are validated against the entity descriptor by
// the repository; this checks only what the caller alone controls.
func (p DistinctProjection) Validate() error {
	if p.Column == "" {
		return fmt.Errorf("%w: projection column is empty", ErrInvalidProjection)
	}
	if p.Limit < 1 || p.Limit > DistinctProjectionMaxLimit {
		return fmt.Errorf("%w: limit %d outside 1..%d", ErrInvalidProjection, p.Limit, DistinctProjectionMaxLimit)
	}
	if len(p.Filters) > DistinctProjectionMaxFilters {
		return fmt.Errorf("%w: %d filters exceed %d", ErrInvalidProjection, len(p.Filters), DistinctProjectionMaxFilters)
	}
	for _, f := range p.Filters {
		if !f.Op.Valid() || f.Column == "" {
			return fmt.Errorf("%w: invalid filter on %q", ErrInvalidProjection, f.Column)
		}
	}
	if len(p.AnyOf) > DistinctProjectionMaxAlternatives {
		return fmt.Errorf("%w: %d alternatives exceed %d", ErrInvalidProjection, len(p.AnyOf), DistinctProjectionMaxAlternatives)
	}
	for index, alternative := range p.AnyOf {
		if len(alternative) == 0 || len(alternative) > DistinctProjectionMaxAlternativeFilters {
			return fmt.Errorf("%w: alternative %d has %d filters", ErrInvalidProjection, index, len(alternative))
		}
		for _, f := range alternative {
			if !f.Op.Valid() || f.Column == "" {
				return fmt.Errorf("%w: invalid alternative filter on %q", ErrInvalidProjection, f.Column)
			}
		}
	}
	if bound := p.BoundValues(); bound > DistinctProjectionMaxBoundValues {
		return fmt.Errorf(
			"%w: %d bound values exceed %d", ErrInvalidProjection, bound, DistinctProjectionMaxBoundValues,
		)
	}
	return nil
}
