// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// resolveLicense must honor the precedence explicit(--license) > OLIVARES_LICENSE_PATH
// > OLIVARES_LICENSE > <data-dir>/license.key > none (§3 point 2).
func TestResolveLicensePrecedence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(licenseDataDirPath(dir), []byte("DATADIR_BLOB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	flagFile := filepath.Join(dir, "flag.lic")
	if err := os.WriteFile(flagFile, []byte("  FLAG_BLOB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(dir, "env.lic")
	if err := os.WriteFile(envFile, []byte("ENVPATH_BLOB"), 0o600); err != nil {
		t.Fatal(err)
	}
	full := map[string]string{"OLIVARES_LICENSE_PATH": envFile, "OLIVARES_LICENSE": "ENVINLINE"}

	cases := []struct {
		name     string
		explicit string
		env      map[string]string
		dataDir  string
		wantKind string
		wantBlob string
	}{
		{"flag wins over all", flagFile, full, dir, licenseSourceFlag, "FLAG_BLOB"},
		{"env-path over env-inline+data-dir", "", full, dir, licenseSourceEnvPath, "ENVPATH_BLOB"},
		{"env-inline over data-dir", "", map[string]string{"OLIVARES_LICENSE": "ENVINLINE"}, dir, licenseSourceEnvInline, "ENVINLINE"},
		{"data-dir default", "", nil, dir, licenseSourceDataDir, "DATADIR_BLOB"},
		{"none when nothing set", "", nil, t.TempDir(), licenseSourceNone, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src, err := resolveLicense(c.explicit, c.dataDir, envFunc(c.env))
			if err != nil {
				t.Fatalf("resolveLicense: %v", err)
			}
			if src.Kind != c.wantKind {
				t.Errorf("Kind = %q, want %q", src.Kind, c.wantKind)
			}
			if src.Blob != c.wantBlob {
				t.Errorf("Blob = %q, want %q (trimmed)", src.Blob, c.wantBlob)
			}
		})
	}
}

// An explicit file source that is SET but unreadable must fail loudly (operator
// intent), but the data-dir default being absent is not an error.
func TestResolveLicenseUnreadableExplicitErrors(t *testing.T) {
	if _, err := resolveLicense(filepath.Join(t.TempDir(), "missing.lic"), t.TempDir(), envFunc(nil)); err == nil {
		t.Error("an unreadable --license must error")
	}
	missing := filepath.Join(t.TempDir(), "missing.lic")
	if _, err := resolveLicense("", t.TempDir(), envFunc(map[string]string{"OLIVARES_LICENSE_PATH": missing})); err == nil {
		t.Error("an unreadable OLIVARES_LICENSE_PATH must error")
	}
	// The data-dir default being absent is NOT an error.
	src, err := resolveLicense("", t.TempDir(), envFunc(nil))
	if err != nil || src.Kind != licenseSourceNone {
		t.Errorf("absent data-dir default: got (%+v, %v), want none/nil", src, err)
	}
}

func TestLicenseOverridePresent(t *testing.T) {
	if _, _, present := licenseOverridePresent("", envFunc(nil)); present {
		t.Error("no override should be present with empty flag+env")
	}
	if k, _, present := licenseOverridePresent("/x.lic", envFunc(nil)); !present || k != licenseSourceFlag {
		t.Errorf("flag override: present=%v kind=%q", present, k)
	}
	if k, _, present := licenseOverridePresent("", envFunc(map[string]string{"OLIVARES_LICENSE_PATH": "/p"})); !present || k != licenseSourceEnvPath {
		t.Errorf("env-path override: present=%v kind=%q", present, k)
	}
	if k, _, present := licenseOverridePresent("", envFunc(map[string]string{"OLIVARES_LICENSE": "x"})); !present || k != licenseSourceEnvInline {
		t.Errorf("env-inline override: present=%v kind=%q", present, k)
	}
	// A data-dir-only source is NOT an external override (install is allowed there).
	src := licenseSource{Kind: licenseSourceDataDir}
	if src.External() {
		t.Error("data-dir source must not be External()")
	}
}
