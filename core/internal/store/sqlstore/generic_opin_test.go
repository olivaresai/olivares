// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// OpIn (2026-10-02, the batched grant read + A4): a membership test bound as one
// parameterized IN (...) — the fragment carries every value, never interpolated
// text. An empty set is an error, never a silent match-all or match-none.
func TestFilterFragmentInBindsEveryValue(t *testing.T) {
	repo := genericRepo{desc: model.EntityDescriptor{
		Fields: []model.FieldSpec{{Name: "subject", Kind: model.KindText}},
	}}
	fragment, args, err := repo.filterFragment(model.Filter{
		Column: "subject", Op: model.OpIn, Value: []string{"a", "b", "c"},
	})
	if err != nil {
		t.Fatalf("filterFragment: %v", err)
	}
	if want := "subject IN (?, ?, ?)"; fragment != want {
		t.Fatalf("fragment = %q, want %q", fragment, want)
	}
	if len(args) != 3 || args[0] != "a" || args[1] != "b" || args[2] != "c" {
		t.Fatalf("args = %#v, want the three values in order", args)
	}
}

func TestFilterFragmentInRejectsAnEmptySet(t *testing.T) {
	repo := genericRepo{desc: model.EntityDescriptor{
		Fields: []model.FieldSpec{{Name: "subject", Kind: model.KindText}},
	}}
	for _, value := range []any{[]string{}, []model.ID{}, []any{}, nil} {
		if _, _, err := repo.filterFragment(model.Filter{Column: "subject", Op: model.OpIn, Value: value}); err == nil {
			t.Fatalf("an empty IN set (%#v) must be refused, not silently matched", value)
		}
	}
	// A scalar is not a set.
	if _, _, err := repo.filterFragment(model.Filter{Column: "subject", Op: model.OpIn, Value: "a"}); err == nil {
		t.Fatal("a scalar Value for OpIn must be refused")
	}
}
