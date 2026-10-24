// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"testing"
)

func TestActivationAddonFactsPreserveUnknownAndKnownFalse(t *testing.T) {
	for _, value := range []*bool{nil, new(bool), new(true)} {
		data, err := json.Marshal(ActivationAddonDTO{Key: "control", InBuild: value, LicenseCovered: value})
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"in_build", "license_covered"} {
			got, exists := fields[key]
			if value == nil {
				if exists {
					t.Errorf("unknown %s must be omitted, got %v", key, got)
				}
			} else if !exists || got != *value {
				t.Errorf("known %s = %v (present=%v), want %v", key, got, exists, *value)
			}
		}
	}
}

// A request names a preset or one family, never both and never neither; a family
// reaches the service as the selection "family:<key>", which a preset name can
// never spell (the regulated preset and the regulated family are different sets).
func TestActivationSelectionFromARequest(t *testing.T) {
	for _, tc := range []struct {
		name, preset, family, want string
		wantErr                    bool
	}{
		{name: "preset", preset: "starter", want: "starter"},
		{name: "family", family: "regulated", want: "family:regulated"},
		{name: "family with surrounding space", family: " regulated ", want: "family:regulated"},
		{name: "a list is not a family", family: "regulated,enterprise", wantErr: true},
		{name: "a combination is not a family", family: "regulated+business", wantErr: true},
		{name: "both", preset: "starter", family: "business", wantErr: true},
		{name: "neither", wantErr: true},
		{name: "blank", preset: " ", family: " ", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := activationSelection(tc.preset, tc.family)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("activationSelection(%q, %q) = %q, %v; want %q, error=%v", tc.preset, tc.family, got, err, tc.want, tc.wantErr)
			}
		})
	}
}
