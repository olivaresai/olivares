// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package license_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
)

// updateGolden regenerates testdata/wireformat_vectors.json from the live
// license.Sign output:  go test ./core/license -run TestWireFormatGolden -update-golden
var updateGolden = flag.Bool("update-golden", false, "regenerate testdata/wireformat_vectors.json")

// goldenSeed is a FIXED, non-secret 32-byte Ed25519 seed dedicated to the
// wire-format golden vectors. Ed25519 from a fixed seed is fully deterministic
// (RFC 8032), so a fixed seed + fixed claims + fixed RFC3339 timestamps yield a
// byte-for-byte reproducible signature. That determinism is the whole point: it
// lets an INDEPENDENT re-implementation of the signer in another language sign the
// exact same vectors with the exact same key and assert it reproduces these blobs
// to the byte — a cross-language conformance harness for the wire format. It signs
// nothing of value (licensing gates nothing — see the package doc), so it is safe
// to commit, like the dev seed.
var goldenSeed = [32]byte{
	0x6f, 0x6c, 0x69, 0x76, 0x61, 0x72, 0x65, 0x73, // "olivares"
	0x20, 0x77, 0x69, 0x72, 0x65, 0x2d, 0x66, 0x6d, // " wire-fm"
	0x74, 0x20, 0x67, 0x6f, 0x6c, 0x64, 0x65, 0x6e, // "t golden"
	0x20, 0x73, 0x65, 0x65, 0x64, 0x20, 0x76, 0x31, // " seed v1"
}

const goldenPath = "testdata/wireformat_vectors.json"

