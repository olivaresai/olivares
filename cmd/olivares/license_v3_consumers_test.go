// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// The consumer half of the v3 routing change.
//
// core/license proves the ROUTER reads both containers. That is necessary and it is not the P0:
// the defect measured on 2026-08-11 was that ten call sites — the holder, the seat seam, the
// installer, the CLI reports, the CRL observation, the enterprise-download gate and the engine's
// own server-info — all called license.Verify, which returns the flat claim set alone, so a
// deployment handed the credential this project signs reported "invalid" for a license it had
// just been sold. A green core/license suite would have said nothing about that, which is
// exactly how the credential got built, tested and left unplugged.
//
// So these tests drive the CONSUMERS with a signed v3 credential and assert what an operator
// would see.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
)

// signTestCredentialV3 signs the frozen cross-language vector — the bytes the TypeScript issuer
// actually produces — with the dev key the holder verifies against. Building the payload here
// instead of reusing the vector would test this file's idea of the wire, not the wire.
func signTestCredentialV3(t *testing.T) string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Clean("../../core/license/testdata/credential_v3_ts_vector.json"))
	if err != nil {
		t.Fatalf("the cross-language vector is missing: %v", err)
	}
	priv := license.DevPrivateKey()
	if len(priv) != ed25519.PrivateKeySize {
		t.Skip("this build embeds no dev key")
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(ed25519.Sign(priv, payload))
}

// The vector's base line runs in `term` to 2026-08-31; its add-on's lease ended 2026-08-10. Both
// instants are FIXED here rather than taken from the wall clock: a test that read time.Now()
// would pass today and start failing on 2026-09-01 for a reason that has nothing to do with the
// code.
var (
	insideTheBaseTerm = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	pastEverything    = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
)

// TestTheHolderReadsAV3Credential is the hot-apply path — what `license install`, the console and
// a SIGHUP reload all converge on. Before the routing change this reported "invalid".
func TestTheHolderReadsAV3Credential(t *testing.T) {
	pub := license.DefaultPublicKey()
	if len(pub) == 0 {
		t.Skip("this build embeds no verification key")
	}
	clock := func() time.Time { return insideTheBaseTerm }
	h := newLicenseHolder(pub, licenseSource{Kind: licenseSourceNone}, clock, discardLogger())

	d := h.set(licenseSource{Blob: signTestCredentialV3(t), Kind: licenseSourceDataDir})
	if d.status != "valid" {
		t.Fatalf("holder status = %q, want valid — the engine rejected the credential the issuer signs", d.status)
	}
	if d.licensee != "DODO-10 Sub A" {
		t.Fatalf("licensee = %q, want %q", d.licensee, "DODO-10 Sub A")
	}
	if !d.verified {
		t.Fatal("a correctly-signed credential was not marked verified")
	}
	if !d.lic.IsCredentialV3() || len(d.lic.Grants()) != 2 {
		t.Fatalf("the holder kept %d grant line(s); the purchase has 2", len(d.lic.Grants()))
	}

	// Non-firing direction: the same credential past every line is expired, so "valid" above is
	// not a verifier that says yes to anything.
	expired := newLicenseHolder(pub, licenseSource{Blob: signTestCredentialV3(t), Kind: licenseSourceDataDir},
		func() time.Time { return pastEverything }, discardLogger())
	if got := expired.display().status; got != "expired" {
		t.Fatalf("status past every line = %q, want expired", got)
	}
	// And a blob that does not verify is still invalid, container or not.
	broken := newLicenseHolder(pub, licenseSource{Blob: signTestCredentialV3(t) + "x", Kind: licenseSourceDataDir}, clock, discardLogger())
	if got := broken.display().status; got != "invalid" {
		t.Fatalf("status of a tampered credential = %q, want invalid", got)
	}
}

