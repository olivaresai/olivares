// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// The routing half of the v3 contract: the envelope is shared with v1/v2, so something has to
// decide WHICH container a verified payload holds. Before this file the answer was "nothing
// does", and the consequence was not theoretical:
//
//	core/license/license.go:127                 wire.Licensee is a STRING
//	commercial/.../credential-v3.ts:211         the issuer emits licensee as an OBJECT
//	ParseCredentialV3                           zero production callers
//
// so Verify's json.Unmarshal into `wire` failed on the licensee object and every consumer got
// ErrMalformed before a single grant was evaluated. These tests pin the routing, the ORDER of
// signature-then-parse, and the non-firing direction in both containers.
package license_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
)

const v3VectorPath = "testdata/credential_v3_ts_vector.json"

// envelope builds the shared blob envelope over arbitrary payload bytes. The v3 issuer is
// TypeScript, so there is no Go signer for a credential and the test has to be the envelope —
// which is also the point: the envelope is the ONLY thing the two containers share.
func envelope(t *testing.T, payload []byte, priv ed25519.PrivateKey) string {
	t.Helper()
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(ed25519.Sign(priv, payload))
}

func v3Vector(t *testing.T) []byte {
	t.Helper()
	payload, err := os.ReadFile(filepath.Clean(v3VectorPath))
	if err != nil {
		t.Fatalf("the cross-language vector is missing (%v).\n"+
			"Regenerate it from the issuer:\n"+
			"  cd commercial/license-worker && OLIVARES_WRITE_V3_VECTOR=1 node --test test/credential-v3.test.ts", err)
	}
	return payload
}

// TestVerifyEnvelopeReadsTheSignedTypeScriptVector is the acceptance test of this change: the
// bytes the ISSUER produces, signed, read end to end by the verifier the engine calls.
//
// The conformance vector already proved ParseCredentialV3 accepts those bytes. It could not
// prove anything about Verify, because nothing connected the two — which is exactly how a
// credential nobody can read passed a green suite.
func TestVerifyEnvelopeReadsTheSignedTypeScriptVector(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	payload := v3Vector(t)

	v, err := license.VerifyEnvelope(envelope(t, payload, priv), pub)
	if err != nil {
		t.Fatalf("the engine REFUSES the credential the issuer signs: %v", err)
	}
	if v.Container != license.ContainerCredentialV3 {
		t.Fatalf("container = %q, want %q", v.Container, license.ContainerCredentialV3)
	}
	if got, want := v.Credential.Licensee, "DODO-10 Sub A"; got != want {
		t.Fatalf("licensee = %q, want %q — the envelope's licensee is an OBJECT and this is its display_name", got, want)
	}
	if len(v.Credential.Grants) != 2 {
		t.Fatalf("grants = %d, want 2 (the real corpus pair: a base and its add-on)", len(v.Credential.Grants))
	}
	if got := v.Licensee(); got != "DODO-10 Sub A" {
		t.Fatalf("Verified.Licensee() = %q — the accessor every call site uses must not diverge from the container", got)
	}

	// The three labels a v3 does not carry stay EMPTY instead of being filled from the
	// nearest-looking field. support_tier is the one that matters: the credential's envelope has
	// `support_profile`, whose value here is "business" — a PRODUCT tier, where SupportTier's
	// domain is the support relationship ("standard", "enterprise"). Mapping it by name would put
	// a fact nobody signed into a slot that means something else, in a console badge. It is
	// reachable under its own name and nowhere else.
	if got := v.SupportTier(); got != "" {
		t.Fatalf("SupportTier() = %q for a v3, want empty: support_profile is a different field, not a rename", got)
	}
	if got := v.SupportProfile(); got != "business" {
		t.Fatalf("SupportProfile() = %q, want business", got)
	}
	if got := v.Plan(); got != "" {
		t.Fatalf("Plan() = %q for a v3, want empty: the grant list replaced the one-line plan label", got)
	}
	if got := v.Profile(); got != "" {
		t.Fatalf("Profile() = %q for a v3, want empty: purpose (production|staging) is not the issuance profile (online|airgapped|trial)", got)
	}
}

// TestVerifyEnvelopeKeepsTheFlatContainerReadable is the compatibility half: flat licenses are
// issued and installed today, and this change is additive. Verify's own battery covers the flat
// path; this asserts the NEW entry point agrees with it byte for byte.
func TestVerifyEnvelopeKeepsTheFlatContainerReadable(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	want := license.Claims{
		Licensee:  "ACME S.L.",
		Plan:      "commercial",
		Serial:    "lic_flat_0001",
		HolderID:  "sub_123",
		IssuedAt:  time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2027, 8, 7, 10, 0, 0, 0, time.UTC),
	}
	blob, err := license.Sign(want, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	v, err := license.VerifyEnvelope(blob, pub)
	if err != nil {
		t.Fatalf("the flat container stopped verifying: %v", err)
	}
	if v.Container != license.ContainerFlat {
		t.Fatalf("container = %q, want %q", v.Container, license.ContainerFlat)
	}
	if v.Claims.Licensee != want.Licensee || !v.Claims.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("claims = %+v, want licensee %q expiring %s", v.Claims, want.Licensee, want.ExpiresAt)
	}
	// And the old entry point still answers exactly as it did.
	c, err := license.Verify(blob, pub)
	if err != nil {
		t.Fatalf("Verify no longer reads a flat license: %v", err)
	}
	if c.Licensee != want.Licensee || c.Serial != want.Serial {
		t.Fatalf("Verify claims = %+v, want %+v", c, want)
	}
}

