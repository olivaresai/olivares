// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package connectv1

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha1" //nolint:gosec // git blob identity, not a security digest
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The two vector files are cross-language evidence, not Go-authored expectations:
//
//   - canonical-vectors.b-16b41ca.json is byte-identical to the Worker's committed
//     commercial/license-worker/src/connect/canonical-vectors.json at protocol commit
//     16b41ca56de959528557076b92731317872fb6c1 (git blob d986f43690124a1b1a006bc2d10984a72dcc5dd4,
//     checked below).
//   - ts-generated-vectors.b-16b41ca.json was produced by running that commit's canonical.ts and
//     keys.ts under Node 24 (assessments/implementation/r115-connect-client-current/evidence).
const (
	bCommittedVectors = "testdata/canonical-vectors.b-16b41ca.json"
	bCommittedBlobID  = "d986f43690124a1b1a006bc2d10984a72dcc5dd4"
	tsGeneratedFile   = "testdata/ts-generated-vectors.b-16b41ca.json"
)

func gitBlobID(b []byte) string {
	h := sha1.New() //nolint:gosec // git object id
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// messageFromJSON builds a PopMessage from a vector with the TypeScript field names, refusing
// what the Go type cannot hold (fractional or non-numeric integers, a different domain).
func messageFromJSON(raw json.RawMessage) (PopMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return PopMessage{}, err
	}
	str := func(k string) (string, error) {
		s, ok := m[k].(string)
		if !ok {
			return "", fmt.Errorf("%s is not a string", k)
		}
		return s, nil
	}
	num := func(k string) (int64, error) {
		n, ok := m[k].(json.Number)
		if !ok {
			return 0, fmt.Errorf("%s is not a number", k)
		}
		return n.Int64()
	}
	var out PopMessage
	var err error
	if d, _ := str("domain"); d != Domain {
		return PopMessage{}, fmt.Errorf("domain %q is not %q", d, Domain)
	}
	if out.BindingEpoch, err = num("binding_epoch"); err != nil {
		return PopMessage{}, err
	}
	if out.Exp, err = num("exp"); err != nil {
		return PopMessage{}, err
	}
	for _, f := range []struct {
		k string
		p *string
	}{
		{"body_sha256", &out.BodySHA256}, {"challenge", &out.Challenge}, {"idempotency_key", &out.IdempotencyKey},
		{"kid", &out.KID}, {"method", &out.Method}, {"operation", &out.Operation}, {"origin", &out.Origin},
		{"path", &out.Path}, {"target", &out.Target},
	} {
		if *f.p, err = str(f.k); err != nil {
			return PopMessage{}, err
		}
	}
	return out, nil
}