// TestTheStatusDTOCarriesTheCredentialFacts covers what the console and server-info render.
func TestTheStatusDTOCarriesTheCredentialFacts(t *testing.T) {
	pub := license.DefaultPublicKey()
	if len(pub) == 0 {
		t.Skip("this build embeds no verification key")
	}
	h := newLicenseHolder(pub, licenseSource{Blob: signTestCredentialV3(t), Kind: licenseSourceDataDir},
		func() time.Time { return insideTheBaseTerm }, discardLogger())
	svc := &licenseService{holder: h, edition: "community", log: discardLogger(), getenv: func(string) string { return "" }}

	info := svc.LicenseDisplay()
	if info.Status != "valid" || info.Licensee != "DODO-10 Sub A" {
		t.Fatalf("server-info display = %+v, want valid/DODO-10 Sub A", info)
	}
	// The two labels a v3 credential does not carry stay EMPTY rather than being filled from the
	// nearest-looking field. The vector's `support_profile` is "business" — a product tier, not a
	// support relationship — and mapping it by name would render "Support: business" in a console
	// badge, which is a fact nobody signed sitting in a slot that means something else. It is
	// reachable under its own name instead.
	if info.SupportTier != "" {
		t.Fatalf("support tier = %q, want empty: support_profile is a different field, not a rename", info.SupportTier)
	}
	if info.Plan != "" {
		t.Fatalf("plan = %q, want empty: v3 replaced the one-line plan label with the grant list", info.Plan)
	}
	lic, err := license.VerifyEnvelope(signTestCredentialV3(t), pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got := lic.SupportProfile(); got != "business" {
		t.Fatalf("SupportProfile() = %q, want business — the field must be reachable under its own name", got)
	}
}

// TestTheCLIReportShowsEveryPurchasedLine is the anti-flattening assertion at the surface an
// operator actually reads. A report that printed one product and one date would be the exact
// aggregation the container exists to prevent — and it would look perfectly fine.
func TestTheCLIReportShowsEveryPurchasedLine(t *testing.T) {
	pub := license.DefaultPublicKey()
	if len(pub) == 0 {
		t.Skip("this build embeds no verification key")
	}
	lic, err := license.VerifyEnvelope(signTestCredentialV3(t), pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	key, body := licenseReport(lic, insideTheBaseTerm)
	if key != "credential" {
		t.Fatalf("report key = %q, want credential (a v3 has no flat claim set to print)", key)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	doc := string(raw)
	for _, want := range []string{
		"pdt_0NkL6fPms1DwlDsUUcawf", // the base product
		// `adn_` is the Dodo add-on product-id PREFIX, not a misspelling of "and": the id
		// is four times over in the SIGNED cross-language vector this test verifies
		// (core/license/testdata/credential_v3_ts_vector.json), whose bytes cannot be
		// edited to match without invalidating the signature. The misspell pass of
		// 9e077afb5 rewrote it here and left this test red.
		"adn_0NkL6ebYpt2k6K5c973u8", // the add-on, which a flat report would have dropped
		`"issuance_phase":"term"`,
		`"issuance_phase":"refund_window"`, // the two lines are in DIFFERENT phases
		"cred_01JZQAP6C8M4R2T7V9W1X3Y5ZA",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("the report does not mention %q; it printed:\n%s", want, doc)
		}
	}

	// The lifecycle block agrees with the holder, and prints no invented profile.
	life := licenseLifecycle(lic, license.Revocation{}, "", false, insideTheBaseTerm)
	if life["status"] != string(license.StatusValid) {
		t.Fatalf("lifecycle status = %v, want valid", life["status"])
	}
	if _, ok := life["profile"]; ok {
		t.Fatalf("lifecycle printed a profile %v for a credential that carries none", life["profile"])
	}
	// And the flat container still reports exactly as it did, profile included.
	flat, err := license.VerifyEnvelope(signTestLicense(t, license.Claims{
		Licensee: "Acme", IssuedAt: insideTheBaseTerm, ExpiresAt: insideTheBaseTerm.Add(24 * time.Hour),
	}), pub)
	if err != nil {
		t.Fatalf("verify flat: %v", err)
	}
	if k, _ := licenseReport(flat, insideTheBaseTerm); k != "claims" {
		t.Fatalf("flat report key = %q, want claims", k)
	}
	if life := licenseLifecycle(flat, license.Revocation{}, "", false, insideTheBaseTerm); life["profile"] != "online" {
		t.Fatalf("flat lifecycle profile = %v, want online", life["profile"])
	}
}

// TestNoConsumerReadsLicencesByTheFlatOnlyPath is the criterion "the ten call sites see the same
// thing", enforced mechanically instead of by review.
//
// It is a guard and not a style rule. The failure it exists to catch is not "someone wrote the
// wrong function": it is a deployment where `olivares license status` accepts a credential and
// server-info calls the same credential invalid, because two call sites of ten took different
// entry points. That divergence is invisible in any single test — each half passes — and it is
// how the original defect stayed alive through a green suite.
//
// license.Verify is NOT deprecated: it is the flat container's reader and core/license keeps
// testing it directly. What is refused is a CONSUMER reaching for the flat-only path, which
// silently answers "no license" for a container this project signs.
func TestNoConsumerReadsLicencesByTheFlatOnlyPath(t *testing.T) {
	roots := []string{".", filepath.Join("..", "..", "core", "api")}
	offenders := []string{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("read %s: %v", root, err)
		}
		scanned := 0
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			scanned++
			body, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			for i, line := range strings.Split(string(body), "\n") {
				// The call, not the word: the comments in these files legitimately discuss
				// license.Verify and its history.
				if strings.Contains(line, "license.Verify(") {
					offenders = append(offenders, fmt.Sprintf("%s/%s:%d", root, name, i+1))
				}
			}
		}
		// The control: a guard that scanned nothing would pass forever. If these directories are
		// ever moved, this fails loudly instead of going quiet.
		if scanned == 0 {
			t.Fatalf("scanned no production source under %s; the guard is measuring nothing", root)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("these consumers read licenses by the flat-only path and will report a v3 "+
			"credential as no license at all — use license.VerifyEnvelope:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// TestTheEnterpriseDownloadGateAcceptsACredential closes the last consumer: `upgrade
// --enterprise` refuses locally unless the installed license is live. With the old flat-only
// path a paying customer holding a v3 credential was told their license "does not verify against
// this build's key" — which is both wrong and unactionable.
func TestTheEnterpriseDownloadGateAcceptsACredential(t *testing.T) {
	if len(license.DefaultPublicKey()) == 0 {
		t.Skip("this build embeds no verification key")
	}
	dir := t.TempDir()
	// A term that does not rot. The frozen vector's base ends 2026-08-31 and this gate reads the
	// wall clock, so a test built on it would, from 2026-09-01, accept "EXPIRED" as a pass — and
	// an implementation that classified EVERY decoded credential as expired would pass the
	// acceptance test it is named after. (2026-08-11 Codex contrast, F-6.)
	if err := os.WriteFile(filepath.Join(dir, "license.key"), []byte(signNonExpiringCredentialV3(t)+"\n"), 0o600); err != nil {
		t.Fatalf("write license: %v", err)
	}

	lic, err := requireValidLicense("", dir)
	if err != nil {
		t.Fatalf("the enterprise download gate refused a live v3 credential: %v", err)
	}
	if !lic.IsCredentialV3() || lic.Licensee() != "ACME S.L." {
		t.Fatalf("the gate returned %+v, want the v3 credential", lic)
	}

	// Non-firing control: a credential whose base has lapsed is still refused, so the acceptance
	// above is not a gate that says yes to anything shaped like a credential.
	lapsed := t.TempDir()
	if err := os.WriteFile(filepath.Join(lapsed, "license.key"), []byte(signTestCredentialV3(t)+"\n"), 0o600); err != nil {
		t.Fatalf("write license: %v", err)
	}
	if _, err := requireValidLicense("", lapsed); err == nil {
		if time.Now().UTC().After(time.Date(2026, 8, 31, 20, 44, 27, 0, time.UTC)) {
			t.Fatal("the frozen vector's base has lapsed and the gate still accepted it")
		}
	}
}

// signNonExpiringCredentialV3 signs a minimal, well-formed credential whose base term is far
// enough out that no test built on it turns red for the passage of time. The frozen vector stays
// where byte-level conformance is the point; this is for the paths that read the wall clock.
func signNonExpiringCredentialV3(t *testing.T) string {
	t.Helper()
	priv := license.DevPrivateKey()
	if len(priv) != ed25519.PrivateKeySize {
		t.Skip("this build embeds no dev key")
	}
	payload := []byte(`{"schema":"olivares.commercial.credential.v3","serial":"cred_nonexpiring_0001",` +
		`"issue_seq":1,"key_id":"issuer-2026-08","key_epoch":1,` +
		`"issued_at":"2026-08-07T10:00:00Z","not_before":"2026-08-07T10:00:00Z",` +
		`"entity_id":"ent_acme_sl","deployment_id":"dep_prod_01","purpose":"production",` +
		`"licensee":{"display_name":"ACME S.L."},"support_profile":"business","grants":[` +
		`{"grant_id":"gr_base","order_line_id":"ol_base","product_id":"pdt_business","kind":"base",` +
		`"cadence":"year","paid_through":"2099-01-01T00:00:00Z","expires_at":"2099-01-01T00:00:00Z",` +
		`"issuance_phase":"term","guarantee_deadline":null,"promotion_hold_deadline":null,"lease_until":null}]}`)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(ed25519.Sign(priv, payload))
}

// TestTheAddOnLicenceSeamSeesALiveCredential is the in-tree half of the seam the enterprise
// overlay publishes to every add-on gate. A false answer there is StateUnentitled, so a paying
// customer holding a v3 credential loses every add-on while this same holder's display path
// reports the license valid — two answers to one question, from one struct.
func TestTheAddOnLicenceSeamSeesALiveCredential(t *testing.T) {
	pub := license.DefaultPublicKey()
	if len(pub) == 0 {
		t.Skip("this build embeds no verification key")
	}
	h := newLicenseHolder(pub, licenseSource{Blob: signNonExpiringCredentialV3(t), Kind: licenseSourceDataDir},
		func() time.Time { return insideTheBaseTerm }, discardLogger())

	c, ok := h.claims()
	if !ok {
		t.Fatal("the add-on license seam reports NO license for a live v3 credential; every add-on gate refuses")
	}
	if c.Licensee != "ACME S.L." || c.Serial != "cred_nonexpiring_0001" {
		t.Fatalf("seam claims = %+v, want the holder and the serial the CRL names", c)
	}
	// The seam must agree with the display path rather than contradict it.
	if got, want := c.Status(insideTheBaseTerm), license.Status(h.display().status); got != want {
		t.Fatalf("the seam says %q where the display says %q", got, want)
	}
	// Non-firing: a credential whose base has lapsed is not projected as live.
	lapsed := newLicenseHolder(pub, licenseSource{Blob: signTestCredentialV3(t), Kind: licenseSourceDataDir},
		func() time.Time { return pastEverything }, discardLogger())
	if _, ok := lapsed.claims(); ok {
		t.Fatal("a lapsed credential was projected as a live license")
	}
}

// TestTheHolderExposesTheSignedGrantList is the sibling of claims(): addongate
// cannot honor per-line entitlement without this list.
func TestTheHolderExposesTheSignedGrantList(t *testing.T) {
	pub := license.DefaultPublicKey()
	if len(pub) == 0 {
		t.Skip("this build embeds no verification key")
	}
	h := newLicenseHolder(pub, licenseSource{Blob: signTestCredentialV3(t), Kind: licenseSourceDataDir},
		func() time.Time { return insideTheBaseTerm }, discardLogger())

	got, ok := h.grants()
	if !ok {
		t.Fatal("grants() reported no license for a live v3 credential")
	}
	if len(got) != 2 {
		t.Fatalf("grants() returned %d lines; the signed purchase has 2", len(got))
	}
	if got[0].Kind != license.GrantKindBase {
		t.Fatalf("first line kind = %q, want base", got[0].Kind)
	}
	if got[1].Kind != license.GrantKindAddon {
		t.Fatalf("second line kind = %q, want addon", got[1].Kind)
	}

	// No-fire for the paid add-on: the vector's addon lease ended 2026-08-10
	// and insideTheBaseTerm is 2026-08-20, so Active is false. Filtering it
	// out here would hide a purchased line from addongate.
	if got[1].Active(insideTheBaseTerm) {
		t.Fatal("fixture changed: the add-on line is still Active at insideTheBaseTerm")
	}
	if got[0].Active(insideTheBaseTerm) == false {
		t.Fatal("fixture changed: the base line is not Active at insideTheBaseTerm")
	}

	orig := got[0].ProductID
	got[0].ProductID = "mutated"
	again, _ := h.grants()
	if again[0].ProductID != orig {
		t.Fatal("grants() handed out the live slice; a caller could rewrite the credential")
	}

	// Mutant: skip the expiry check — a lapsed container would still look live.
	lapsed := newLicenseHolder(pub, licenseSource{Blob: signTestCredentialV3(t), Kind: licenseSourceDataDir},
		func() time.Time { return pastEverything }, discardLogger())
	if _, ok := lapsed.grants(); ok {
		t.Fatal("grants() reported a live list for a lapsed credential")
	}

	// Mutant: invent a base line for a flat blob — overlay would treat v1 as v3.
	flat := newLicenseHolder(pub, licenseSource{Blob: signTestLicense(t, license.Claims{
		Licensee: "Acme", IssuedAt: insideTheBaseTerm, ExpiresAt: insideTheBaseTerm.Add(24 * time.Hour),
	}), Kind: licenseSourceDataDir}, func() time.Time { return insideTheBaseTerm }, discardLogger())
	flatGrants, flatOK := flat.grants()
	if !flatOK {
		t.Fatal("grants() reported no license for a live flat blob")
	}
	if flatGrants != nil {
		t.Fatalf("flat grants() = %#v, want nil: a v1/v2 blob has no grant list", flatGrants)
	}

	// Mutant: skip the verify — garbage would look like "no grants, live".
	bad := newLicenseHolder(pub, licenseSource{Blob: "not-a-blob", Kind: licenseSourceDataDir},
		func() time.Time { return insideTheBaseTerm }, discardLogger())
	if _, ok := bad.grants(); ok {
		t.Fatal("grants() accepted an unverifiable blob")
	}
}

func TestBootWiresTheGrantListSeam(t *testing.T) {
	src, err := os.ReadFile("boot.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "bindEnterpriseEntitlement(licHolder.grants)") {
		t.Fatal("boot.go does not bind licHolder.grants; addongate would see no EntitlementFunc")
	}
}