// TestSignatureIsCheckedBeforeTheContainerIsChosen is the SECURITY property of this change, and
// it is about ORDER, not about acceptance: the schema field is attacker-supplied until the
// signature says otherwise, so a blob that fails the signature must never reach a parser chosen
// by its own contents.
//
// The assertion discriminates because the payload is BOTH badly signed AND a structurally broken
// v3 (no grants). If the container were chosen first, the error would be the v3 parser's
// complaint about grants; only a signature-first order yields ErrBadSignature.
func TestSignatureIsCheckedBeforeTheContainerIsChosen(t *testing.T) {
	pub, _, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	_, attacker, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	// Names itself v3 and would be refused BY THE PARSER for carrying no grants.
	hostile := []byte(`{"schema":"olivares.commercial.credential.v3","serial":"x","issue_seq":1,` +
		`"key_id":"k","key_epoch":1,"issued_at":"2026-08-07T10:00:00Z","not_before":"2026-08-07T10:00:00Z",` +
		`"entity_id":"e","deployment_id":"d","purpose":"production","licensee":{"display_name":"X"},"grants":[]}`)

	_, err = license.VerifyEnvelope(envelope(t, hostile, attacker), pub)
	if !errors.Is(err, license.ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature — a payload the key does not attest reached the v3 parser, "+
			"which turns an attacker-controlled field into a parser selector", err)
	}
	if strings.Contains(fmt.Sprint(err), "grants") {
		t.Fatalf("err = %v — the parser ran before the signature was checked", err)
	}

	// ⛔ THE CELL ABOVE IS NOT ENOUGH, and a compiled mutant proved it (2026-08-11 Codex contrast,
	// F-6): an implementation that reads the discriminator FIRST and only then verifies still
	// returns ErrBadSignature and never mentions grants, so it passes everything above.
	//
	// These two do discriminate, because they make the DISCRIMINATOR's own verdict observable. A
	// badly-signed payload whose schema containerOf would reject must still come back
	// ErrBadSignature: any implementation that consulted the discriminator first would surface
	// ITS error instead, since there is nothing else it could return.
	for _, tc := range []struct {
		name    string
		payload string
	}{{
		name:    "an unknown schema, badly signed",
		payload: `{"schema":"olivares.commercial.credential.v4","licensee":"ACME","issued_at":"2026-08-07T10:00:00Z"}`,
	}, {
		name:    "an ambiguous duplicate schema, badly signed",
		payload: `{"schema":"","schema":"olivares.commercial.credential.v3","licensee":"ACME"}`,
	}, {
		name:    "a non-string schema, badly signed",
		payload: `{"schema":3,"licensee":"ACME","issued_at":"2026-08-07T10:00:00Z"}`,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := license.VerifyEnvelope(envelope(t, []byte(tc.payload), attacker), pub)
			if !errors.Is(err, license.ErrBadSignature) {
				t.Fatalf("err = %v, want ErrBadSignature — the container verdict surfaced, so the "+
					"discriminator was consulted on bytes the key had not attested", err)
			}
			if errors.Is(err, license.ErrUnknownContainer) || errors.Is(err, license.ErrMalformed) {
				t.Fatalf("err = %v — that is the discriminator's verdict, reached before the signature", err)
			}
		})
	}
}

// TestVerifyEnvelopeRefusesATamperedV3Credential is the NON-FIRING direction named in the brief:
// a VerifyEnvelope that accepted anything would pass every "it reads v3" test above.
func TestVerifyEnvelopeRefusesATamperedV3Credential(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	payload := v3Vector(t)
	blob := envelope(t, payload, priv)

	// One byte of the SIGNED payload, changed after signing: same envelope shape, same schema,
	// same everything a shape-sniffing router would look at.
	tampered := []byte(strings.Replace(string(payload), "DODO-10 Sub A", "DODO-10 Sub B", 1))
	if string(tampered) == string(payload) {
		t.Fatal("the mutation did not apply; the control proves nothing")
	}
	enc := base64.RawURLEncoding
	_, sigB64, _ := strings.Cut(blob, ".")
	if _, err := license.VerifyEnvelope(enc.EncodeToString(tampered)+"."+sigB64, pub); !errors.Is(err, license.ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature for a v3 credential whose licensee was rewritten after signing", err)
	}

	// And the same blob under a DIFFERENT key is refused too.
	other, _, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if _, err := license.VerifyEnvelope(blob, other); !errors.Is(err, license.ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature under a foreign key", err)
	}
}