// vectorInput is the WIRE-LEVEL input of a golden vector: the same fields any
// language's signer feeds in, with times as RFC3339 strings (whole-second UTC,
// exactly the form license.toWire emits and the Worker must reproduce). Absent
// optional fields exercise omitempty. It is deliberately NOT license.Claims:
// the conformance contract is at the wire layer, so the TS signer can consume an
// identical JSON shape without a time.Time abstraction.
type vectorInput struct {
	Licensee    string   `json:"licensee"`
	Plan        string   `json:"plan,omitempty"`
	SupportTier string   `json:"support_tier,omitempty"`
	HolderID    string   `json:"holder_id,omitempty"`
	Serial      string   `json:"serial,omitempty"`
	Profile     string   `json:"profile,omitempty"`
	Features    []string `json:"features,omitempty"`
	MaxTenants  int      `json:"max_tenants,omitempty"`
	MaxUsers    int      `json:"max_users,omitempty"`
	IssuedAt    string   `json:"issued_at"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
	GraceUntil  string   `json:"grace_until,omitempty"`
}

type goldenVector struct {
	Name string      `json:"name"`
	Note string      `json:"note,omitempty"`
	In   vectorInput `json:"input"`
	// ExpectedPayload is the EXACT canonical JSON string that gets signed (for
	// human inspection + to let the TS test localize a divergence to encoder vs
	// signature). ExpectedBlob is the full base64url(payload).base64url(sig).
	ExpectedPayload string `json:"expected_payload"`
	ExpectedBlob    string `json:"expected_blob"`
}

type goldenFile struct {
	Comment       string         `json:"_comment"`
	PrivateKeyB64 string         `json:"private_key_b64"`
	PublicKeyB64  string         `json:"public_key_b64"`
	Vectors       []goldenVector `json:"vectors"`
}

// goldenInputs are the scenarios that lock every branch of the wire encoder:
// omitempty drop, full presence, Go's HTML escaping (&,<,>), the U+2028/U+2029
// line separators, control chars / quote / backslash, multi-byte UTF-8
// passthrough, the integer claims, and the bare max_users enterprise case.
func goldenInputs() []goldenVector {
	return []goldenVector{
		{
			Name: "minimal_perpetual",
			Note: "licensee + issued_at only; omitempty drops everything else; perpetual (no expires_at)",
			In:   vectorInput{Licensee: "Acme GmbH", IssuedAt: "2026-01-02T03:04:05Z"},
		},
		{
			Name: "max_users_only",
			Note: "the common enterprise case: a seat entitlement, perpetual",
			In:   vectorInput{Licensee: "Beta Corp", Plan: "commercial", HolderID: "polar_sub_123", MaxUsers: 25, IssuedAt: "2026-06-23T10:00:00Z"},
		},
		{
			Name: "full_with_expiry",
			Note: "every field set incl. expiry + support_tier — locks field ORDER and presence",
			In: vectorInput{
				Licensee: "Gamma S.L.", Plan: "commercial", SupportTier: "enterprise", HolderID: "polar_cus_abc",
				Features: []string{"sso", "worm", "ha"}, MaxTenants: 50, MaxUsers: 200,
				IssuedAt: "2026-06-23T10:00:00Z", ExpiresAt: "2027-06-23T10:00:00Z",
			},
		},
		{
			Name: "html_escape",
			Note: "Go json.Marshal escapes & < > by default (SetEscapeHTML) -> a naive JSON.stringify DIVERGES here",
			In: vectorInput{
				Licensee: `Acme & Sons <Holdings> "Inc."`, Plan: "commercial",
				HolderID: "id>with>gt", Features: []string{"a&b", "c<d"},
				IssuedAt: "2026-06-23T10:00:00Z",
			},
		},
		{
			Name: "unicode_passthrough",
			Note: "non-ASCII runes are emitted as raw UTF-8 (NOT \\u escaped), but & still escapes",
			In: vectorInput{
				Licensee: "Müller & Associés — Genève ☃ 株式会社", Plan: "commercial",
				IssuedAt: "2026-06-23T10:00:00Z",
			},
		},
		{
			Name: "line_and_paragraph_separators",
			Note: "U+2028 and U+2029 are escaped by Go's encoder (\\u2028/\\u2029) even though they are valid UTF-8",
			In: vectorInput{
				Licensee: "before\u2028mid\u2029after", IssuedAt: "2026-06-23T10:00:00Z",
			},
		},
		{
			Name: "control_chars_quote_backslash",
			Note: "tab/newline short-forms, \\u0001 long-form, escaped quote and backslash",
			In: vectorInput{
				Licensee: "tab\tnl\nctrlq\"bs\\xend", IssuedAt: "2026-06-23T10:00:00Z",
			},
		},
		{
			Name: "expired",
			Note: "valid signature over a past expiry — status is a local-clock decision, not a signing one",
			In: vectorInput{
				Licensee: "Old Co", Plan: "commercial", MaxUsers: 5,
				IssuedAt: "2024-01-01T00:00:00Z", ExpiresAt: "2025-01-01T00:00:00Z",
			},
		},
		{
			Name: "serial_and_profile",
			Note: "serial + profile ride the wire AFTER holder_id and BEFORE features — the short-lived online license the worker re-issues each cycle",
			In: vectorInput{
				Licensee: "Delta Ltd", Plan: "commercial", HolderID: "polar_cus_del",
				Serial: "d2f1a0be-9c34-4e57-8a21-0f6b3c9d4e5f", Profile: "online", MaxUsers: 50,
				IssuedAt: "2026-07-19T10:00:00Z", ExpiresAt: "2026-10-17T10:00:00Z",
			},
		},
		{
			// the attested grace window. It rides LAST on the wire, after
			// expires_at, so every vector above encodes byte-for-byte as it always did
			// — which is the compatibility claim this vector exists to prove alongside.
			Name: "attested_grace_window",
			Note: "term-only: the issuer attests the 168h grace window; absent everywhere else, so omitempty drops it",
			In: vectorInput{
				Licensee: "Zeta Systems", Plan: "commercial", HolderID: "dodo_sub_zeta",
				Serial: "9f1c4d22-7a63-4b81-9e05-2c8d7f6a1b34", Profile: "online", Features: []string{"regulated"},
				IssuedAt: "2026-08-02T09:00:00Z", ExpiresAt: "2026-09-02T09:00:00Z", GraceUntil: "2026-09-09T09:00:00Z",
			},
		},
		{
			Name: "airgapped_serial_no_holder",
			Note: "an air-gapped SKU blob — serial present without holder_id, profile airgapped, 12-month term",
			In: vectorInput{
				Licensee: "Epsilon Gov", Plan: "commercial",
				Serial: "0b7e2c11-5d6f-4a89-9c30-e4d5f6a7b8c9", Profile: "airgapped", MaxUsers: 500,
				IssuedAt: "2026-07-19T10:00:00Z", ExpiresAt: "2027-07-19T10:00:00Z",
			},
		},
	}
}

func toClaims(t *testing.T, in vectorInput) license.Claims {
	t.Helper()
	c := license.Claims{
		Licensee: in.Licensee, Plan: in.Plan, SupportTier: in.SupportTier, HolderID: in.HolderID,
		Serial: in.Serial, Profile: in.Profile,
		Features: in.Features, MaxTenants: in.MaxTenants, MaxUsers: in.MaxUsers,
	}
	issued, err := time.Parse(time.RFC3339, in.IssuedAt)
	if err != nil {
		t.Fatalf("vector issued_at %q: %v", in.IssuedAt, err)
	}
	c.IssuedAt = issued
	if in.ExpiresAt != "" {
		exp, err := time.Parse(time.RFC3339, in.ExpiresAt)
		if err != nil {
			t.Fatalf("vector expires_at %q: %v", in.ExpiresAt, err)
		}
		c.ExpiresAt = exp
	}
	if in.GraceUntil != "" {
		gu, err := time.Parse(time.RFC3339, in.GraceUntil)
		if err != nil {
			t.Fatalf("vector grace_until %q: %v", in.GraceUntil, err)
		}
		c.GraceUntil = gu
	}
	return c
}

// TestWireFormatGolden locks the EXACT on-the-wire bytes of license.Sign against
// committed golden vectors. A change to toWire (field order, omitempty), the JSON
// escaping, or the RFC3339 formatting changes the signed payload and breaks this
// test — which is the point: the wire format is a cross-language contract (an
// independent signer in another language must reproduce these bytes) and must never
// drift silently. Regenerate intentionally with -update-golden after a deliberate change.
func TestWireFormatGolden(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(goldenSeed[:])
	pub := priv.Public().(ed25519.PublicKey)

	if *updateGolden {
		writeGolden(t, priv, pub)
	}

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update-golden to create it): %v", err)
	}
	var gf goldenFile
	if err := json.Unmarshal(raw, &gf); err != nil {
		t.Fatalf("parse golden: %v", err)
	}

	// The committed key must be exactly this build's deterministic golden key,
	// else the blobs are not reproducible by an independent signer.
	if want := license.EncodeKey(priv); gf.PrivateKeyB64 != want {
		t.Fatalf("golden private key drifted from the fixed seed; regenerate with -update-golden")
	}

	for _, v := range gf.Vectors {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			c := toClaims(t, v.In)
			blob, err := license.Sign(c, priv)
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			if blob != v.ExpectedBlob {
				t.Errorf("blob drift\n  got:  %s\n  want: %s", blob, v.ExpectedBlob)
			}
			// The blob the open binary trusts must verify against the public key.
			got, err := license.Verify(blob, pub)
			if err != nil {
				t.Fatalf("verify own blob: %v", err)
			}
			if got.Licensee != c.Licensee || got.MaxUsers != c.MaxUsers {
				t.Errorf("round-trip mismatch: %+v vs %+v", got, c)
			}
		})
	}
}

func writeGolden(t *testing.T, priv ed25519.PrivateKey, pub ed25519.PublicKey) {
	t.Helper()
	vectors := goldenInputs()
	for i := range vectors {
		c := toClaims(t, vectors[i].In)
		blob, err := license.Sign(c, priv)
		if err != nil {
			t.Fatalf("sign %s: %v", vectors[i].Name, err)
		}
		payloadB64, _, ok := strings.Cut(blob, ".")
		if !ok {
			t.Fatalf("malformed blob for %s", vectors[i].Name)
		}
		payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
		if err != nil {
			t.Fatalf("decode payload %s: %v", vectors[i].Name, err)
		}
		vectors[i].ExpectedPayload = string(payload)
		vectors[i].ExpectedBlob = blob
	}
	gf := goldenFile{
		Comment: "Wire-format golden vectors locking core/license.Sign byte-for-byte. " +
			"Cross-language conformance oracle: an independent re-implementation of the signer " +
			"signing the same input with private_key_b64 MUST reproduce expected_blob exactly. " +
			"Regenerate with: go test ./core/license -run TestWireFormatGolden -update-golden",
		PrivateKeyB64: license.EncodeKey(priv),
		PublicKeyB64:  license.EncodeKey(pub),
		Vectors:       vectors,
	}
	if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := json.MarshalIndent(gf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goldenPath, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d golden vectors to %s", len(vectors), goldenPath)
}
