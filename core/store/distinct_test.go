// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// TestDistinctProjectionBoundValuesStayUnderBothEngines proves the statement
// shape's bind arithmetic without rendering: the worst admissible projection
// (every arm repeating the full common set and the anchor, carrying the largest
// alternative) stays under the lower engine ceiling, BoundValues counts the
// duplicated common predicates per arm, and Validate refuses a shape that would
// exceed the ceiling before any SQL exists.
func TestDistinctProjectionBoundValuesStayUnderBothEngines(t *testing.T) {
	const sqliteCeiling, postgresCeiling = 32766, 65535
	if DistinctProjectionMaxBoundValues > sqliteCeiling || DistinctProjectionMaxBoundValues > postgresCeiling {
		t.Fatalf("ceiling %d above an engine limit (sqlite %d, postgres %d)",
			DistinctProjectionMaxBoundValues, sqliteCeiling, postgresCeiling)
	}
	if DistinctProjectionWorstCaseBoundValues > DistinctProjectionMaxBoundValues {
		t.Fatalf("worst case %d exceeds the ceiling %d", DistinctProjectionWorstCaseBoundValues, DistinctProjectionMaxBoundValues)
	}
	// Every alternative is one compound term on SQLite, whose compiled ceiling
	// is 500 terms; the alternative bound must stay strictly below it.
	if DistinctProjectionMaxAlternatives >= DistinctProjectionMaxCompoundArms {
		t.Fatalf("%d alternatives reach SQLite's compound-select ceiling %d",
			DistinctProjectionMaxAlternatives, DistinctProjectionMaxCompoundArms)
	}

	// The worst admissible shape, built exactly at every bound.
	worst := DistinctProjection{Column: "c", Limit: DistinctProjectionMaxLimit, After: "a"}
	for i := 0; i < DistinctProjectionMaxFilters; i++ {
		worst.Filters = append(worst.Filters, model.Filter{Column: "f", Op: model.OpEq, Value: i})
	}
	for i := 0; i < DistinctProjectionMaxAlternatives; i++ {
		var alternative []model.Filter
		for j := 0; j < DistinctProjectionMaxAlternativeFilters; j++ {
			alternative = append(alternative, model.Filter{Column: "s", Op: model.OpEq, Value: j})
		}
		worst.AnyOf = append(worst.AnyOf, alternative)
	}
	if got := worst.BoundValues(); got != DistinctProjectionWorstCaseBoundValues {
		t.Fatalf("worst BoundValues = %d, want the constant %d", got, DistinctProjectionWorstCaseBoundValues)
	}
	if err := worst.Validate(); err != nil {
		t.Fatalf("worst admissible shape refused: %v", err)
	}

	// Arithmetic: 1 tenant + binding filters + anchor, repeated per arm, plus the
	// arm's own binding filters; NULL tests bind nothing; composite predicates
	// bind one.
	p := DistinctProjection{
		Column: "c", Limit: 1, After: "x",
		Filters: []model.Filter{
			{Column: "w", Op: model.OpEq, Value: "ws"},
			{Column: "e", Op: model.OpUnsetOrGt, Value: "now"},
			{Column: "d", Op: model.OpIsNull},
			{Column: "n", Op: model.OpNotNull},
			{Column: "l", Op: model.OpEqOrUnset, Value: "v"},
		},
	}
	if got := p.BoundValues(); got != 1+3+1 {
		t.Fatalf("common-only BoundValues = %d, want 5", got)
	}
	p.AnyOf = [][]model.Filter{
		{{Column: "k", Op: model.OpEq, Value: "user"}, {Column: "r", Op: model.OpEq, Value: "u1"}},
		{{Column: "k", Op: model.OpEq, Value: "group"}, {Column: "r", Op: model.OpEq, Value: "g1"}},
		{{Column: "k", Op: model.OpIsNull}},
	}
	if got := p.BoundValues(); got != 3*5+2+2+0 {
		t.Fatalf("three-arm BoundValues = %d, want 19", got)
	}

	// The ceiling is enforced on the exact count, not on the shape bounds alone.
	// A projection can only exceed it by violating a shape bound, so drive it
	// through the alternative count and confirm the shape bound speaks first
	// and the value bound is still checked on the exact arithmetic.
	over := worst
	over.AnyOf = append(over.AnyOf, worst.AnyOf[0])
	if err := over.Validate(); !errors.Is(err, ErrInvalidProjection) {
		t.Fatalf("one alternative over the bound accepted: %v", err)
	}
	if got := over.BoundValues(); got <= DistinctProjectionWorstCaseBoundValues {
		t.Fatalf("over-bound BoundValues = %d, want more than %d", got, DistinctProjectionWorstCaseBoundValues)
	}
}