// TestAnUnknownContainerIsNeverReadAsFlat is the deny-closed direction of the router.
//
// It matters because encoding/json IGNORES unknown fields: a future v4 payload, correctly signed
// with our key, would decode into the flat `wire` struct as a license with an empty licensee and
// no expiry. "Fall back to the old parser" is how a container with different semantics gets read
// under rules that were never written for it.
func TestAnUnknownContainerIsNeverReadAsFlat(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	future := []byte(`{"schema":"olivares.commercial.credential.v4","licensee":"ACME S.L.",` +
		`"issued_at":"2026-08-07T10:00:00Z","expires_at":"2027-08-07T10:00:00Z"}`)

	v, err := license.VerifyEnvelope(envelope(t, future, priv), pub)
	if err == nil {
		t.Fatalf("a credential naming schema v4 was read as container %q with claims %+v", v.Container, v.Claims)
	}
	if !errors.Is(err, license.ErrUnknownContainer) {
		t.Fatalf("err = %v, want ErrUnknownContainer", err)
	}
	if _, err := license.Verify(envelope(t, future, priv), pub); err == nil {
		t.Fatal("Verify read a v4 payload as a flat license")
	}
}

// TestVerifyNamesTheV3ContainerInsteadOfCallingItMalformed keeps the old entry point honest.
//
// Verify returns FLAT claims by contract, and a v3 credential has none — flattening N grants into
// one Claims is precisely the aggregation v3 exists to prevent. So it refuses, but it refuses with
// a name a caller can route on instead of "malformed", which is what it says today and what makes
// a correctly-signed credential indistinguishable from a corrupt blob.
func TestVerifyNamesTheV3ContainerInsteadOfCallingItMalformed(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	blob := envelope(t, v3Vector(t), priv)

	_, err = license.Verify(blob, pub)
	if !errors.Is(err, license.ErrCredentialV3) {
		t.Fatalf("err = %v, want ErrCredentialV3", err)
	}
	if errors.Is(err, license.ErrMalformed) {
		t.Fatal("a correctly-signed v3 credential is still reported as malformed")
	}
}

// TestAV3SchemaNeverSucceedsThroughTheFlatReader is the cell the 2026-08-11 scope audit calls the
// important one, and it fails in the direction nobody notices.
//
// A payload that NAMES itself v3 but carries `licensee` as a string sails straight through the
// flat reader today: json.Unmarshal into `wire` does not use DisallowUnknownFields, so it eats
// `schema`, `grants` and `entity_id` in silence and yields a Claims with a ZERO ExpiresAt — which
// Status reports as EXPIRED. That is a paid license reported lapsed, with no error anywhere. An
// inverted dispatch produces exactly this, and every "it reads v3" test above would still pass.
func TestAV3SchemaNeverSucceedsThroughTheFlatReader(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	// Names v3, then lies about the shape: a string licensee and no grants.
	mixed := []byte(`{"schema":"olivares.commercial.credential.v3","licensee":"ACME S.L.",` +
		`"issued_at":"2026-08-07T10:00:00Z"}`)

	v, err := license.VerifyEnvelope(envelope(t, mixed, priv), pub)
	if err == nil {
		t.Fatalf("a v3-schema payload was read as container %q with claims %+v — the flat reader "+
			"accepted it and its zero expiry would be reported as EXPIRED", v.Container, v.Claims)
	}
	if v.Claims.Licensee != "" {
		t.Fatalf("flat claims leaked out of a failed read: %+v", v.Claims)
	}
	if _, err := license.Verify(envelope(t, mixed, priv), pub); err == nil {
		t.Fatal("Verify read a v3-schema payload as a flat license")
	}
}

// TestAFlatLicenceNamingTheSchemaInItsLicenseeIsStillFlat closes the discriminator hazard.
//
// The schema string is 33 bytes of CLIENT-CONTROLLED text: `licensee` comes straight from the
// purchase (commercial/license-worker/src/license/claims.ts:22), so a customer can put it in
// their own organization name. A router that decided by substring — which the v3 parser did
// until this change — would call that license a BROKEN v3 and stop reading it.
func TestAFlatLicenceNamingTheSchemaInItsLicenseeIsStillFlat(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	hostileName := "olivares.commercial.credential.v3"
	blob, err := license.Sign(license.Claims{
		Licensee:  hostileName,
		IssuedAt:  time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2027, 8, 7, 10, 0, 0, 0, time.UTC),
	}, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	v, err := license.VerifyEnvelope(blob, pub)
	if err != nil {
		t.Fatalf("a flat license whose LICENSEE contains the schema string was refused: %v", err)
	}
	if v.Container != license.ContainerFlat || v.Licensee() != hostileName {
		t.Fatalf("container = %q licensee = %q, want flat and the name intact", v.Container, v.Licensee())
	}
	if got := v.Status(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)); got != license.StatusValid {
		t.Fatalf("status = %q, want valid", got)
	}
	// And asked directly, the v3 parser says "not v3" rather than "broken v3" — the distinction
	// a caller's fallback depends on.
	payloadB64, _, _ := strings.Cut(blob, ".")
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := license.ParseCredentialV3(payload); !errors.Is(err, license.ErrNotV3) {
		t.Fatalf("ParseCredentialV3 = %v, want ErrNotV3 for a flat license that merely mentions the schema", err)
	}
}

