// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDoctorSystemdUnitFollowsTheInstallerThatWroteIt pins which unit a system
// doctor measures when --unit is not given.
//
// MEASURED 2026-10-06 on a clean Ubuntu 24.04 host after `dpkg -i`: the package
// installs /usr/lib/systemd/system/olivares.service and records that path in the
// install manifest, while doctor always stat'ed /etc/systemd/system/olivares.service
// (where install-service.sh writes). service-unit and install-manifest failed with
// "restore the installed file from the signed release" and ownership was unknown,
// on a correct install. All three read o.unit, so one resolver is the one cause.
func TestDoctorSystemdUnitFollowsTheInstallerThatWroteIt(t *testing.T) {
	const (
		etcUnit = "/etc/systemd/system/olivares.service"
		pkgUnit = "/usr/lib/systemd/system/olivares.service"
	)
	cases := []struct {
		name    string
		present []string
		statErr error // returned for etcUnit when it is not in present
		want    string
	}{
		{"shell installer unit", []string{etcUnit}, nil, etcUnit},
		{"deb or rpm unit", []string{pkgUnit}, nil, pkgUnit},
		// systemd itself loads /etc/systemd/system before /usr/lib/systemd/system.
		{"an /etc unit overrides the package unit", []string{etcUnit, pkgUnit}, nil, etcUnit},
		{"nothing installed keeps the shell installer path", nil, nil, etcUnit},
		// A stat that fails for a reason other than absence must be reported by the
		// checks that read o.unit, not hidden by quietly measuring the other file.
		{"unreadable /etc unit is not skipped", []string{pkgUnit}, fs.ErrPermission, etcUnit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, deps := doctorFixture(t)
			selfInfo, err := os.Stat(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			deps.stat = func(path string) (fs.FileInfo, error) {
				for _, p := range tc.present {
					if p == path {
						return selfInfo, nil
					}
				}
				if path == etcUnit && tc.statErr != nil {
					return nil, tc.statErr
				}
				return nil, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
			}
			o := &doctorOptions{mode: "system", init: "systemd", binary: "/usr/bin/olivares", server: "https://127.0.0.1:8443"}
			if err := resolveDoctorPaths(o, deps); err != nil {
				t.Fatal(err)
			}
			if o.unit != tc.want {
				t.Fatalf("unit = %q, want %q", o.unit, tc.want)
			}
		})
	}

	t.Run("an explicit --unit still wins", func(t *testing.T) {
		_, deps := doctorFixture(t)
		deps.stat = func(string) (fs.FileInfo, error) { return nil, errors.New("must not be asked") }
		o := &doctorOptions{mode: "system", init: "systemd", binary: "/usr/bin/olivares", unit: "/opt/x/olivares.service", server: "https://127.0.0.1:8443"}
		if err := resolveDoctorPaths(o, deps); err != nil {
			t.Fatal(err)
		}
		if o.unit != "/opt/x/olivares.service" {
			t.Fatalf("unit = %q", o.unit)
		}
	})
}

// uidInfo reports a chosen owner through Sys(), the way doctorFileUID reads a
// real stat, so the ownership check can be exercised without being root.
type uidInfo struct {
	fs.FileInfo
	uid uint32
}

func (i uidInfo) Sys() any { return struct{ Uid uint32 }{i.uid} }

// TestDoctorSystemPackageInstallPassesUnitManifestAndOwnership is the issue's
// exit criterion end to end: a v2 manifest that lists the package unit, no --unit,
// systemd, and the three checks that read the unit path all pass. The package
// unit exists only at its /usr/lib path (served from a fixture file) and nothing
// exists at the /etc path.
func TestDoctorSystemPackageInstallPassesUnitManifestAndOwnership(t *testing.T) {
	const serviceUID = 4242
	o, deps := doctorFixture(t)
	fixtureUnit := o.unit
	manifest, err := json.Marshal(map[string]any{
		"schema": "olivares.ai/local-install/v2", "mode": "system", "init": "systemd",
		"data_dir": o.dataDir, "config": o.config,
		"files": []map[string]string{
			{"path": o.binary, "mode": "0755"},
			{"path": o.config, "mode": "0600"},
			{"path": doctorSystemdPackageUnit, "mode": "0644"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.dataDir, "install-manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	onDisk := func(path string) string {
		if path == doctorSystemdPackageUnit {
			return fixtureUnit
		}
		return path
	}
	deps.lookupUID = func(string) (int, error) { return serviceUID, nil }
	deps.stat = func(path string) (fs.FileInfo, error) {
		if path == doctorSystemdAdminUnit {
			return nil, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
		}
		info, err := os.Stat(onDisk(path))
		if err != nil {
			return nil, err
		}
		uid := uint32(0)
		if path == o.dataDir {
			uid = serviceUID
		}
		return uidInfo{FileInfo: info, uid: uid}, nil
	}
	deps.readFile = func(path string) ([]byte, error) { return os.ReadFile(onDisk(path)) }

	system := &doctorOptions{
		mode: "system", init: "systemd", dataDir: o.dataDir, config: o.config, binary: o.binary,
		server: "https://127.0.0.1:8443", timeout: time.Second,
	}
	report, _, err := runDoctor(context.Background(), system, deps)
	if err != nil {
		t.Fatal(err)
	}
	if report.Paths.Unit != doctorSystemdPackageUnit {
		t.Fatalf("measured unit = %q, want the packaged %q", report.Paths.Unit, doctorSystemdPackageUnit)
	}
	for _, name := range []string{"service-unit", "install-manifest", "ownership"} {
		if check := doctorCheckByName(report, name); check.Status != "pass" {
			t.Errorf("%s = %+v, want pass", name, check)
		}
	}
	if unit := doctorCheckByName(report, "service-unit"); !strings.HasPrefix(unit.Detail, doctorSystemdPackageUnit) {
		t.Errorf("service-unit detail %q does not name the packaged unit", unit.Detail)
	}
}
