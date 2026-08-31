// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
)

// the license half of "the operator cannot name what they are doing":
// there was install/status/verify and no uninstall, and an install over an
// existing license replaced it silently, non-atomically, and did not keep the
// mode its own help promises.

// signTestLicence mints a blob with the dev key this build compiles in.
func signTestLicence(t *testing.T, licensee string, expires time.Time) string {
	t.Helper()
	if !license.HasDevKey {
		t.Skip("this build ships no dev signing key")
	}
	blob, err := license.Sign(license.Claims{
		Licensee: licensee, Plan: "business", IssuedAt: time.Now().UTC(), ExpiresAt: expires,
	}, license.DevPrivateKey())
	if err != nil {
		t.Fatalf("sign a test license: %v", err)
	}
	return blob
}

// installLicence runs the real CLI with the blob on stdin.
func installLicence(t *testing.T, blob string, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(blob))
	root.SetArgs(append([]string{"license", "install", "-"}, args...))
	_, err := root.ExecuteC()
	return out.String(), err
}

// TestLicenseInstallKeepsThePromisedModeOverAnExistingFile.
//
// Measured 2026-08-09 against the shipped binary: install used os.WriteFile,
// whose perm argument applies ONLY WHEN IT CREATES THE FILE, so a target that
// already existed 0644 stayed 0644 with the license in it — while the command's
// own help, LICENSING.md and the upgrade runbook all say 0600. The identical trap is
// documented and fixed 200 lines below in the same file, for keygen only.
//
// A pre-existing 0644 license file is not a hypothetical: a restore from backup,
// a `cp` install, or a first install under a permissive umask all produce one,
// and every RENEWAL then writes through it.
func TestLicenseInstallKeepsThePromisedModeOverAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, licenseFileName)
	if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil { //nolint:gosec // the point of the fixture
		t.Fatal(err)
	}
	blob := signTestLicence(t, "Acme Ltd", time.Now().Add(365*24*time.Hour))
	if out, err := installLicence(t, blob, "--data-dir", dir); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the installed license: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("the installed license has mode %04o; install's own help promises 0600", got)
	}
	// And it is the new license, not a truncated remnant.
	raw, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.TrimSpace(string(raw)) != blob {
		t.Fatal("the installed file does not hold the blob that was installed")
	}
}

// TestLicenseInstallNamesTheLicenceItReplaced: a renewal is a transition, and
// "installed" printed over a license that was already there reads as a first
// install. The non-fire direction is asserted too — a genuine first install must
// not claim to have replaced anything.
func TestLicenseInstallNamesTheLicenceItReplaced(t *testing.T) {
	dir := t.TempDir()
	first := signTestLicence(t, "Acme Ltd", time.Date(2027, 7, 14, 0, 0, 0, 0, time.UTC))
	second := signTestLicence(t, "Acme Ltd", time.Date(2028, 7, 14, 0, 0, 0, 0, time.UTC))

	out1, err := installLicence(t, first, "--data-dir", dir)
	if err != nil {
		t.Fatalf("first install: %v\n%s", err, out1)
	}
	if strings.Contains(out1, "REPLACED") {
		t.Fatalf("a first install replaced nothing and must not say it did:\n%s", out1)
	}

	out2, err := installLicence(t, second, "--data-dir", dir)
	if err != nil {
		t.Fatalf("renewal: %v\n%s", err, out2)
	}
	if !strings.Contains(out2, "REPLACED") {
		t.Fatalf("a renewal replaced a license and must say so:\n%s", out2)
	}
	if !strings.Contains(out2, "2027-07-14") {
		t.Fatalf("the transition must identify the license that was replaced, by its term:\n%s", out2)
	}
}