// TestTheDiscriminatorIsNotFooledByNestingOrDuplication attacks the ONE field that chooses the
// reader. Each case is a way to make the router and the reader disagree about what a payload is,
// and a disagreement there is silent: the flat reader drops what it does not recognize instead of
// complaining, so the license comes back with a zero expiry and is reported EXPIRED.
func TestTheDiscriminatorIsNotFooledByNestingOrDuplication(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	base := grant("gr_base", "pdt_business", "base", "term", "2027-01-31T00:00:00Z", "2027-01-31T00:00:00Z", "")

	cases := []struct {
		name    string
		payload string
		// wantFlat: read as the flat container. Otherwise it must ERROR — never be misrouted.
		wantFlat bool
	}{{
		name: "a schema nested inside licensee does not vote",
		// Only the top level decides, so this is a flat license with an odd extra field.
		payload:  `{"licensee":"ACME","schema_nested":{"schema":"olivares.commercial.credential.v3"},"issued_at":"2026-08-07T10:00:00Z"}`,
		wantFlat: true,
	}, {
		name: "a schema inside a grant does not vote",
		payload: `{"licensee":"ACME","issued_at":"2026-08-07T10:00:00Z","grants":[` +
			`{"schema":"olivares.commercial.credential.v3"}]}`,
		wantFlat: true,
	}, {
		name: "an explicitly EMPTY schema is not an absent one",
		// ABSENCE identifies the flat container, and only absence. This payload names a
		// discriminator and does not name the one this build reads, so it must be refused —
		// routing it to flat let the decoder drop the field in silence.
		payload: `{"schema":"","licensee":"ACME","issued_at":"2026-08-07T10:00:00Z","expires_at":"2027-08-07T10:00:00Z"}`,
	}, {
		name: "two schema keys are ambiguous and refused",
		// encoding/json keeps the LAST; a scan that stopped at the first would route by one value
		// and let the reader use the other.
		payload: `{"schema":"","schema":"olivares.commercial.credential.v3","licensee":"ACME",` +
			`"issued_at":"2026-08-07T10:00:00Z"}`,
	}, {
		name:    "a numeric schema is not a container name",
		payload: `{"schema":3,"licensee":"ACME","issued_at":"2026-08-07T10:00:00Z"}`,
	}, {
		name:    "a null schema is not a container name",
		payload: `{"schema":null,"licensee":"ACME","issued_at":"2026-08-07T10:00:00Z"}`,
	}, {
		name:    "an array schema is not a container name",
		payload: `{"schema":["olivares.commercial.credential.v3"],"licensee":"ACME","issued_at":"2026-08-07T10:00:00Z"}`,
	}, {
		name:    "a top-level array is neither container",
		payload: `["olivares.commercial.credential.v3"]`,
	}, {
		name:    "an empty payload is neither container",
		payload: ``,
	}, {
		name: "the schema after the grants still routes to v3",
		// Field ORDER must not decide: the issuer writes schema first, but nothing in the format
		// requires it and a reader that only looked at the head would misroute this.
		payload: `{"serial":"cred_test_0001","issue_seq":1,"key_id":"issuer-2026-08","key_epoch":1,` +
			`"issued_at":"2026-08-07T10:00:00Z","not_before":"2026-08-07T10:00:00Z","entity_id":"e",` +
			`"deployment_id":"d","purpose":"production","licensee":{"display_name":"ACME"},` +
			`"grants":[` + base + `],"schema":"olivares.commercial.credential.v3"}`,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := license.VerifyEnvelope(envelope(t, []byte(tc.payload), priv), pub)
			if tc.wantFlat {
				if err != nil {
					t.Fatalf("a flat license was refused: %v", err)
				}
				if v.Container != license.ContainerFlat || v.Licensee() != "ACME" {
					t.Fatalf("container = %q licensee = %q, want flat/ACME", v.Container, v.Licensee())
				}
				return
			}
			// The trailing-schema case is a genuine v3 and must ROUTE there, not error.
			if strings.HasSuffix(tc.payload, `"schema":"olivares.commercial.credential.v3"}`) {
				if err != nil {
					t.Fatalf("a v3 credential whose schema rides last was refused: %v", err)
				}
				if v.Container != license.ContainerCredentialV3 {
					t.Fatalf("container = %q, want the v3 container", v.Container)
				}
				return
			}
			if err == nil {
				t.Fatalf("payload was accepted as container %q with claims %+v; it must be refused",
					v.Container, v.Claims)
			}
			if v.Container == license.ContainerFlat && v.Licensee() != "" {
				t.Fatalf("a refused payload still produced flat claims: %+v", v.Claims)
			}
		})
	}
}

