// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

var editionOverrideEnvs = map[string]string{
	"OLIVARES_SAMPLE_ALPHA_CONFIG": "sample-alpha",
	"OLIVARES_SAMPLE_BETA_CONFIG":  "sample-beta",
}

// A variable that overrides a setting the administrator saved is reported by name and by the
// setting it overrides. Its value can be a path or a secret and is never printed.
func TestDoctorEditionOverridesNamesTheVariableAndNeverItsValue(t *testing.T) {
	getenv := func(key string) string {
		if key == "OLIVARES_SAMPLE_ALPHA_CONFIG" {
			return "/etc/olivares/secret-path.json"
		}
		return ""
	}
	check := doctorEditionOverrides(editionOverrideEnvs, getenv)
	if check.Status != "warn" || check.Name != "edition-setting-overrides" {
		t.Fatalf("check = %+v; want a warning named edition-setting-overrides", check)
	}
	if !strings.Contains(check.Detail, "OLIVARES_SAMPLE_ALPHA_CONFIG") || !strings.Contains(check.Detail, "sample-alpha") {
		t.Fatalf("the detail must name the variable and the setting it overrides: %q", check.Detail)
	}
	if strings.Contains(check.Detail, "secret-path") || strings.Contains(check.Detail, "OLIVARES_SAMPLE_BETA_CONFIG") {
		t.Fatalf("the detail leaks a value or names a variable that is not set: %q", check.Detail)
	}
	if check.Remediation == "" {
		t.Fatal("a warning without a way out")
	}
}

func TestDoctorEditionOverridesPassesWhenNothingOverrides(t *testing.T) {
	check := doctorEditionOverrides(editionOverrideEnvs, func(string) string { return "" })
	if check.Status != "pass" {
		t.Fatalf("check = %+v; want pass", check)
	}
}