func TestCommittedWorkerVectorsAreByteIdentical(t *testing.T) {
	raw, err := os.ReadFile(bCommittedVectors)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitBlobID(raw); got != bCommittedBlobID {
		t.Fatalf("vector file blob = %s, want the Worker's committed blob %s", got, bCommittedBlobID)
	}
	var doc struct {
		Schema  string `json:"schema"`
		Domain  string `json:"domain"`
		Vectors []struct {
			Name      string          `json:"name"`
			Message   json.RawMessage `json:"message"`
			Canonical string          `json:"canonical"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Domain != Domain || len(doc.Vectors) == 0 {
		t.Fatalf("unexpected vector document: %s %d", doc.Domain, len(doc.Vectors))
	}
	for _, v := range doc.Vectors {
		m, err := messageFromJSON(v.Message)
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		got, err := EncodePopMessage(m)
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		if string(got) != v.Canonical {
			t.Fatalf("%s:\n got %s\nwant %s", v.Name, got, v.Canonical)
		}
	}
}

// The Worker's vectors at c04256a4 (published to the client by B's PROTOCOL.md and Root's wire
// relay) add the rotate-key dual-proof vector with three mismatch cases.
const (
	bRotationVectors = "testdata/canonical-vectors.b-c04256a.json"
	bRotationBlobID  = "536880dba0a0bdb599120a62472d03dd7e0aa8aa"
)

func TestWorkerRotationVectorsWithNewKeyProof(t *testing.T) {
	raw, err := os.ReadFile(bRotationVectors)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitBlobID(raw); got != bRotationBlobID {
		t.Fatalf("vector file blob = %s, want the Worker's committed blob %s", got, bRotationBlobID)
	}
	var doc struct {
		Vectors []struct {
			Name      string          `json:"name"`
			Body      string          `json:"body"`
			Message   json.RawMessage `json:"message"`
			Canonical string          `json:"canonical"`
			Keys      map[string]struct {
				SeedHex   string `json:"seed_hex"`
				PublicKey string `json:"public_key"`
				KID       string `json:"kid"`
			} `json:"keys"`
			Headers    map[string]string `json:"headers"`
			Mismatches []struct {
				Name              string `json:"name"`
				Signature         string `json:"signature"`
				VerifiesWith      string `json:"verifies_with"`
				ValidForCanonical bool   `json:"valid_for_canonical"`
			} `json:"mismatches"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, v := range doc.Vectors {
		m, err := messageFromJSON(v.Message)
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		if enc, err := EncodePopMessage(m); err != nil || string(enc) != v.Canonical {
			t.Fatalf("%s canonical:\n got %q (%v)\nwant %q", v.Name, enc, err, v.Canonical)
		}
		if len(v.Keys) == 0 {
			continue
		}
		checked++
		if v.Body != "" && BodyDigest([]byte(v.Body)) != m.BodySHA256 {
			t.Fatalf("%s: the body digest does not commit the published body", v.Name)
		}
		pubs := map[string]ed25519.PublicKey{}
		privs := map[string]ed25519.PrivateKey{}
		for role, k := range v.Keys {
			priv := privFromSeedHex(t, k.SeedHex)
			pub := priv.Public().(ed25519.PublicKey)
			kid, _ := KID(pub)
			if EncodePublicKey(pub) != k.PublicKey || kid != k.KID {
				t.Fatalf("%s: %s key identity differs", v.Name, role)
			}
			pubs[role], privs[role] = pub, priv
		}
		if m.KID != v.Keys["old"].KID {
			t.Fatalf("%s: the rotation message must carry the old key's kid", v.Name)
		}
		if !bytes.Contains([]byte(v.Body), []byte(`"new_public_key":"`+v.Keys["new"].PublicKey+`"`)) {
			t.Fatalf("%s: the body does not commit new_public_key", v.Name)
		}
		proof, _ := Sign(privs["old"], m)
		newProof, _ := Sign(privs["new"], m)
		if proof != v.Headers[HeaderProof] || newProof != v.Headers[HeaderNewKeyProof] {
			t.Fatalf("%s: Go proofs differ from the Worker's published headers", v.Name)
		}
		if !Verify(pubs["old"], m, v.Headers[HeaderProof]) || !Verify(pubs["new"], m, v.Headers[HeaderNewKeyProof]) {
			t.Fatalf("%s: a published proof does not verify in Go", v.Name)
		}
		if len(v.Mismatches) != 3 {
			t.Fatalf("%s: %d mismatch cases, want 3", v.Name, len(v.Mismatches))
		}
		for _, mm := range v.Mismatches {
			if got := Verify(pubs[mm.VerifiesWith], m, mm.Signature); got != mm.ValidForCanonical {
				t.Fatalf("%s / %s: Go accepts=%v, Worker says %v", v.Name, mm.Name, got, mm.ValidForCanonical)
			}
		}
	}
	if checked != 1 {
		t.Fatalf("expected exactly one dual-proof vector, found %d", checked)
	}
}

type tsVectorDoc struct {
	Schema string `json:"schema"`
	Keys   []struct {
		SeedHex         string `json:"seed_hex"`
		PublicKeyB64url string `json:"public_key_b64url"`
		KID             string `json:"kid"`
		Fingerprint     string `json:"fingerprint"`
	} `json:"keys"`
	Valid []struct {
		Name            string          `json:"name"`
		Message         json.RawMessage `json:"message"`
		Canonical       string          `json:"canonical"`
		CanonicalSHA256 string          `json:"canonical_sha256"`
		Signatures      []struct {
			PublicKeyB64url  string `json:"public_key_b64url"`
			CanonicalWithKID string `json:"canonical_with_kid"`
			SignatureB64url  string `json:"signature_b64url"`
		} `json:"signatures"`
	} `json:"valid"`
	Invalid []struct {
		Name    string          `json:"name"`
		Message json.RawMessage `json:"message"`
		Refused bool            `json:"refused"`
	} `json:"invalid"`
	Strict []struct {
		Input    string `json:"input"`
		Accepted bool   `json:"accepted"`
	} `json:"strict"`
	ExactFields []struct {
		Obj      string   `json:"obj"`
		Required []string `json:"required"`
		Optional []string `json:"optional"`
		Accepted bool     `json:"accepted"`
	} `json:"exact_fields"`
	BodyDigests []struct {
		Body   string `json:"body"`
		SHA256 string `json:"sha256"`
	} `json:"body_digests"`
	DualProofs []struct {
		Name                  string          `json:"name"`
		Message               json.RawMessage `json:"message"`
		Canonical             string          `json:"canonical"`
		OldPublicKey          string          `json:"old_public_key_b64url"`
		NewPublicKey          string          `json:"new_public_key_b64url"`
		OldSignature          string          `json:"old_signature_b64url"`
		NewSignature          string          `json:"new_signature_b64url"`
		NewSignatureOverOwnID string          `json:"new_signature_over_own_kid_b64url"`
		Checks                map[string]bool `json:"checks"`
	} `json:"dual_proofs"`
}

// TestTSGeneratedDualProof is the rotation's second signature (Root direction item 4): the
// proposed key signs the SAME canonical message — the bound key's kid and epoch unchanged — and a
// signature over a message carrying the proposed key's own kid does not stand in for it.
func TestTSGeneratedDualProof(t *testing.T) {
	doc := loadTSVectors(t)
	if len(doc.DualProofs) == 0 {
		t.Fatal("the generated vector file carries no dual-proof vector")
	}
	seeds := map[string]ed25519.PrivateKey{}
	for _, k := range doc.Keys {
		seeds[k.PublicKeyB64url] = privFromSeedHex(t, k.SeedHex)
	}
	for _, v := range doc.DualProofs {
		m, err := messageFromJSON(v.Message)
		if err != nil {
			t.Fatal(err)
		}
		if enc, err := EncodePopMessage(m); err != nil || string(enc) != v.Canonical {
			t.Fatalf("%s canonical: %q %v", v.Name, enc, err)
		}
		oldPub, _ := DecodePublicKey(v.OldPublicKey)
		newPub, _ := DecodePublicKey(v.NewPublicKey)
		if got, _ := KID(oldPub); got != m.KID {
			t.Fatalf("the rotation message must carry the bound key's kid")
		}
		oldSig, _ := Sign(seeds[v.OldPublicKey], m)
		newSig, _ := Sign(seeds[v.NewPublicKey], m)
		if oldSig != v.OldSignature || newSig != v.NewSignature {
			t.Fatalf("%s: Go proofs differ from the TypeScript proofs", v.Name)
		}
		if !Verify(oldPub, m, v.OldSignature) || !Verify(newPub, m, v.NewSignature) {
			t.Fatalf("%s: a TypeScript proof does not verify in Go", v.Name)
		}
		if Verify(newPub, m, v.NewSignatureOverOwnID) || Verify(newPub, m, v.OldSignature) {
			t.Fatalf("%s: a mismatched proof verified as the new-key proof", v.Name)
		}
		for name, want := range map[string]bool{"old_verifies": true, "new_verifies_same_message": true,
			"new_with_own_kid_verifies_same_message": false, "old_signature_under_new_key": false} {
			if v.Checks[name] != want {
				t.Fatalf("%s: TypeScript check %s = %v, want %v", v.Name, name, v.Checks[name], want)
			}
		}
	}
}

func loadTSVectors(t *testing.T) tsVectorDoc {
	t.Helper()
	raw, err := os.ReadFile(tsGeneratedFile)
	if err != nil {
		t.Fatal(err)
	}
	var doc tsVectorDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != "olivares.connect.v1.go-client-vectors" || len(doc.Valid) < 5 || len(doc.Invalid) < 8 || len(doc.Strict) < 19 {
		t.Fatalf("the generated vector file is incomplete: %s valid=%d invalid=%d strict=%d", doc.Schema, len(doc.Valid), len(doc.Invalid), len(doc.Strict))
	}
	return doc
}

func privFromSeedHex(t *testing.T, s string) ed25519.PrivateKey {
	t.Helper()
	seed, err := hex.DecodeString(s)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("seed %q: %v", s, err)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func TestTSGeneratedKeyIdentity(t *testing.T) {
	doc := loadTSVectors(t)
	for _, k := range doc.Keys {
		pub := privFromSeedHex(t, k.SeedHex).Public().(ed25519.PublicKey)
		if got := EncodePublicKey(pub); got != k.PublicKeyB64url {
			t.Errorf("public key = %s, want %s", got, k.PublicKeyB64url)
		}
		if got, _ := KID(pub); got != k.KID {
			t.Errorf("kid = %s, want %s", got, k.KID)
		}
		if got, _ := Fingerprint(pub); got != k.Fingerprint {
			t.Errorf("fingerprint = %s, want %s", got, k.Fingerprint)
		}
		back, err := DecodePublicKey(k.PublicKeyB64url)
		if err != nil || !bytes.Equal(back, pub) {
			t.Errorf("decode public key: %v", err)
		}
	}
}

// TestTSGeneratedCanonicalAndSignatures: Ed25519 is deterministic, so a Go signature over the Go
// encoding must be the SAME string the TypeScript signer produced over its encoding, and the
// TypeScript signature must verify in Go.
func TestTSGeneratedCanonicalAndSignatures(t *testing.T) {
	doc := loadTSVectors(t)
	keys := map[string]ed25519.PrivateKey{}
	for _, k := range doc.Keys {
		keys[k.PublicKeyB64url] = privFromSeedHex(t, k.SeedHex)
	}
	for _, v := range doc.Valid {
		t.Run(v.Name, func(t *testing.T) {
			m, err := messageFromJSON(v.Message)
			if err != nil {
				t.Fatal(err)
			}
			got, err := EncodePopMessage(m)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != v.Canonical {
				t.Fatalf("canonical:\n got %q\nwant %q", got, v.Canonical)
			}
			if BodyDigest(got) != v.CanonicalSHA256 {
				t.Fatalf("canonical digest mismatch")
			}
			for _, s := range v.Signatures {
				priv, ok := keys[s.PublicKeyB64url]
				if !ok {
					t.Fatalf("unknown signing key %s", s.PublicKeyB64url)
				}
				pub := priv.Public().(ed25519.PublicKey)
				withKID := m
				withKID.KID, _ = KID(pub)
				enc, err := EncodePopMessage(withKID)
				if err != nil || string(enc) != s.CanonicalWithKID {
					t.Fatalf("canonical with kid:\n got %q (%v)\nwant %q", enc, err, s.CanonicalWithKID)
				}
				sig, err := Sign(priv, withKID)
				if err != nil {
					t.Fatal(err)
				}
				if sig != s.SignatureB64url {
					t.Fatalf("Go signature %s differs from the TypeScript signature %s", sig, s.SignatureB64url)
				}
				if !Verify(pub, withKID, s.SignatureB64url) {
					t.Fatalf("the TypeScript signature does not verify in Go")
				}
				tampered := withKID
				tampered.Target += "x"
				if Verify(pub, tampered, s.SignatureB64url) {
					t.Fatalf("a signature verified over a different target")
				}
			}
		})
	}
}

func TestTSGeneratedRefusalsAreRefusedInGo(t *testing.T) {
	doc := loadTSVectors(t)
	for _, v := range doc.Invalid {
		if !v.Refused {
			t.Fatalf("%s: the vector file says TypeScript accepted it; this test only carries refusals", v.Name)
		}
		m, err := messageFromJSON(v.Message)
		if err != nil {
			continue // refused by the Go type itself (fractional integer, other domain)
		}
		if _, err := EncodePopMessage(m); !errors.Is(err, ErrCanonical) {
			t.Errorf("%s: Go encoded a message TypeScript refuses (err=%v)", v.Name, err)
		}
	}
}

func TestTSGeneratedStrictReaderAgreement(t *testing.T) {
	doc := loadTSVectors(t)
	for _, v := range doc.Strict {
		_, err := ParseStrictObject([]byte(v.Input), BodyMax)
		if accepted := err == nil; accepted != v.Accepted {
			t.Errorf("input %q: Go accepted=%v (%v), TypeScript accepted=%v", v.Input, accepted, err, v.Accepted)
		}
	}
	for _, v := range doc.ExactFields {
		obj, err := ParseStrictObject([]byte(v.Obj), BodyMax)
		if err == nil {
			err = RequireExactFields(obj, v.Required, v.Optional)
		}
		if accepted := err == nil; accepted != v.Accepted {
			t.Errorf("exact fields %q: Go accepted=%v (%v), TypeScript accepted=%v", v.Obj, accepted, err, v.Accepted)
		}
	}
	for _, v := range doc.BodyDigests {
		if got := BodyDigest([]byte(v.Body)); got != v.SHA256 {
			t.Errorf("digest of %q = %s, want %s", v.Body, got, v.SHA256)
		}
	}
}

func TestGoOnlyStrictnessRefusesWhatGoCannotRepresent(t *testing.T) {
	base := PopMessage{BindingEpoch: 0, BodySHA256: strings.Repeat("a", 64), Challenge: "n", Exp: 1,
		IdempotencyKey: "idem-0001", KID: "k", Method: "POST", Operation: OpRefresh, Origin: "https://x", Path: PathRefresh, Target: "dep"}
	if _, err := EncodePopMessage(base); err != nil {
		t.Fatalf("control: %v", err)
	}
	bad := base
	bad.Target = "dep\xff"
	if _, err := EncodePopMessage(bad); !errors.Is(err, ErrCanonical) {
		t.Errorf("invalid UTF-8: %v", err)
	}
	bad = base
	bad.Exp = MaxSafeInteger + 1
	if _, err := EncodePopMessage(bad); !errors.Is(err, ErrCanonical) {
		t.Errorf("unsafe exp: %v", err)
	}
	for _, in := range []string{
		`{"a":"\ud83d"}`,
		`{"a":"\ude00"}`,
		"{\"a\":\"\xff\"}",
		`{"a":` + strings.Repeat("[", maxStrictDepth+2) + strings.Repeat("]", maxStrictDepth+2) + `}`,
	} {
		if _, err := ParseStrictObject([]byte(in), BodyMax); !errors.Is(err, ErrStrictJSON) {
			t.Errorf("%q: %v", in, err)
		}
	}
	obj, err := ParseStrictObject([]byte(`{"a":"\ud83d\ude00"}`), BodyMax)
	if err != nil || obj["a"] != "\U0001F600" {
		t.Errorf("surrogate pair: %v %q", err, obj["a"])
	}
	if _, err := ParseStrictObject([]byte(`{"a":1}`), 3); !errors.Is(err, ErrStrictJSON) {
		t.Errorf("size bound: %v", err)
	}
}

func TestIntendedRoutesMirrorTheWorker(t *testing.T) {
	cases := []struct{ op, target, method, path string }{
		{OpBindPending, "scope:dodo/b/h", "POST", PathDeployments},
		{OpBind, "req_1", "POST", PathDeployments},
		{OpRecover, "dep_1", "POST", "/connect/deployments/dep_1/rotate-key"},
		{OpReactivate, "dep_1", "POST", "/connect/deployments/dep_1/rotate-key"},
		{OpRefresh, "dep_1", "POST", PathRefresh},
		{OpRotateKey, "dep_1", "POST", "/connect/deployments/dep_1/rotate-key"},
		{OpDelete, "dep_1", "DELETE", "/connect/deployments/dep_1"},
	}
	for _, tc := range cases {
		m, p, err := IntendedRoute(tc.op, tc.target)
		if err != nil || m != tc.method || p != tc.path {
			t.Errorf("%s: %s %s %v", tc.op, m, p, err)
		}
	}
	for _, bad := range []string{"../x", "a/b", "", strings.Repeat("a", 81), "dep?x"} {
		if _, _, err := IntendedRoute(OpRotateKey, bad); err == nil {
			t.Errorf("route-unsafe id %q accepted", bad)
		}
	}
	if _, _, err := IntendedRoute("promote", "x"); err == nil {
		t.Error("unknown operation accepted")
	}
}

func TestErrorCodeDisplayNeverEchoesUnknownText(t *testing.T) {
	if ErrApprovalRequired.Display() != ErrApprovalRequired || !ErrApprovalRequired.Known() {
		t.Fatal("known code")
	}
	if got := ErrorCode("<script>").Display(); got != ErrorCodeUnknown {
		t.Fatalf("unknown code displayed as %q", got)
	}
}

func TestNewIdempotencyKeyShape(t *testing.T) {
	k, err := NewIdempotencyKey(bytes.NewReader(bytes.Repeat([]byte{7}, 32)))
	if err != nil || len(k) != 43 {
		t.Fatalf("key %q: %v", k, err)
	}
	if _, err := NewIdempotencyKey(bytes.NewReader(nil)); err == nil {
		t.Fatal("a short random source must fail, never yield a weak key")
	}
}