// TestErrNotV3IsOnlyForAPayloadThatNamesNoSchema guards the meaning of the ONE error that is
// documented permission to fall back to the v1/v2 reader.
//
// Handing that permission to a payload naming an UNKNOWN container is how a future credential gets
// read under rules written for another one — and the fall-back reader drops what it does not
// recognize, so the misread is silent: an empty licensee, no expiry, reported expired.
func TestErrNotV3IsOnlyForAPayloadThatNamesNoSchema(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantIs  error
	}{{
		name:    "no schema at all is the only fallback case",
		payload: `{"licensee":"ACME","issued_at":"2026-08-07T10:00:00Z"}`,
		wantIs:  license.ErrNotV3,
	}, {
		name:    "a future container is NOT a fallback case",
		payload: `{"schema":"olivares.commercial.credential.v4","licensee":"ACME","issued_at":"2026-08-07T10:00:00Z"}`,
		wantIs:  license.ErrUnknownContainer,
	}, {
		name:    "an explicitly empty schema is NOT a fallback case",
		payload: `{"schema":"","licensee":"ACME","issued_at":"2026-08-07T10:00:00Z"}`,
		wantIs:  license.ErrUnknownContainer,
	}, {
		name:    "a non-string schema is NOT a fallback case",
		payload: `{"schema":3,"licensee":"ACME","issued_at":"2026-08-07T10:00:00Z"}`,
		wantIs:  license.ErrMalformed,
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := license.ParseCredentialV3([]byte(tc.payload))
			if !errors.Is(err, tc.wantIs) {
				t.Fatalf("err = %v, want errors.Is(..., %v)", err, tc.wantIs)
			}
			if tc.wantIs != license.ErrNotV3 && errors.Is(err, license.ErrNotV3) {
				t.Fatalf("err = %v also reports ErrNotV3, which is permission to read these bytes "+
					"with the v1/v2 parser", err)
			}
		})
	}
}

// TestARenewalGraceCannotEndBeforeThePaidTerm closes an inversion that only the UPPER bound was
// guarding: a negative duration is not greater than the maximum, so it passed.
//
// The damage is the opposite of what "grace" means. EffectiveBoundary returns grace_ends_at in
// this phase, so an inverted window SHORTENS the right: the credential reports expired while the
// attested paid term is still running.
func TestARenewalGraceCannotEndBeforeThePaidTerm(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	inverted := credentialWithGrants(t,
		`{"grant_id":"g","order_line_id":"ol","product_id":"pdt_business","kind":"base",`+
			`"cadence":"month","paid_through":"2026-08-31T00:00:00Z","expires_at":"2026-08-31T00:00:00Z",`+
			`"issuance_phase":"renewal_grace","guarantee_deadline":null,"promotion_hold_deadline":null,`+
			`"lease_until":null,"grace_reason":"renewal_failure","grace_ends_at":"2026-08-30T00:00:00Z"}`)
	if _, err := license.VerifyEnvelope(envelope(t, inverted, priv), pub); err == nil {
		t.Fatal("a renewal grace ending BEFORE its own paid_through was accepted; it shortens the right it claims to extend")
	}

	// Non-firing control: the same grant with a grace that genuinely extends the term is accepted,
	// so the refusal above is not a rule that rejects every grace.
	valid := credentialWithGrants(t,
		`{"grant_id":"g","order_line_id":"ol","product_id":"pdt_business","kind":"base",`+
			`"cadence":"month","paid_through":"2026-08-31T00:00:00Z","expires_at":"2026-09-02T00:00:00Z",`+
			`"issuance_phase":"renewal_grace","guarantee_deadline":null,"promotion_hold_deadline":null,`+
			`"lease_until":null,"grace_reason":"renewal_failure","grace_ends_at":"2026-09-02T00:00:00Z"}`)
	v, err := license.VerifyEnvelope(envelope(t, valid, priv), pub)
	if err != nil {
		t.Fatalf("a well-formed renewal grace was refused: %v", err)
	}
	if got := v.Status(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)); got != license.StatusGrace {
		t.Fatalf("status inside the attested grace = %q, want grace", got)
	}
	if got, want := v.Term(), time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("term = %s, want the paid term %s (the grace end is RightEnds)", got, want)
	}
	if got, want := v.RightEnds(), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("right ends = %s, want the attested grace end %s", got, want)
	}
}

