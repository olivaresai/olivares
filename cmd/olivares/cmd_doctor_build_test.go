// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestDoctorBuildCheckCanonicalSourceStamp(t *testing.T) {
	oldVersion, oldCommit := version, commit
	t.Cleanup(func() { version, commit = oldVersion, oldCommit })
	// This test binary deliberately has no release OTA anchor. A canonical
	// source stamp must work without one; release and unknown stamps must not.
	for _, tt := range []struct {
		name, version, commit, licKey, otaKey, want string
	}{
		{"unstamped development", "dev", "none", "dev", "none", "pass"},
		{"canonical source", "437c1f637c", "437c1f637c", "dev", "none", "pass"},
		{"dirty canonical source", "437c1f637c-dirty", "437c1f637c", "dev", "none", "pass"},
		{"short git abbreviation", "437c", "437c", "dev", "none", "pass"},
		{"full git hash", "437c1f637cd5594180078695362683c93f6a11a5", "437c1f637cd5594180078695362683c93f6a11a5", "dev", "none", "pass"},
		{"release without anchor", "v26.9.1", "437c1f637c", "dev", "none", "fail"},
		{"bare semver without anchor", "26.9.1", "437c1f637c", "dev", "none", "fail"},
		{"release without commit", "v26.9.1", "none", "dev", "none", "fail"},
		{"unknown version", "local-build", "437c1f637c", "dev", "none", "fail"},
		{"mismatched hash", "437c1f637c", "137c1f637c", "dev", "none", "fail"},
		{"invalid hash", "437c1f637z", "437c1f637z", "dev", "none", "fail"},
		{"too short hash", "437", "437", "dev", "none", "fail"},
		{"overlong hash", "437c1f637cd5594180078695362683c93f6a11a500", "437c1f637cd5594180078695362683c93f6a11a500", "dev", "none", "fail"},
		{"dirty release", "v26.9.1-dirty", "437c1f637c", "dev", "none", "fail"},
		{"source with broken license anchor", "437c1f637c", "437c1f637c", "misconfigured", "none", "fail"},
		{"source with broken OTA anchor", "437c1f637c", "437c1f637c", "dev", "misconfigured", "fail"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			version, commit = tt.version, tt.commit
			if got := doctorBuildCheck(tt.licKey, tt.otaKey); got.Status != tt.want {
				t.Fatalf("build check = %+v, want %s", got, tt.want)
			}
		})
	}
}

func TestDoctorQuickstartWithCanonicalSourceStamp(t *testing.T) {
	oldVersion, oldCommit := version, commit
	t.Cleanup(func() { version, commit = oldVersion, oldCommit })
	version, commit = "437c1f637c", "437c1f637c"
	o, deps := quickstartDoctorFixture(t)
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.OK || report.Overall != "healthy" {
		t.Fatalf("source-built quickstart: code=%d overall=%s build=%+v", code, report.Overall, doctorCheckByName(report, "build-and-anchors"))
	}
}
