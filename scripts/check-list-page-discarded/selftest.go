// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// runSelfTest proves the checker in BOTH directions on a synthetic tree.
//
// A gate exercised only where the answer is "clean" is a constant wearing a
// predicate's clothes. The two positive cases are the shapes that actually
// shipped — cmd_eventing.go:148 (empty literal) and :1194 (a variable that
// collects Filters and never a Limit) — so if a refactor blinds this gate, it is
// blinded against the very defect it was written for and the self-test says so
// before the scan does.
//
// The negative cases are each of the legitimate shapes ONE AT A TIME. Grouping
// them would let one silence hide behind another's: if `keeps the page` and
// `Limit: 1` were in one sample, a checker that had stopped looking at the query
// entirely would still pass it.
func runSelfTest() error {
	dir, err := os.MkdirTemp("", "check-list-page-discarded-selftest")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	cases := []struct {
		name string
		body string
		want int // findings expected
	}{
		{
			// The shape that shipped as cmd_eventing.go:148.
			name: "positive_empty_literal",
			body: `recs, _, err := repo.List(ctx, model.Query{})
	_, _ = recs, err`,
			want: 1,
		},
		{
			// The shape that shipped as cmd_eventing.go:1194: the variable
			// collects Filters and never receives a Limit.
			name: "positive_var_never_limited",
			body: `q := model.Query{Filters: []int{1}}
	q.Filters = append(q.Filters, 2)
	recs, _, err := repo.List(ctx, q)
	_, _ = recs, err`,
			want: 1,
		},
		{
			// Keeps the page: the caller still holds the evidence.
			name: "negative_keeps_page",
			body: `recs, page, err := repo.List(ctx, model.Query{})
	_, _, _ = recs, page, err`,
			want: 0,
		},
		{
			// Declares a cap in the literal.
			name: "negative_limit_in_literal",
			body: `recs, _, err := repo.List(ctx, model.Query{Limit: 1})
	_, _ = recs, err`,
			want: 0,
		},
		{
			// Declares the cap on the variable, after building it.
			name: "negative_limit_assigned_to_var",
			body: `q := model.Query{}
	q.Limit = 500
	recs, _, err := repo.List(ctx, q)
	_, _ = recs, err`,
			want: 0,
		},
		{
			// The cap may be set by the caller: UNDECIDED, never a finding.
			name: "negative_query_from_parameter",
			body: `recs, _, err := repo.List(ctx, outer)
	_, _ = recs, err`,
			want: 0,
		},
	}

	for _, tc := range cases {
		sub := filepath.Join(dir, tc.name)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			return err
		}
		src := fmt.Sprintf(`package sample

type model struct{}

func sample(ctx, repo, outer any) {
	%s
}
`, tc.body)
		// The sample never has to COMPILE — go/ast parses it — but it does have
		// to parse, and a sample that silently stopped parsing would report zero
		// findings and read exactly like a clean tree.
		if err := os.WriteFile(filepath.Join(sub, "sample.go"), []byte(src), 0o600); err != nil {
			return err
		}
		found, c, err := scan(sub)
		if err != nil {
			return fmt.Errorf("%s: scan: %w", tc.name, err)
		}
		if c.Calls != 1 {
			return fmt.Errorf("%s: the sample was not parsed as one List call (calls=%d) — the control measured nothing", tc.name, c.Calls)
		}
		if len(found) != tc.want {
			return fmt.Errorf("%s: want %d finding(s), got %d", tc.name, tc.want, len(found))
		}
	}
	return nil
}