// TestTheLegacySeamProjectsACredentialWithoutSayingWhatWasBought pins the ONE projection onto the
// flat claim set, and pins both halves of it: what it must carry, and what it must never carry.
//
// It exists because answering "no license" here is not free. The enterprise overlay publishes this
// provider as the process-wide add-on license source, where a false answer is StateUnentitled and
// every add-on operation is refused — a paying customer losing everything they bought, quietly.
func TestTheLegacySeamProjectsACredentialWithoutSayingWhatWasBought(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	v, err := license.VerifyEnvelope(envelope(t, v3Vector(t), priv), pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	c, ok := v.LegacySeamClaims()
	if !ok {
		t.Fatal("a valid v3 credential projected to 'no licence'; every add-on gate reads this as unentitled")
	}
	if c.Licensee != "DODO-10 Sub A" || c.Serial != "cred_01JZQAP6C8M4R2T7V9W1X3Y5ZA" {
		t.Fatalf("projection = %+v, want the holder and the serial (a CRL names the serial)", c)
	}
	// The term is the BASE line's, so the seam's own arithmetic lands where Credential.Status does.
	base, _ := v.Credential.BaseGrant()
	if !c.ExpiresAt.Equal(base.EffectiveBoundary()) {
		t.Fatalf("projected expiry = %s, want the base boundary %s", c.ExpiresAt, base.EffectiveBoundary())
	}
	insideTerm := base.EffectiveBoundary().Add(-time.Hour)
	if got, want := c.Status(insideTerm), v.Status(insideTerm); got != want {
		t.Fatalf("the seam says %q where the credential says %q; two answers to one question", got, want)
	}
	past := base.EffectiveBoundary().Add(time.Hour)
	if got := c.Status(past); got != license.StatusExpired {
		t.Fatalf("projected status past the base = %q, want expired", got)
	}

	// ⛔ And what it must NEVER carry: anything that says WHAT WAS BOUGHT. The credential has two
	// lines with different products and different phases; a projection that named one of them
	// would be the flattening this whole change refuses.
	if c.Plan != "" || len(c.Features) != 0 || c.SupportTier != "" || c.Profile != "" {
		t.Fatalf("the projection leaked purchase detail: %+v", c)
	}
	if c.MaxUsers != 0 || c.MaxTenants != 0 {
		t.Fatalf("the projection invented a figure nobody signed: %+v", c)
	}

	// A flat license projects to itself, unchanged.
	flat := license.Claims{Licensee: "ACME", Plan: "commercial", IssuedAt: time.Now().UTC()}
	blob, err := license.Sign(flat, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	fv, err := license.VerifyEnvelope(blob, pub)
	if err != nil {
		t.Fatalf("verify flat: %v", err)
	}
	if fc, ok := fv.LegacySeamClaims(); !ok || fc.Plan != "commercial" || fc.Licensee != "ACME" {
		t.Fatalf("flat projection = (%+v, %v), want the claims unchanged", fc, ok)
	}
	// And nothing projects out of nothing.
	if _, ok := (license.Verified{}).LegacySeamClaims(); ok {
		t.Fatal("the zero value projected a license")
	}
}

// TestTheGrantsAreNotFlattened is the mutant named in the brief: a base plus two add-ons that
// arrives as one product, one term.
func TestTheGrantsAreNotFlattened(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	payload := credentialWithGrants(t,
		grant("gr_base", "pdt_business", "base", "term", "2027-01-31T00:00:00Z", "2027-01-31T00:00:00Z", ""),
		grant("gr_a1", "adn_identity", "addon", "term", "2027-01-31T00:00:00Z", "2027-01-31T00:00:00Z", ""),
		grant("gr_a2", "adn_regops", "addon", "refund_window", "2027-01-31T00:00:00Z", "2026-08-10T10:00:00Z", "2026-08-10T10:00:00Z"),
	)

	v, err := license.VerifyEnvelope(envelope(t, payload, priv), pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(v.Credential.Grants) != 3 {
		t.Fatalf("grants = %d, want 3 — a purchase of a base and two add-ons kept all three lines", len(v.Credential.Grants))
	}
	seen := map[string]string{}
	for _, g := range v.Credential.Grants {
		seen[g.ProductID] = string(g.Phase)
	}
	for _, want := range []string{"pdt_business", "adn_identity", "adn_regops"} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("product %q was dropped; the lines present are %v", want, seen)
		}
	}
	// The phases genuinely differ, which is the fact a flat container cannot carry.
	if seen["adn_regops"] == seen["pdt_business"] {
		t.Fatalf("the add-on took its base's phase (%q): the lines were merged", seen["adn_regops"])
	}
	// And no flat claim set was manufactured on the side.
	if v.Claims.Licensee != "" || !v.Claims.ExpiresAt.IsZero() || v.Claims.Plan != "" || len(v.Claims.Features) != 0 {
		t.Fatalf("a v3 credential produced flat claims %+v — that is the aggregation v3 exists to prevent", v.Claims)
	}
}

// TestCredentialStatusFollowsTheBaseGrant pins the ONE credential-wide answer the display
// surfaces need, and pins it to the canon's own dependency rather than to an invented merge:
// every add-on requires an effective base grant (PRICING-CANON.md:925), so a credential whose
// base has lapsed confers nothing regardless of what its add-on lines still say.
func TestCredentialStatusFollowsTheBaseGrant(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	// Base runs to 2027-01-31 in term; the add-on's lease ends 2026-08-10.
	payload := credentialWithGrants(t,
		grant("gr_base", "pdt_business", "base", "term", "2027-01-31T00:00:00Z", "2027-01-31T00:00:00Z", ""),
		grant("gr_a1", "adn_regops", "addon", "refund_window", "2027-01-31T00:00:00Z", "2026-08-10T10:00:00Z", "2026-08-10T10:00:00Z"),
	)
	v, err := license.VerifyEnvelope(envelope(t, payload, priv), pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	insideBoth := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	baseOnly := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	pastBase := time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)

	if got := v.Status(insideBoth); got != license.StatusValid {
		t.Fatalf("status inside both lines = %q, want valid", got)
	}
	if got := v.Status(baseOnly); got != license.StatusValid {
		t.Fatalf("status with a live base and a lapsed add-on = %q, want valid — an add-on's expiry is not the base's", got)
	}
	if got := v.Status(pastBase); got != license.StatusExpired {
		t.Fatalf("status past the base = %q, want expired", got)
	}
	// The per-line truth is still available and still differs at baseOnly.
	if active := v.Credential.ActiveGrants(baseOnly); len(active) != 1 || active[0].Kind != license.GrantKindBase {
		t.Fatalf("active grants at %s = %v, want exactly the base", baseOnly, active)
	}
	// Before not_before nothing is conferred: the closed status set has no "not yet valid", and a
	// verifier reports the state that grants nothing rather than inventing one that does.
	if got := v.Status(time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)); got != license.StatusExpired {
		t.Fatalf("status before not_before = %q, want expired (nothing is conferred yet)", got)
	}
}

