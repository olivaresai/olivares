// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model_test

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

func TestTimestampCanonicalRoundTrip(t *testing.T) {
	in := time.Date(2026, 6, 2, 10, 30, 0, 123456789, time.UTC)
	ts := model.NewTimestamp(in)
	if got := ts.String(); got != "2026-06-02T10:30:00.123456789Z" {
		t.Fatalf("String = %q", got)
	}
	back, err := model.ParseTimestamp(ts.String())
	if err != nil {
		t.Fatal(err)
	}
	if !back.Time().Equal(in) {
		t.Fatalf("round-trip = %v, want %v", back.Time(), in)
	}
}

// TestTimestampLexicalOrder confirms the canonical text sorts chronologically —
// the property that lets ORDER BY on the text column be correct.
func TestTimestampLexicalOrder(t *testing.T) {
	earlier := model.NewTimestamp(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)).String()
	later := model.NewTimestamp(time.Date(2026, 1, 1, 0, 0, 0, 1, time.UTC)).String()
	if earlier >= later {
		t.Fatalf("lexical order broken: %q !< %q", earlier, later)
	}
}

func TestIDAndTenantZeroness(t *testing.T) {
	if !model.ID("").IsZero() || !model.ID("00000000-0000-0000-0000-000000000000").IsZero() {
		t.Fatal("empty/nil ID should be zero")
	}
	if model.NewID().IsZero() {
		t.Fatal("fresh ID should not be zero")
	}
	if model.SystemTenantID.IsZero() {
		t.Fatal("system tenant must not be zero")
	}
	if !model.SystemTenantID.IsSystem() {
		t.Fatal("system tenant must report IsSystem")
	}
}

func TestKindNamespacing(t *testing.T) {
	k := model.Kind("rrw.access_edge")
	if k.Namespace() != "rrw" || k.Name() != "access_edge" || !k.Valid() {
		t.Fatalf("kind parse: ns=%q name=%q valid=%v", k.Namespace(), k.Name(), k.Valid())
	}
	for _, bad := range []model.Kind{"nodot", "Core.X", ".x", "x.", "core.", "a b.c"} {
		if bad.Valid() {
			t.Fatalf("kind %q should be invalid", bad)
		}
	}
}

func TestDescriptorColumns(t *testing.T) {
	d := model.EntityDescriptor{
		Kind: "rrw.w", Table: "rrw_w", SoftDelete: true,
		Fields: []model.FieldSpec{{Name: "label", Kind: model.KindText}},
	}
	got := d.AllColumns()
	want := []string{"id", "tenant_id", "created_at", "updated_at", "version", "deleted_at", "label"}
	if len(got) != len(want) {
		t.Fatalf("AllColumns = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllColumns[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if k, _ := d.KindOfColumn("version"); k != model.KindInt {
		t.Fatal("version column should be KindInt")
	}
	if !d.NullableColumn("deleted_at") {
		t.Fatal("deleted_at should be nullable")
	}
}
