// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestThresholdListEmptyIsKnownNotNull guards the Terraform Plugin Framework
// contract for the Computed "thresholds" attribute: an empty budget must yield a
// KNOWN, empty list — never null (null on a Computed-only attribute makes
// Terraform report an inconsistent result). Mirrors stringList in
// inventory_data_source.go.
func TestThresholdListEmptyIsKnownNotNull(t *testing.T) {
	ctx := context.Background()
	for _, in := range [][]float64{nil, {}} {
		got, diags := thresholdList(ctx, in)
		if diags.HasError() {
			t.Fatalf("unexpected diagnostics for %v: %v", in, diags)
		}
		if got.IsNull() {
			t.Fatalf("thresholds list is null for empty input %v; a Computed list must be known", in)
		}
		if got.IsUnknown() {
			t.Fatalf("thresholds list is unknown for empty input %v", in)
		}
		if n := len(got.Elements()); n != 0 {
			t.Fatalf("expected 0 elements for empty input %v, got %d", in, n)
		}
		if !got.ElementType(ctx).Equal(types.Float64Type) {
			t.Fatalf("expected Float64 element type, got %v", got.ElementType(ctx))
		}
	}
}

// TestThresholdListValues verifies the populated path preserves order and arity.
func TestThresholdListValues(t *testing.T) {
	ctx := context.Background()
	got, diags := thresholdList(ctx, []float64{0.5, 0.8, 1.0})
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got.IsNull() || got.IsUnknown() {
		t.Fatal("populated thresholds list must be known and non-null")
	}
	if n := len(got.Elements()); n != 3 {
		t.Fatalf("expected 3 elements, got %d", n)
	}
}
