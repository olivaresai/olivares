// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"testing"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func TestWeakestAttribution(t *testing.T) {
	tests := []struct {
		a, b     string
		expected string
	}{
		{"firm", "firm", "firm"},
		{"firm", "approximate", "approximate"},
		{"approximate", "firm", "approximate"},
		{"firm", "unknown", "unknown"},
		{"unknown", "unknown", "unknown"},
		{"", "firm", ""},
	}
	for _, tt := range tests {
		got := weakestOfTwo(tt.a, tt.b)
		if got != tt.expected {
			t.Errorf("weakestOfTwo(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.expected)
		}
	}
}

func TestWeakerConfidence(t *testing.T) {
	tests := []struct {
		a, b     sdkmodel.Confidence
		expected string
	}{
		{sdkmodel.ConfidenceAttributed, sdkmodel.ConfidenceAttributed, "attributed"},
		{sdkmodel.ConfidenceAttributed, sdkmodel.ConfidenceApproximate, "approximate"},
		{sdkmodel.ConfidenceApproximate, sdkmodel.ConfidenceAttributed, "approximate"},
		{sdkmodel.ConfidenceApproximate, sdkmodel.ConfidenceApproximate, "approximate"},
	}
	for _, tt := range tests {
		got := weakerConfidence(tt.a, tt.b)
		if got != tt.expected {
			t.Errorf("weakerConfidence(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.expected)
		}
	}
}