// TestAnAddOnOutlivingItsBaseDoesNotKeepTheCredentialAlive is what separates "the base grant's
// status" from "any grant is active", and without it a status that answered the second question
// would pass every other test here.
//
// The case is reachable, not hypothetical: an add-on may be paid through the same instant as its
// base and then enter renewal_grace, whose attested end may run up to MaxGracePeriod past
// paid_through. At that point the add-on line is active and the base is not — and the canon says
// the credential confers nothing, because every add-on requires an effective base grant.
func TestAnAddOnOutlivingItsBaseDoesNotKeepTheCredentialAlive(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	payload := credentialWithGrants(t,
		grant("gr_base", "pdt_business", "base", "term", "2027-01-31T00:00:00Z", "2027-01-31T00:00:00Z", ""),
		// Same paid_through as the base, then a bounded renewal grace of five days past it.
		`{"grant_id":"gr_a1","order_line_id":"ol_gr_a1","product_id":"adn_regops","kind":"addon",`+
			`"cadence":"month","paid_through":"2027-01-31T00:00:00Z","expires_at":"2027-02-05T00:00:00Z",`+
			`"issuance_phase":"renewal_grace","guarantee_deadline":null,"promotion_hold_deadline":null,`+
			`"lease_until":null,"grace_reason":"renewal_failure","grace_ends_at":"2027-02-05T00:00:00Z"}`,
	)
	v, err := license.VerifyEnvelope(envelope(t, payload, priv), pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	pastBase := time.Date(2027, 2, 2, 0, 0, 0, 0, time.UTC)

	// The premise, stated over the LINE's own window so the assertion below cannot pass for the
	// wrong reason: at this instant the add-on is still inside its attested grace.
	var addon license.Grant
	for _, g := range v.Credential.Grants {
		if g.Kind == license.GrantKindAddon {
			addon = g
		}
	}
	if !addon.Active(pastBase) {
		t.Fatalf("premise broken: the add-on's own window already closed at %s (boundary %s)",
			pastBase.Format(time.RFC3339), addon.EffectiveBoundary().Format(time.RFC3339))
	}

	if got := v.Status(pastBase); got != license.StatusExpired {
		t.Fatalf("status = %q with a lapsed base and an add-on still in grace, want expired: "+
			"every add-on requires an effective base grant (PRICING-CANON.md:925)", got)
	}
	// And the EFFECTIVE set agrees with the status instead of contradicting it. This assertion
	// used to demand the opposite — that ActiveGrants hand back the add-on — which locked in a
	// credential reporting itself expired while offering a line to render as live. (2026-08-11
	// Codex contrast, F-3.)
	if active := v.Credential.ActiveGrants(pastBase); len(active) != 0 {
		t.Fatalf("active grants at %s = %v, want none: an add-on whose base has lapsed confers nothing",
			pastBase.Format(time.RFC3339), active)
	}
}

// TestTheTermOfAProvisionalBaseIsItsLeaseAndNotItsPaidThrough separates the two candidates for
// "when does this license end", which are the SAME instant in a promoted grant and far apart in a
// provisional one — so a test written only against `term` cannot tell them apart.
//
// During the money-back window the lease can end in 72h while paid_through sits a month out.
// Reading paid_through would tell an operator their license runs to the end of the month when the
// signed runway expires on Monday, and every renewal reminder built on it would fire too late.
func TestTheTermOfAProvisionalBaseIsItsLeaseAndNotItsPaidThrough(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	// issued 2026-08-07T10:00Z, so the 72h ceiling lands on 2026-08-10T10:00Z.
	payload := credentialWithGrants(t,
		grant("gr_base", "pdt_business", "base", "refund_window",
			"2026-09-30T00:00:00Z", "2026-08-10T10:00:00Z", "2026-08-10T10:00:00Z"),
	)
	v, err := license.VerifyEnvelope(envelope(t, payload, priv), pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	lease := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	paidThrough := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

	if got := v.Term(); !got.Equal(lease) {
		t.Fatalf("term = %s, want the lease %s (paid_through is %s and is not the runway)",
			got.Format(time.RFC3339), lease.Format(time.RFC3339), paidThrough.Format(time.RFC3339))
	}
	if got := v.RightEnds(); !got.Equal(lease) {
		t.Fatalf("right ends = %s, want the lease %s", got.Format(time.RFC3339), lease.Format(time.RFC3339))
	}
	// The premise, so the assertion above cannot pass because the two happen to coincide.
	if lease.Equal(paidThrough) {
		t.Fatal("premise broken: the lease and paid_through are the same instant")
	}
	if got := v.Status(paidThrough.Add(-24 * time.Hour)); got != license.StatusExpired {
		t.Fatalf("status past the lease but inside paid_through = %q, want expired", got)
	}
}

// TestCredentialRevocationMatchesTheSerialAndTheKeyEpoch keeps the CRL path working across the
// container change. Today a v3 credential is never even parsed at the CRL site, so a revoked
// credential is silently unreported.
func TestCredentialRevocationMatchesTheSerialAndTheKeyEpoch(t *testing.T) {
	pub, priv, err := license.GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	payload := credentialWithGrants(t,
		grant("gr_base", "pdt_business", "base", "term", "2027-01-31T00:00:00Z", "2027-01-31T00:00:00Z", ""),
	)
	v, err := license.VerifyEnvelope(envelope(t, payload, priv), pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)

	byName := license.Revocation{Serials: []string{"cred_test_0001"}}
	if got := v.StatusWithRevocation(now, byName); got != license.StatusRevoked {
		t.Fatalf("status under a CRL naming this serial = %q, want revoked", got)
	}
	// Non-firing: a CRL that names somebody else revokes nothing.
	other := license.Revocation{Serials: []string{"cred_someone_else"}}
	if got := v.StatusWithRevocation(now, other); got != license.StatusValid {
		t.Fatalf("status under a CRL naming another serial = %q, want valid", got)
	}
	// The signing-key fence: issued 2026-08-07, fenced from 2026-08-08.
	fence := license.Revocation{LicenseKeyEpoch: time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC).Unix()}
	if got := v.StatusWithRevocation(now, fence); got != license.StatusRevoked {
		t.Fatalf("status under a key-epoch fence = %q, want revoked", got)
	}
}

// ---- payload builders --------------------------------------------------------------------
// The issuer is TypeScript, so a Go test that needs a credential other than the frozen vector
// has to write the bytes. They are written in the issuer's field order (credential-v3.ts:200-223)
// so what these tests exercise is what the wire actually carries.

func grant(id, product, kind, phase, paidThrough, expiresAt, lease string) string {
	leaseJSON := "null"
	if lease != "" {
		leaseJSON = `"` + lease + `"`
	}
	return fmt.Sprintf(`{"grant_id":%q,"order_line_id":%q,"product_id":%q,"kind":%q,"cadence":"month",`+
		`"paid_through":%q,"expires_at":%q,"issuance_phase":%q,`+
		`"guarantee_deadline":"2026-08-21T10:00:00Z","promotion_hold_deadline":"2027-02-28T10:00:00Z","lease_until":%s}`,
		id, "ol_"+id, product, kind, paidThrough, expiresAt, phase, leaseJSON)
}

func credentialWithGrants(t *testing.T, grants ...string) []byte {
	t.Helper()
	return []byte(fmt.Sprintf(`{"schema":"olivares.commercial.credential.v3","serial":"cred_test_0001",`+
		`"issue_seq":1,"key_id":"issuer-2026-08","key_epoch":1,`+
		`"issued_at":"2026-08-07T10:00:00Z","not_before":"2026-08-07T10:00:00Z",`+
		`"entity_id":"ent_acme_sl","deployment_id":"dep_prod_01","purpose":"production",`+
		`"licensee":{"display_name":"ACME S.L."},"support_profile":"business","grants":[%s]}`,
		strings.Join(grants, ",")))
}
