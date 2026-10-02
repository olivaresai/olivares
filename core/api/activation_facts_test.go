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
