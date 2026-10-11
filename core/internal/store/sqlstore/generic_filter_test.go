// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestFilterFragmentLikeUsesExplicitEscape(t *testing.T) {
	const contract = `SQL_LIKE_ESCAPE_CONTRACT: OpLike must emit an explicit ESCAPE clause and preserve the bound pattern`
	const pattern = `%service\_100\%\\owner%`

	repo := genericRepo{desc: model.EntityDescriptor{
		Fields: []model.FieldSpec{{Name: "subject", Kind: model.KindText}},
	}}
	fragment, values, err := repo.filterFragment(model.Filter{
		Column: "subject",
		Op:     model.OpLike,
		Value:  pattern,
	})
	if err != nil {
		t.Fatalf("%s: filterFragment returned error: %v", contract, err)
	}
	if want := `subject LIKE ? ESCAPE '\'`; fragment != want {
		t.Fatalf("%s: fragment = %q, want %q", contract, fragment, want)
	}
	if len(values) != 1 {
		t.Fatalf("%s: bound values = %#v, want exactly one", contract, values)
	}
	if got, ok := values[0].(string); !ok || got != pattern {
		t.Fatalf("%s: bound value = %#v, want unchanged %q", contract, values[0], pattern)
	}
}
