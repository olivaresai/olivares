// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/license"
)

// C03-02: `olivares license sign --features` is the first CLI consumer of
// Claims.Features. Sign already copies the field; the hole was the flag.

func signViaCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	if !license.HasDevKey {
		t.Skip("this build ships no dev signing key")
	}
	root := newRootCmd()
	var out, errOut strings.Builder
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"license", "sign"}, args...))
	_, err := root.ExecuteC()
	return strings.TrimSpace(out.String()), err
}

func TestLicenseSignFeaturesRoundTrip(t *testing.T) {
	blob, err := signViaCLI(t,
		"--licensee", "Acme",
		"--plan", "business",
		"--expires", "2027-07-14T00:00:00Z",
		"--features", "regulated, ai-runtime-security",
	)
	if err != nil {
		t.Fatalf("sign --features: %v", err)
	}
	v, verr := license.VerifyEnvelope(blob, license.DefaultPublicKey())
	if verr != nil {
		t.Fatalf("verify signed blob: %v", verr)
	}
	got := v.Features()
	want := []string{"regulated", "ai-runtime-security"}
	if !slices.Equal(got, want) {
		t.Fatalf("Features() = %#v, want %#v — --features did not reach Sign", got, want)
	}
}

func TestLicenseSignWithoutFeaturesHasNone(t *testing.T) {
	blob, err := signViaCLI(t, "--licensee", "Acme", "--plan", "business")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	v, verr := license.VerifyEnvelope(blob, license.DefaultPublicKey())
	if verr != nil {
		t.Fatalf("verify: %v", verr)
	}
	if got := v.Features(); len(got) != 0 {
		t.Fatalf("unsigned --features minted %#v", got)
	}
}

func TestLicenseSignRefusesEmptyFeatureTag(t *testing.T) {
	if _, err := signViaCLI(t, "--licensee", "Acme", "--features", "regulated,"); err == nil {
		t.Fatal("trailing comma was accepted — would mint an empty feature tag")
	}
}

func TestParseLicenseFeatures(t *testing.T) {
	got, err := parseLicenseFeatures("  regulated, identity-scale ")
	if err != nil || !slices.Equal(got, []string{"regulated", "identity-scale"}) {
		t.Fatalf("parse = %#v / %v", got, err)
	}
	if _, err := parseLicenseFeatures(","); err == nil {
		t.Fatal("bare comma was accepted")
	}
	if got, err := parseLicenseFeatures(""); err != nil || got != nil {
		t.Fatalf("empty = %#v / %v", got, err)
	}
}

func TestLicenseSignRefusesInventedFeatureID(t *testing.T) {
	if _, err := signViaCLI(t, "--licensee", "Acme", "--features", "business-max"); err == nil {
		t.Fatal("invented feature id was accepted — issued licenses would leave the fused canon")
	}
	if _, err := parseLicenseFeatures("enterprise"); err == nil {
		t.Fatal("enterprise is not one of the four add-ons")
	}
}
