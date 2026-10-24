// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/release"
)

// Installing a commercial license on a Community build prints the one next step for this
// installation, the same words the console shows; nothing is printed where there is nothing
// to switch to.

func TestLicenseInstallOnACommunityBuildNamesTheNextStep(t *testing.T) {
	hermeticUpgradeEnv(t)
	asEdition(t, "community")
	out, err := installLicence(t, signTestLicence(t, "Acme Ltd", time.Now().Add(365*24*time.Hour)), "--data-dir", t.TempDir())
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	step := editionSwitchStep(installKind(), true)
	for _, want := range []string{
		"this installation runs the Community build",
		"olivares upgrade --edition business",
		step.Command, step.Then,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the install output does not say %q:\n%s", want, out)
		}
	}
}

func TestLicenseInstallSaysNothingToSwitchWhenThereIsNothingToSwitchTo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edition string
		expires time.Time
	}{
		{"a Business build", "enterprise", time.Now().Add(365 * 24 * time.Hour)},
		{"an expired license", "community", time.Now().Add(-24 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asEdition(t, tc.edition)
			out, err := installLicence(t, signTestLicence(t, "Acme Ltd", tc.expires), "--data-dir", t.TempDir())
			if err != nil {
				t.Fatalf("install: %v\n%s", err, out)
			}
			if strings.Contains(out, "--edition business") {
				t.Fatalf("a switch was offered where there is none:\n%s", out)
			}
		})
	}
}

func TestLicenseStatusCarriesTheSwitchForScripts(t *testing.T) {
	hermeticUpgradeEnv(t)
	asEdition(t, "community")
	dir := t.TempDir()
	if out, err := installLicence(t, signTestLicence(t, "Acme Ltd", time.Now().Add(365*24*time.Hour)), "--data-dir", dir); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	root := newRootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetArgs([]string{"license", "status", "--data-dir", dir, "-o", "json"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		EditionSwitch struct {
			To      string `json:"to"`
			Install string `json:"install"`
			Command string `json:"command"`
		} `json:"edition_switch"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("status is not JSON: %v\n%s", err, out.String())
	}
	if got.EditionSwitch.To != "business" || !strings.Contains(got.EditionSwitch.Command, "upgrade --edition business") || got.EditionSwitch.Install == "" {
		t.Fatalf("edition_switch = %+v", got.EditionSwitch)
	}
}

func TestLicenseInstallReportCarriesTheSwitchForScripts(t *testing.T) {
	hermeticUpgradeEnv(t)
	asEdition(t, "community")
	out, err := installLicence(t, signTestLicence(t, "Acme Ltd", time.Now().Add(365*24*time.Hour)), "--data-dir", t.TempDir(), "-o", "json")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	start := strings.Index(out, "{")
	var got struct {
		EditionSwitch *struct {
			To      string `json:"to"`
			Command string `json:"command"`
		} `json:"edition_switch"`
	}
	dec := json.NewDecoder(strings.NewReader(out[start:]))
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, out)
	}
	if got.EditionSwitch == nil || got.EditionSwitch.To != "business" || !strings.Contains(got.EditionSwitch.Command, "--edition business") {
		t.Fatalf("report edition_switch = %+v", got.EditionSwitch)
	}
}

// A revoked license is not a license to switch on. `license status` counts the revocation list in
// its status, and the step it prints must agree with that status. The control (the same license, no
// list) proves the setup offers a switch, so the revoked case is not silent for another reason.
func TestLicenseStatusOffersNoSwitchForARevokedLicense(t *testing.T) {
	hermeticUpgradeEnv(t)
	asEdition(t, "community")
	if !license.HasDevKey {
		t.Skip("this build ships no dev signing key")
	}
	now := time.Now().UTC()
	blob, err := license.Sign(license.Claims{
		Licensee: "Acme Ltd", Plan: "business", Serial: "ser-revoked",
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(365 * 24 * time.Hour),
	}, license.DevPrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if out, err := installLicence(t, blob, "--data-dir", dir); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	otaPub, otaPriv, _ := ed25519.GenerateKey(nil)
	manifest := writeCRLManifest(t, t.TempDir(), &release.RevokedSet{Serials: []string{"ser-revoked"}}, otaPriv)

	status := func(extra ...string) map[string]any {
		t.Helper()
		root := newRootCmd()
		var out strings.Builder
		root.SetOut(&out)
		root.SetArgs(append([]string{"license", "status", "--data-dir", dir, "-o", "json"}, extra...))
		if _, err := root.ExecuteC(); err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
			t.Fatalf("status is not JSON: %v\n%s", err, out.String())
		}
		return doc
	}
	if control := status(); control["edition_switch"] == nil {
		t.Fatalf("the control (no revocation list) offers no switch, so the revoked case proves nothing: %v", control)
	}
	revoked := status("--manifest", manifest, "--ota-pubkey", base64.StdEncoding.EncodeToString(otaPub))
	if revoked["status"] != "revoked" || revoked["edition_switch"] != nil {
		t.Fatalf("status=%v edition_switch=%v: a revoked license must print no switch", revoked["status"], revoked["edition_switch"])
	}
}
