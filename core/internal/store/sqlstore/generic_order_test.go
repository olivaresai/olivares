// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestOrderClauseDoesNotDuplicateExplicitIDTiebreaker(t *testing.T) {
	repo := &genericRepo{desc: model.EntityDescriptor{
		Fields: []model.FieldSpec{{Name: "sequence", Kind: model.KindInt}},
	}}
	got, custom, err := repo.orderClause([]model.Sort{
		{Column: "sequence", Desc: true},
		{Column: model.ColID, Desc: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !custom || got != "sequence DESC, id DESC" {
		t.Fatalf("explicit-id order = %q custom=%t, want sequence DESC, id DESC / true", got, custom)
	}

	got, custom, err = repo.orderClause([]model.Sort{{Column: "sequence", Desc: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !custom || got != "sequence DESC, id ASC" {
		t.Fatalf("implicit-id order = %q custom=%t, want sequence DESC, id ASC / true", got, custom)
	}
}