// TestLicenseInstallRefusesUnderABootOverride. Installing while
// OLIVARES_LICENSE* outranks the data-dir file used to persist and print a
// warning: exit 0 over a change the engine will never read. The console half
// (licenseService.InstallLicense) already refused, and the upgrade runbook
// already documented the CLI as refusing — this stops one surface disagreeing
// with the other two.
func TestLicenseInstallRefusesUnderABootOverride(t *testing.T) {
	dir := t.TempDir()
	blob := signTestLicence(t, "Acme Ltd", time.Now().Add(365*24*time.Hour))
	t.Setenv("OLIVARES_LICENSE", blob)

	out, err := installLicence(t, blob, "--data-dir", dir)
	if err == nil {
		t.Fatalf("an install that changes nothing the engine reads must refuse:\n%s", out)
	}
	if !strings.Contains(err.Error(), "OUTRANKS") {
		t.Errorf("the refusal must say WHY, got: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, licenseFileName)); serr == nil {
		t.Fatal("the refused install still wrote the file")
	}

	// --force is the named way through, for staging a file before the override
	// goes away. Without this the refusal above could be a wall with no door.
	if out, ferr := installLicence(t, blob, "--data-dir", dir, "--force"); ferr != nil {
		t.Fatalf("--force must stage the file anyway: %v\n%s", ferr, out)
	}
	if _, serr := os.Stat(filepath.Join(dir, licenseFileName)); serr != nil {
		t.Fatalf("--force did not stage the file: %v", serr)
	}
}

// TestLicenseUninstallRemovesTheLicenceAndSaysWhatRemains covers the verb that
// did not exist in the CLI at all, though the console has had it since
// (DELETE /v1/console/license) and the upgrade runbook lists license removal as
// supported. Removing a file by hand is not the same thing: nothing told the
// operator whether an override was still supplying one.
func TestLicenseUninstallRemovesTheLicenceAndSaysWhatRemains(t *testing.T) {
	dir := t.TempDir()
	blob := signTestLicence(t, "Acme Ltd", time.Date(2028, 7, 14, 0, 0, 0, 0, time.UTC))
	if out, err := installLicence(t, blob, "--data-dir", dir); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	path := filepath.Join(dir, licenseFileName)

	// Destructive, so it asks. A non-interactive session has nobody to ask.
	if out, err := runCLI(t, "license", "uninstall", "--data-dir", dir); err == nil {
		t.Fatalf("removing a license must be consent-gated:\n%s", out)
	}
	if _, serr := os.Stat(path); serr != nil {
		t.Fatal("the unconfirmed uninstall removed the license anyway")
	}

	out, err := runCLI(t, "license", "uninstall", "--data-dir", dir, "--yes")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if _, serr := os.Stat(path); serr == nil {
		t.Fatal("uninstall reported success and left the license in place")
	}
	if !strings.Contains(out, "Acme Ltd") {
		t.Errorf("uninstall must name what it removed:\n%s", out)
	}
	if !strings.Contains(out, `"now_resolves"`) || !strings.Contains(out, "none") {
		t.Errorf("uninstall must report what the engine resolves afterwards, or 'removed' is half an answer:\n%s", out)
	}

	// Removing what is not there is not a failure — and it must not claim it
	// removed something.
	again, aerr := runCLI(t, "license", "uninstall", "--data-dir", dir, "--yes")
	if aerr != nil {
		t.Fatalf("uninstalling nothing must not fail: %v\n%s", aerr, again)
	}
	if !strings.Contains(again, `"removed": false`) {
		t.Errorf("uninstalling nothing must report removed=false:\n%s", again)
	}
}

// TestLicenseUninstallRefusesUnderABootOverride: deleting the data-dir file
// while OLIVARES_LICENSE* supplies one would look like it worked and change
// nothing. Same refusal the console makes (api.ErrLicenseManagedExternally).
func TestLicenseUninstallRefusesUnderABootOverride(t *testing.T) {
	dir := t.TempDir()
	blob := signTestLicence(t, "Acme Ltd", time.Now().Add(365*24*time.Hour))
	if out, err := installLicence(t, blob, "--data-dir", dir); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	t.Setenv("OLIVARES_LICENSE", blob)

	out, err := runCLI(t, "license", "uninstall", "--data-dir", dir, "--yes")
	if err == nil {
		t.Fatalf("uninstall under an active override must refuse:\n%s", out)
	}
	if !strings.Contains(err.Error(), "OUTRANKS") {
		t.Errorf("the refusal must say why, got: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, licenseFileName)); serr != nil {
		t.Fatal("the refused uninstall removed the license anyway")
	}
}
