// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// TestDoctorSaysWhereModelCallsGo: one informational row, never a failure, from the
// same posture the launch gate states at boot (session_inference_routing).
func TestDoctorSaysWhereModelCallsGo(t *testing.T) {
	o, deps := doctorFixture(t)
	for _, tc := range []struct{ base, want string }{
		{"", "Model calls go straight to the provider"},
		{"https://proxy-user:s3cret@inference.internal:8443/v1/?token=abc", "Model calls go through https://inference.internal:8443/v1/"},
	} {
		base := tc.base
		deps.getenv = func(k string) string {
			if k == envSessionBaseURL {
				return base
			}
			return ""
		}
		report, code, err := runDoctor(context.Background(), o, deps)
		if err != nil {
			t.Fatal(err)
		}
		row := doctorCheckByName(report, "session-inference-routing")
		if row.Status != "pass" || row.Required || row.Detail != tc.want {
			t.Fatalf("base %q: row = %+v, want pass, optional, %q", base, row, tc.want)
		}
		if code != exitcode.OK {
			t.Fatalf("base %q: doctor exit = %d; the row must never fail the install", base, code)
		}
		if strings.Contains(row.Detail, "s3cret") || strings.Contains(row.Detail, "abc") {
			t.Fatalf("the row printed a secret: %q", row.Detail)
		}
	}
}

// TestDoctorStatesSessionLaunchGatePosture: one row, never a failure, from the same
// setting the launch gate logs at boot. Unset refuses in every build; an explicit
// fail-open is a warning that names the control and the variable.
func TestDoctorStatesSessionLaunchGatePosture(t *testing.T) {
	o, deps := doctorFixture(t)
	for _, tc := range []struct {
		name, budget, context, status, detail string
		remediation                           []string
	}{
		{name: "unset", status: "pass", detail: "With this environment, a session whose budget or context policy cannot be read does not start"},
		{name: "explicit fail-closed", budget: "fail-closed", context: "fail-closed", status: "pass",
			detail: "With this environment, a session whose budget or context policy cannot be read does not start"},
		{name: "budget fail-open", budget: "fail-open", status: "warn",
			detail: "With this environment, a session starts even when its budget cannot be read", remediation: []string{envSessionBudgetAvailability}},
		{name: "both fail-open", budget: "fail-open", context: "FAIL-OPEN", status: "warn",
			detail:      "With this environment, a session starts even when its budget or context policy cannot be read",
			remediation: []string{envSessionBudgetAvailability, envSessionContextAvailability}},
	} {
		env := map[string]string{envSessionBudgetAvailability: tc.budget, envSessionContextAvailability: tc.context}
		deps.getenv = func(k string) string { return env[k] }
		report, code, err := runDoctor(context.Background(), o, deps)
		if err != nil {
			t.Fatal(err)
		}
		row := doctorCheckByName(report, "session-launch-gate")
		if row.Status != tc.status || row.Required || row.Detail != tc.detail {
			t.Fatalf("%s: row = %+v, want %s, optional, %q", tc.name, row, tc.status, tc.detail)
		}
		for _, v := range tc.remediation {
			if !strings.Contains(row.Remediation, v) {
				t.Fatalf("%s: remediation %q does not name %s", tc.name, row.Remediation, v)
			}
		}
		if code != exitcode.OK {
			t.Fatalf("%s: doctor exit = %d; the row must never fail the install", tc.name, code)
		}
	}
}
