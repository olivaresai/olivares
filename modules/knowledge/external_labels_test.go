// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import "testing"

func TestExternalLabelsAllowed(t *testing.T) {
	tests := []struct {
		name       string
		labels     []string
		clearances []string
		want       bool
	}{
		{"no labels", nil, nil, true},
		{"labels but no clearances", []string{"purview:confidential"}, nil, false},
		{"exact match", []string{"purview:confidential"}, []string{"purview:confidential"}, true},
		{"no match", []string{"purview:highly-confidential"}, []string{"purview:confidential"}, false},
		{"wildcard match", []string{"purview:highly-confidential"}, []string{"purview:*"}, true},
		{"wildcard no match", []string{"uc:pii"}, []string{"purview:*"}, false},
		{"multiple labels one matches", []string{"purview:internal", "uc:pii"}, []string{"uc:pii"}, true},
		{"empty labels slice", []string{}, nil, true},
		{"multiple clearances one matches", []string{"purview:confidential"}, []string{"uc:pii", "purview:confidential"}, true},
		{"wildcard matches subpath", []string{"purview:secret/level2"}, []string{"purview:*"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := externalLabelsAllowed(tt.labels, tt.clearances); got != tt.want {
				t.Errorf("externalLabelsAllowed(%v, %v) = %v, want %v", tt.labels, tt.clearances, got, tt.want)
			}
		})
	}
}

func TestLabelMatchesClearance(t *testing.T) {
	tests := []struct {
		label      string
		clearances []string
		want       bool
	}{
		{"purview:confidential", []string{"purview:confidential"}, true},
		{"purview:confidential", []string{"purview:*"}, true},
		{"purview:confidential", []string{"uc:*"}, false},
		{"gdrive:restricted", []string{"gdrive:restricted", "purview:*"}, true},
		{"gdrive:restricted", []string{"purview:*"}, false},
		{"uc:pii", []string{"uc:pii"}, true},
		{"uc:pii", []string{"uc:*"}, true},
		{"uc:phi", []string{"uc:pii"}, false},
		{"purview:public", []string{"purview:*"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.label+"/"+tt.clearances[0], func(t *testing.T) {
			if got := labelMatchesClearance(tt.label, tt.clearances); got != tt.want {
				t.Errorf("labelMatchesClearance(%q, %v) = %v, want %v", tt.label, tt.clearances, got, tt.want)
			}
		})
	}
}
