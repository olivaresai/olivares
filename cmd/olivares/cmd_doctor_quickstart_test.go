// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// quickstartDoctorFixture is a LOCAL install: a data directory with the engine's
// own state, and none of the files the pinned service installer leaves behind. It
// is built from doctorFixture and then STRIPPED, so the difference between the two
// fixtures is exactly the difference between the two install shapes.
func quickstartDoctorFixture(t *testing.T) (*doctorOptions, doctorDeps) {
	t.Helper()
	o, deps := doctorFixture(t)
	// What quickstart never writes.
	for _, path := range []string{o.config, o.unit, filepath.Join(o.dataDir, "install-manifest.json")} {
		if err := os.Remove(path); err != nil {
			t.Fatalf("strip %s: %v", path, err)
		}
	}
	// And no service manager answers, which is what `--init unknown` means here.
	o.init = "unknown"
	o.unit = ""
	deps.run = func(context.Context, string, ...string) (int, error) { return 1, nil }
	return o, deps
}

// TestDoctorReachesHealthyOnAQuickstartInstall pins the required set a
// quickstart install is actually judged against. Measured 2026-09-18: `quickstart` reached a console, minted its token and created an
// administrator, and doctor answered OVERALL unhealthy with five required checks
// failing or unknown — every remedy naming the pinned service installer, which
// quickstart is the alternative to.
func TestDoctorReachesHealthyOnAQuickstartInstall(t *testing.T) {
	o, deps := quickstartDoctorFixture(t)

	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if report.InstallShape != installShapeLocal {
		t.Fatalf("install_shape = %q, want %q", report.InstallShape, installShapeLocal)
	}
	if code != exitcode.OK || report.Overall != "healthy" {
		var bad []string
		for _, c := range report.Checks {
			if c.Required && c.Status != "pass" && c.Status != "not_applicable" {
				bad = append(bad, c.Name+"="+c.Status+" ("+c.Remediation+")")
			}
		}
		t.Fatalf("doctor on a quickstart install: code %d overall %q; required and not passing: %s",
			code, report.Overall, strings.Join(bad, " · "))
	}
	// The five the walk measured are not applicable, not required, and their
	// remedies name something that applies to THIS installation.
	for _, name := range []string{"configuration", "service-unit", "install-manifest", "ownership", "init-state"} {
		c := doctorCheckByName(report, name)
		if c.Status != "not_applicable" || c.Required {
			t.Errorf("%s = status %q required %v, want a not-applicable optional check", name, c.Status, c.Required)
		}
		if strings.Contains(c.Remediation, "pinned service installer") && !strings.Contains(c.Remediation, "only the pinned") {
			t.Errorf("%s still sends a local install to the service installer: %q", name, c.Remediation)
		}
	}
	// And what a local install DOES own is still required.
	for _, name := range []string{"binary", "data-directory", "livez", "readyz", "store"} {
		if c := doctorCheckByName(report, name); !c.Required || c.Status != "pass" {
			t.Errorf("%s = status %q required %v; a local install still answers for this", name, c.Status, c.Required)
		}
	}
}

// TestDoctorStillRequiresTheServicePostureOnAServiceInstall is the other side of
// the same predicate: the relaxation must not become a way to pass a broken
// service install. The fixture keeps the manifest (so the shape is service) and
// breaks the unit.
func TestDoctorStillRequiresTheServicePostureOnAServiceInstall(t *testing.T) {
	o, deps := doctorFixture(t)
	if err := os.Remove(o.unit); err != nil {
		t.Fatalf("remove unit: %v", err)
	}

	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if report.InstallShape != installShapeService {
		t.Fatalf("install_shape = %q, want %q (an ownership manifest is on disk)", report.InstallShape, installShapeService)
	}
	unit := doctorCheckByName(report, "service-unit")
	if unit.Status == "not_applicable" || !unit.Required {
		t.Fatalf("a missing unit on a SERVICE install was excused: %+v", unit)
	}
	if code == exitcode.OK {
		t.Fatalf("a service install with no unit reported healthy (code %d)", code)
	}
}

// TestAManifestThatCannotBeReadIsStillAServiceInstall pins the deny-closed half of
// the classification: "I cannot read it" is never "it is not there", so a
// permission fault inside a service install cannot be laundered into a local
// install with nothing to check.
func TestAManifestThatCannotBeReadIsStillAServiceInstall(t *testing.T) {
	o, deps := quickstartDoctorFixture(t)
	manifest := filepath.Join(o.dataDir, "install-manifest.json")
	deps.stat = func(path string) (os.FileInfo, error) {
		if path == manifest {
			return nil, os.ErrPermission
		}
		return os.Stat(path)
	}

	if shape := detectDoctorInstallShape(deps, *o, manifest); shape != installShapeService {
		t.Fatalf("an unreadable manifest classified as %q", shape)
	}
}

// TestRelaxationNeverDowngradesAPassingCheck: a local install that happens to
// keep an env file measured something real, and a relaxation that erased it would
// be hiding a fact rather than classifying one.
func TestRelaxationNeverDowngradesAPassingCheck(t *testing.T) {
	checks := []doctorCheck{
		{Name: "configuration", Status: "pass", Required: true},
		{Name: "install-manifest", Status: "fail", Required: true, Remediation: "rerun the pinned service installer"},
		{Name: "livez", Status: "fail", Required: true},
	}
	out := relaxLocalInstallChecks(installShapeLocal, checks)
	if out[0].Status != "pass" || !out[0].Required {
		t.Fatalf("a passing service-shaped check was downgraded: %+v", out[0])
	}
	if out[1].Status != "not_applicable" || out[1].Required {
		t.Fatalf("the failing service-shaped check was not relaxed: %+v", out[1])
	}
	if out[2].Status != "fail" || !out[2].Required {
		t.Fatalf("a check that is not service-shaped was relaxed: %+v", out[2])
	}
	// And on a service install nothing moves at all.
	same := []doctorCheck{{Name: "install-manifest", Status: "fail", Required: true}}
	if got := relaxLocalInstallChecks(installShapeService, same); got[0].Status != "fail" || !got[0].Required {
		t.Fatalf("a service install was relaxed: %+v", got[0])
	}
}
