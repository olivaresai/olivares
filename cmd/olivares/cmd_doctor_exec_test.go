// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"strings"
	"testing"
)

// `doctorExecutable` is the bound that answers gosec G204 for the two exec seams in
// defaultDoctorDeps. What it must do is narrow: refuse a RELATIVE name, where $PATH and not the
// operator would choose the program. What it must NOT pretend is that an absolute path is safe —
// an operator who can pass `--binary /anything` can run `/anything` without us.
//
// Both directions are here on purpose. A predicate that refused everything would satisfy every
// "refuses" case and break doctor entirely, and one that accepted everything is the state this
// change replaced.
func TestDoctorExecutableBound(t *testing.T) {
	for _, tc := range []struct {
		name    string
		program string
		wantErr bool
	}{
		{"a service manager the switch can produce", "systemctl", false},
		{"another one", "launchctl", false},
		{"another one", "rc-service", false},
		{"the operator's own --binary, absolute", "/usr/local/bin/olivares", false},
		{"an absolute path elsewhere is not our boundary to draw", "/tmp/olivares", false},
		{"a RELATIVE name: $PATH would choose, not the operator", "olivares", true},
		{"a relative path", "./olivares", true},
		{"a program nobody asked for", "curl", true},
		{"an uninstall command doctor does not use", "rc-update", true},
		{"empty", "", true},
	} {
		err := doctorExecutable(tc.program)
		if tc.wantErr && err == nil {
			t.Errorf("%s (%q): accepted, want refusal", tc.name, tc.program)
			continue
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s (%q): refused with %v, want accepted", tc.name, tc.program, err)
			continue
		}
		if tc.wantErr && !strings.Contains(err.Error(), "refuses to execute") {
			t.Errorf("%s (%q): the refusal does not say what it is: %v", tc.name, tc.program, err)
		}
	}
}

// The seam wired to the predicate, not merely the predicate: `run` and `runOutput` must consult it
// BEFORE building the command. Both are called with a relative name here, so a regression that
// dropped the check would try to execute `definitely-not-a-program` and fail with an exec error
// instead of the named refusal — which is what this assertion distinguishes.
func TestDoctorDepsRefuseBeforeExec(t *testing.T) {
	deps := defaultDoctorDeps()
	if _, err := deps.run(t.Context(), "definitely-not-a-program"); err == nil ||
		!strings.Contains(err.Error(), "refuses to execute") {
		t.Errorf("run: want the named refusal, got %v", err)
	}
	if _, _, err := deps.runOutput(t.Context(), "definitely-not-a-program"); err == nil ||
		!strings.Contains(err.Error(), "refuses to execute") {
		t.Errorf("runOutput: want the named refusal, got %v", err)
	}
}
