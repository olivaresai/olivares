// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package license

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// keyFromSeed returns a deterministic test keypair. Seeds are owned fixtures, never real keys.
func keyFromSeed(b byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = b
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return priv.Public().(ed25519.PublicKey), priv
}

func mustKID(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	kid, err := KeyID(pub)
	if err != nil {
		t.Fatal(err)
	}
	return kid
}

// v3Payload renders a minimal valid credential with the given key identity and issue instant.
func v3Payload(keyID string, keyEpoch int, issuedAt time.Time) []byte {
	iat := issuedAt.UTC().Format(time.RFC3339)
	return []byte(fmt.Sprintf(`{"schema":"olivares.commercial.credential.v3","serial":"conn_production_dep_a_1",`+
		`"issue_seq":1,"key_id":%q,"key_epoch":%d,"issued_at":%q,"not_before":%q,`+
		`"entity_id":"cus_1","deployment_id":"dep_a","purpose":"production",`+
		`"licensee":{"display_name":"ACME S.L."},"grants":[{"grant_id":"gr_base","order_line_id":"ol_base",`+
		`"product_id":"pdt_business","kind":"base","cadence":"year","paid_through":"2099-01-01T00:00:00Z",`+
		`"expires_at":"2099-01-01T00:00:00Z","issuance_phase":"term","guarantee_deadline":null,`+
		`"promotion_hold_deadline":null,"lease_until":null}]}`, keyID, keyEpoch, iat, iat))
}

func signPayload(priv ed25519.PrivateKey, payload []byte) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(ed25519.Sign(priv, payload))
}

var (
	kNow     = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	kIssued  = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	kRetired = time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
)

func TestKeyIDIsDerivedFromThePublicBytes(t *testing.T) {
	pub, _ := keyFromSeed(1)
	sum := sha256.Sum256(pub)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got := mustKID(t, pub); got != want {
		t.Fatalf("KeyID = %s, want %s", got, want)
	}
	if _, err := KeyID(pub[:31]); !errors.Is(err, ErrKeyringInvalid) {
		t.Fatalf("short key: %v", err)
	}
}

