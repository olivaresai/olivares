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
