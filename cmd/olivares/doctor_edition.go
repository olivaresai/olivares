// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"sort"
	"strings"
)

// doctorEditionOverrides says which environment variables override a setting the edition keeps
// in the deployment settings (edition_settings.go): the console shows the saved choice, and a
// variable set in the engine's environment still wins. envs maps each variable to the setting
// it overrides. The values are never read into the report: they can be paths or secrets.
func doctorEditionOverrides(envs map[string]string, getenv func(string) string) doctorCheck {
	names := make([]string, 0, len(envs))
	for env := range envs {
		if getenv(env) != "" {
			names = append(names, env)
		}
	}
	check := doctorCheck{Name: "edition-setting-overrides", Status: "pass", Detail: "No environment variable overrides a setting saved in the console."}
	if len(names) == 0 {
		return check
	}
	sort.Strings(names)
	overrides := make([]string, 0, len(names))
	for _, env := range names {
		overrides = append(overrides, fmt.Sprintf("%s overrides %s", env, envs[env]))
	}
	check.Status = "warn"
	check.Detail = "Environment overrides of console settings: " + strings.Join(overrides, "; ")
	check.Remediation = "Remove these variables from the engine environment to use the settings saved in the console."
	return check
}