// TestKeyringCurrentVerifyOnlyRevokedUnknown is acceptance §7.9: A verify-only while B signs.
func TestKeyringCurrentVerifyOnlyRevokedUnknown(t *testing.T) {
	pubA, privA := keyFromSeed(0xA)
	pubB, privB := keyFromSeed(0xB)
	pubC, privC := keyFromSeed(0xC)
	pubU, privU := keyFromSeed(0xD)
	kr, err := NewKeyring([]TrustedKey{
		{PublicKey: pubA, State: KeyStateVerifyOnly, Epoch: 1, RetiredAt: kRetired, Source: "data-dir"},
		{PublicKey: pubB, State: KeyStateCurrent, Epoch: 2, Source: "data-dir"},
		{PublicKey: pubC, State: KeyStateRevoked, Source: "data-dir"},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ks := kr.Keys(); ks[0].State != KeyStateCurrent || ks[1].State != KeyStateVerifyOnly || ks[2].State != KeyStateRevoked {
		t.Fatalf("keyring order = %v %v %v", ks[0].State, ks[1].State, ks[2].State)
	}

	t.Run("A verify-only document issued before retirement verifies", func(t *testing.T) {
		v, err := kr.Verify(signPayload(privA, v3Payload(mustKID(t, pubA), 1, kIssued)), kNow)
		if err != nil {
			t.Fatal(err)
		}
		if v.Trust.KID != mustKID(t, pubA) || v.Trust.State != KeyStateVerifyOnly || v.Trust.Epoch != 1 {
			t.Fatalf("trust = %+v", v.Trust)
		}
	})
	t.Run("B current document verifies", func(t *testing.T) {
		v, err := kr.Verify(signPayload(privB, v3Payload(mustKID(t, pubB), 2, kNow)), kNow)
		if err != nil {
			t.Fatal(err)
		}
		if v.Trust.State != KeyStateCurrent || !v.IsCredentialV3() {
			t.Fatalf("verified = %+v", v.Trust)
		}
	})
	t.Run("A document issued after retirement is refused", func(t *testing.T) {
		_, err := kr.Verify(signPayload(privA, v3Payload(mustKID(t, pubA), 1, kRetired)), kNow)
		if !errors.Is(err, ErrKeyRetired) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("revoked key refuses", func(t *testing.T) {
		_, err := kr.Verify(signPayload(privC, v3Payload(mustKID(t, pubC), 1, kIssued)), kNow)
		if !errors.Is(err, ErrKeyRevoked) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown key refuses and names only the declared derived kid", func(t *testing.T) {
		_, err := kr.Verify(signPayload(privU, v3Payload(mustKID(t, pubU), 1, kIssued)), kNow)
		if !errors.Is(err, ErrKeyUnknown) || !errors.Is(err, ErrBadSignature) {
			t.Fatalf("err = %v", err)
		}
		var te *TrustError
		if !errors.As(err, &te) || te.KID != mustKID(t, pubU) {
			t.Fatalf("trust error = %#v", err)
		}
	})
	t.Run("an arbitrary declared key_id is never echoed", func(t *testing.T) {
		_, err := kr.Verify(signPayload(privU, v3Payload("issuer-<script>", 1, kIssued)), kNow)
		if !errors.Is(err, ErrKeyUnknown) || strings.Contains(err.Error(), "script") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestKeyringNeverTrustsTheDeclaredKeyID: selecting by the document's own key_id must not
// establish trust in either direction.
func TestKeyringNeverTrustsTheDeclaredKeyID(t *testing.T) {
	pubB, _ := keyFromSeed(0xB)
	pubX, privX := keyFromSeed(0xE)
	_, privB := keyFromSeed(0xB)
	kr, err := NewKeyring([]TrustedKey{{PublicKey: pubB, State: KeyStateCurrent}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Signed by an untrusted key while CLAIMING the trusted KID.
	_, err = kr.Verify(signPayload(privX, v3Payload(mustKID(t, pubB), 1, kIssued)), kNow)
	if !errors.Is(err, ErrBadSignature) || errors.Is(err, ErrKeyUnknown) {
		t.Fatalf("claimed trusted kid: err = %v", err)
	}
	// Signed by the trusted key while naming ANOTHER key.
	_, err = kr.Verify(signPayload(privB, v3Payload(mustKID(t, pubX), 1, kIssued)), kNow)
	if !errors.Is(err, ErrKeyIDMismatch) {
		t.Fatalf("mismatched signed kid: err = %v", err)
	}
	// Signed by the trusted key while naming a configured label rather than a derived id.
	_, err = kr.Verify(signPayload(privB, v3Payload("issuer-2026-08", 1, kIssued)), kNow)
	if !errors.Is(err, ErrKeyIDMismatch) {
		t.Fatalf("label kid: err = %v", err)
	}
}

func TestKeyringEpochFences(t *testing.T) {
	pubB, privB := keyFromSeed(0xB)
	kidB := mustKID(t, pubB)
	unpinned, err := NewKeyring([]TrustedKey{{PublicKey: pubB, State: KeyStateCurrent}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := NewKeyring([]TrustedKey{{PublicKey: pubB, State: KeyStateCurrent, Epoch: 3}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := NewKeyring([]TrustedKey{{PublicKey: pubB, State: KeyStateCurrent}}, 4)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		kr    Keyring
		epoch int
		want  error
	}{
		{"unpinned accepts epoch 1", unpinned, 1, nil},
		{"unpinned refuses epoch 0", unpinned, 0, ErrKeyEpochFenced},
		{"pinned accepts its epoch", pinned, 3, nil},
		{"pinned refuses another epoch", pinned, 2, ErrKeyEpochFenced},
		{"fence refuses below the minimum", fenced, 3, ErrKeyEpochFenced},
		{"fence accepts the minimum", fenced, 4, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.kr.Verify(signPayload(privB, v3Payload(kidB, tc.epoch, kIssued)), kNow)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := NewKeyring([]TrustedKey{{PublicKey: pubB, State: KeyStateCurrent, Epoch: 2}}, 3); !errors.Is(err, ErrKeyringInvalid) {
		t.Fatalf("a current key pinned below the fence must refuse the keyring: %v", err)
	}
}

// TestKeyringLegacyFlatLicense keeps the v1/v2 flat container verifiable through trusted keys.
func TestKeyringLegacyFlatLicense(t *testing.T) {
	pubA, privA := keyFromSeed(0xA)
	blob, err := Sign(Claims{Licensee: "Legacy Ltd", IssuedAt: kIssued, ExpiresAt: kIssued.AddDate(1, 0, 0)}, privA)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := NewKeyring([]TrustedKey{{PublicKey: pubA, State: KeyStateCurrent}}, 0)
	v, err := current.Verify(blob, kNow)
	if err != nil || v.Container != ContainerFlat || v.Claims.Licensee != "Legacy Ltd" {
		t.Fatalf("flat under current: %+v %v", v, err)
	}
	retired, _ := NewKeyring([]TrustedKey{{PublicKey: pubA, State: KeyStateVerifyOnly, RetiredAt: kRetired}}, 0)
	if _, err := retired.Verify(blob, kNow); err != nil {
		t.Fatalf("flat issued before retirement: %v", err)
	}
	late, err := Sign(Claims{Licensee: "Late", IssuedAt: kRetired.Add(time.Hour)}, privA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retired.Verify(late, kNow); !errors.Is(err, ErrKeyRetired) {
		t.Fatalf("flat issued after retirement: %v", err)
	}
	fenced, _ := NewKeyring([]TrustedKey{{PublicKey: pubA, State: KeyStateCurrent}}, 2)
	if _, err := fenced.Verify(blob, kNow); !errors.Is(err, ErrKeyEpochFenced) {
		t.Fatalf("flat under an unpinned key and a fence: %v", err)
	}
	pinnedFenced, _ := NewKeyring([]TrustedKey{{PublicKey: pubA, State: KeyStateCurrent, Epoch: 2}}, 2)
	if _, err := pinnedFenced.Verify(blob, kNow); err != nil {
		t.Fatalf("flat under a key pinned at the fence: %v", err)
	}
}

func TestKeyringVerifyUntilEndsTheWindow(t *testing.T) {
	pubA, privA := keyFromSeed(0xA)
	kr, err := NewKeyring([]TrustedKey{{PublicKey: pubA, State: KeyStateVerifyOnly, RetiredAt: kRetired, VerifyUntil: kNow}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	blob := signPayload(privA, v3Payload(mustKID(t, pubA), 1, kIssued))
	if _, err := kr.Verify(blob, kNow.Add(-time.Second)); err != nil {
		t.Fatalf("inside the window: %v", err)
	}
	if _, err := kr.Verify(blob, kNow); !errors.Is(err, ErrKeyRetired) {
		t.Fatalf("at the window end: %v", err)
	}
}

func TestKeyringValidation(t *testing.T) {
	pubA, _ := keyFromSeed(0xA)
	cases := []struct {
		name string
		keys []TrustedKey
		min  int
	}{
		{"duplicate", []TrustedKey{{PublicKey: pubA, State: KeyStateCurrent}, {PublicKey: pubA, State: KeyStateRevoked}}, 0},
		{"unknown state", []TrustedKey{{PublicKey: pubA, State: "active"}}, 0},
		{"verify_only without retired_at", []TrustedKey{{PublicKey: pubA, State: KeyStateVerifyOnly}}, 0},
		{"window ends before retirement", []TrustedKey{{PublicKey: pubA, State: KeyStateVerifyOnly, RetiredAt: kRetired, VerifyUntil: kIssued}}, 0},
		{"current with a window", []TrustedKey{{PublicKey: pubA, State: KeyStateCurrent, RetiredAt: kRetired}}, 0},
		{"negative epoch", []TrustedKey{{PublicKey: pubA, State: KeyStateCurrent, Epoch: -1}}, 0},
		{"negative fence", []TrustedKey{{PublicKey: pubA, State: KeyStateCurrent}}, -1},
		{"short key", []TrustedKey{{PublicKey: pubA[:10], State: KeyStateCurrent}}, 0},
	}
	for _, tc := range cases {
		if _, err := NewKeyring(tc.keys, tc.min); !errors.Is(err, ErrKeyringInvalid) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
	many := make([]TrustedKey, MaxKeyringKeys+1)
	for i := range many {
		p, _ := keyFromSeed(byte(i + 1))
		many[i] = TrustedKey{PublicKey: p, State: KeyStateRevoked}
	}
	if _, err := NewKeyring(many, 0); !errors.Is(err, ErrKeyringInvalid) {
		t.Errorf("oversized keyring: %v", err)
	}
	if _, err := (Keyring{}).Verify("a.b", kNow); !errors.Is(err, ErrNoTrustedKeys) {
		t.Errorf("empty keyring: %v", err)
	}
}

func TestKeyringRefusesOversizedAndMalformedBlobs(t *testing.T) {
	pubA, _ := keyFromSeed(0xA)
	kr, _ := NewKeyring([]TrustedKey{{PublicKey: pubA, State: KeyStateCurrent}}, 0)
	if _, err := kr.Verify(strings.Repeat("a", MaxLicenseBlobBytes+1), kNow); !errors.Is(err, ErrMalformed) {
		t.Fatalf("oversized: %v", err)
	}
	for _, blob := range []string{"", "nodot", "!!.sig", "cGF5bG9hZA.!!"} {
		if _, err := kr.Verify(blob, kNow); !errors.Is(err, ErrMalformed) {
			t.Errorf("%q: %v", blob, err)
		}
	}
}

// TestSingleKeyVerifyEnvelopeIsUnchanged pins the primitive's accepted behavior: it does not
// apply keyring rules, so the golden vectors with configured key_id labels still verify there.
func TestSingleKeyVerifyEnvelopeIsUnchanged(t *testing.T) {
	pubB, privB := keyFromSeed(0xB)
	blob := signPayload(privB, v3Payload("issuer-2026-08", 1, kIssued))
	v, err := VerifyEnvelope(blob, pubB)
	if err != nil || !v.IsCredentialV3() || v.Trust != (Trust{}) {
		t.Fatalf("VerifyEnvelope: %+v %v", v.Trust, err)
	}
	kr, _ := SingleKeyKeyring(pubB, "--pubkey")
	if _, err := kr.Verify(blob, kNow); !errors.Is(err, ErrKeyIDMismatch) {
		t.Fatalf("keyring must refuse the label kid: %v", err)
	}
	empty, err := SingleKeyKeyring(nil, "embedded")
	if err != nil || empty.Len() != 0 {
		t.Fatalf("nil key: %v %d", err, empty.Len())
	}
}

func trustDocJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTrustDocumentRoundTripAndApply(t *testing.T) {
	pubE, privE := keyFromSeed(0x1)
	pubB, privB := keyFromSeed(0xB)
	doc := TrustDocument{MinKeyEpoch: 1, Keys: []TrustDocumentKey{
		{PublicKey: pubE, State: KeyStateVerifyOnly, Epoch: 1, RetiredAt: kRetired},
		{PublicKey: pubB, State: KeyStateCurrent, Epoch: 2},
	}}
	enc, err := EncodeTrustDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := DecodeTrustDocument(enc)
	if err != nil {
		t.Fatalf("decode own encoding: %v\n%s", err, enc)
	}
	again, err := EncodeTrustDocument(dec)
	if err != nil || string(again) != string(enc) {
		t.Fatalf("encoding is not stable: %v\n%s\n%s", err, enc, again)
	}
	kr, err := dec.Apply([]TrustedKey{{PublicKey: pubE, State: KeyStateCurrent, Source: "embedded"}})
	if err != nil {
		t.Fatal(err)
	}
	if kr.Len() != 2 {
		t.Fatalf("merged keys = %d, want the embedded key overridden plus one", kr.Len())
	}
	if _, err := kr.Verify(signPayload(privE, v3Payload(mustKID(t, pubE), 1, kRetired.Add(time.Hour))), kNow); !errors.Is(err, ErrKeyRetired) {
		t.Fatalf("the document must demote the embedded key: %v", err)
	}
	v, err := kr.Verify(signPayload(privB, v3Payload(mustKID(t, pubB), 2, kNow)), kNow)
	if err != nil || v.Trust.Source != "data-dir" {
		t.Fatalf("added key: %+v %v", v.Trust, err)
	}
}

func TestTrustDocumentStrictness(t *testing.T) {
	pubB, _ := keyFromSeed(0xB)
	good := map[string]any{"kid": mustKID(t, pubB), "public_key": base64.StdEncoding.EncodeToString(pubB), "state": "current"}
	cases := map[string][]byte{
		"duplicate field":    []byte(`{"schema":"olivares.license.trust.v1","schema":"olivares.license.trust.v1","keys":[]}`),
		"unknown field":      trustDocJSON(t, map[string]any{"schema": TrustDocumentSchema, "keys": []any{}, "url": "https://x"}),
		"unknown key field":  trustDocJSON(t, map[string]any{"schema": TrustDocumentSchema, "keys": []any{mergeMap(good, "fetch_from", "https://x")}}),
		"wrong schema":       trustDocJSON(t, map[string]any{"schema": "olivares.license.trust.v2", "keys": []any{}}),
		"missing keys":       trustDocJSON(t, map[string]any{"schema": TrustDocumentSchema}),
		"kid not derived":    trustDocJSON(t, map[string]any{"schema": TrustDocumentSchema, "keys": []any{mergeMap(good, "kid", "sha256:"+strings.Repeat("0", 64))}}),
		"bad public key":     trustDocJSON(t, map[string]any{"schema": TrustDocumentSchema, "keys": []any{mergeMap(good, "public_key", "AAAA")}}),
		"unknown state":      trustDocJSON(t, map[string]any{"schema": TrustDocumentSchema, "keys": []any{mergeMap(good, "state", "active")}}),
		"non-UTC instant":    trustDocJSON(t, map[string]any{"schema": TrustDocumentSchema, "keys": []any{mergeMap(mergeMap(good, "state", "verify_only"), "retired_at", "2026-09-10T00:00:00+02:00")}}),
		"fractional epoch":   []byte(`{"schema":"olivares.license.trust.v1","keys":[],"min_key_epoch":1.0}`),
		"trailing data":      []byte(`{"schema":"olivares.license.trust.v1","keys":[]} {}`),
		"duplicate key":      trustDocJSON(t, map[string]any{"schema": TrustDocumentSchema, "keys": []any{good, good}}),
		"oversized document": []byte(`{"schema":"olivares.license.trust.v1","keys":[],"pad":"` + strings.Repeat("x", MaxTrustDocumentBytes) + `"}`),
	}
	for name, data := range cases {
		if _, err := DecodeTrustDocument(data); !errors.Is(err, ErrKeyringInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := DecodeTrustDocument(trustDocJSON(t, map[string]any{"schema": TrustDocumentSchema, "keys": []any{good}})); err != nil {
		t.Fatalf("control: a valid document must decode: %v", err)
	}
}

func mergeMap(m map[string]any, k string, v any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for kk, vv := range m {
		out[kk] = vv
	}
	out[k] = v
	return out
}
