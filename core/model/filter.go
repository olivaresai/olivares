// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// Op is a comparison operator for a List filter. The set is closed so the SQL
// generator can map each operator to a fixed, parameterized fragment — a filter
// never injects an operator string.
type Op string

// The supported filter operators.
const (
	// OpEq is equality (=).
	OpEq Op = "eq"
	// OpNe is inequality (<>).
	OpNe Op = "ne"
	// OpLt is less-than (<).
	OpLt Op = "lt"
	// OpLte is less-than-or-equal (<=).
	OpLte Op = "lte"
	// OpGt is greater-than (>).
	OpGt Op = "gt"
	// OpGte is greater-than-or-equal (>=).
	OpGte Op = "gte"
	// OpLike is a case-sensitive pattern match (LIKE) for text columns.
	OpLike Op = "like"
	// OpEqOrUnset matches rows whose column equals the value OR carries no value
	// at all (NULL, and the empty string for a text column). It exists for the ONE
	// predicate a plain OpEq cannot express: a workspace-lineage column whose unset
	// value means "the tenant's default workspace" (back-compat,
	// scoping.go) — a pre-FASE-X row stores NULL, so an operator confined to the
	// default workspace must see it while an operator confined elsewhere must not.
	// Expressing it in SQL is not optional: filtering the page in Go after List
	// would deform Limit, Cursor and HasMore, which the store computes before it
	// returns the page.
	OpEqOrUnset Op = "eq_or_unset"
	// OpIsNull matches rows whose nullable column is unset. It binds no value.
	OpIsNull Op = "is_null"
	// OpNotNull matches rows whose nullable column is set. It binds no value.
	OpNotNull Op = "not_null"
)

// Valid reports whether o is a supported operator.
func (o Op) Valid() bool {
	switch o {
	case OpEq, OpNe, OpLt, OpLte, OpGt, OpGte, OpLike, OpEqOrUnset, OpIsNull, OpNotNull:
		return true
	default:
		return false
	}
}

// Filter is one predicate of a List query. Column is validated against the
// entity's descriptor before any SQL is built (an unknown column is rejected,
// closing column-name injection); Value is always bound as a parameter.
type Filter struct {
	// Column is the column to test (base or entity column).
	Column string
	// Op is the comparison operator.
	Op Op
	// Value is the comparison value (bound, never interpolated).
	Value any
}

// Sort is one ORDER BY term. Column is validated against the descriptor.
type Sort struct {
	// Column is the column to order by.
	Column string
	// Desc orders descending when true.
	Desc bool
}

// Query is a portable List query. All filters are ANDed. Results are ordered by
// Sort then by id (the stable tiebreaker that the keyset cursor walks).
type Query struct {
	// Filters are ANDed predicates.
	Filters []Filter
	// Sort is the ordering; id is always appended as the final tiebreaker.
	Sort []Sort
	// Limit caps the page size; <= 0 means the store default.
	Limit int
	// Cursor is an opaque keyset cursor from a previous Page.
	Cursor string
	// IncludeDeleted returns soft-deleted rows too (gated; forensic use).
	IncludeDeleted bool
}

// Page is the pagination result returned alongside a List page.
//
// Keyset continuation is available only for the default id ordering: when a
// Query carries a custom Sort, Cursor is empty and HasMore still reports whether
// the page was truncated — to read further, raise Limit or drop the Sort and
// paginate by id. Passing a Cursor together with a Sort is rejected.
type Page struct {
	// Cursor is the opaque cursor to pass as Query.Cursor for the next page.
	// Empty for custom-sort queries (see the type comment).
	Cursor string
	// HasMore reports whether more rows exist after this page.
	HasMore bool
}
