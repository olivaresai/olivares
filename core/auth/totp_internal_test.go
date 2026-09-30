// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// The RFC 6238 Appendix B reference vectors: the ASCII seed
// "12345678901234567890" (+ its SHA256/SHA512 extensions) at the documented
// times, 8 digits. These pin dynamic truncation, the counter derivation and
// all three HMACs against the primary source.
var rfc6238Vectors = []struct {
	name   string
	seed   string
	t      int64
	digits map[string]string
}{
	{name: "T=59", seed: "12345678901234567890", t: 59, digits: map[string]string{
		"SHA1": "94287082", "SHA256": "46119246", "SHA512": "90693936"}},
	{name: "T=1111111109", seed: "12345678901234567890", t: 1111111109, digits: map[string]string{
		"SHA1": "07081804", "SHA256": "68084774", "SHA512": "25091201"}},
	{name: "T=1111111111", seed: "12345678901234567890", t: 1111111111, digits: map[string]string{
		"SHA1": "14050471", "SHA256": "67062674", "SHA512": "99943326"}},
	{name: "T=1234567890", seed: "12345678901234567890", t: 1234567890, digits: map[string]string{
		"SHA1": "89005924", "SHA256": "91819424", "SHA512": "93441116"}},
	{name: "T=2000000000", seed: "12345678901234567890", t: 2000000000, digits: map[string]string{
		"SHA1": "69279037", "SHA256": "90698825", "SHA512": "38618901"}},
	{name: "T=20000000000", seed: "12345678901234567890", t: 20000000000, digits: map[string]string{
		"SHA1": "65353130", "SHA256": "77737706", "SHA512": "47863826"}},
}

func TestRFC6238Vectors(t *testing.T) {
	for _, tc := range rfc6238Vectors {
		for alg, want := range tc.digits {
			newHash, err := totpHash(alg)
			if err != nil {
				t.Fatalf("%s %s: %v", tc.name, alg, err)
			}
			seed := []byte(tc.seed)
			if alg != "SHA1" {
				// The RFC extends the seed by REPEATING the shared-secret
				// digits to the hash length (32 for SHA256, 64 for SHA512).
				want := map[string]int{"SHA256": 32, "SHA512": 64}[alg]
				for len(seed) < want {
					seed = append(seed, tc.seed...)
				}
				seed = seed[:want]
			}
			step := tc.t / 30
			if got := hotp(newHash, seed, uint64(step), 8); got != want {
				t.Errorf("%s %s: code = %s, want %s", tc.name, alg, got, want)
			}
			if _, ok := verifyTOTP(newHash, seed, want, 8, 30, step, 0, 0); !ok {
				t.Errorf("%s %s: verifyTOTP rejected the RFC vector", tc.name, alg)
			}
		}
	}
}

func TestVerifyTOTPWindowAndReplay(t *testing.T) {
	newHash, _ := totpHash("SHA1")
	seed := []byte("window-replay-seed-1234")
	step := int64(1000)
	code := hotp(newHash, seed, uint64(step), 6)

	// Exact step verifies and records itself.
	if got, ok := verifyTOTP(newHash, seed, code, 6, 30, step, 1, 0); !ok || got != step {
		t.Fatalf("exact step = %d, %v", got, ok)
	}
	// ±1 drift verifies; the FORWARD match records its own step.
	if got, ok := verifyTOTP(newHash, seed, code, 6, 30, step-1, 1, 0); !ok || got != step {
		t.Fatalf("backward drift = %d, %v", got, ok)
	}
	if _, ok := verifyTOTP(newHash, seed, code, 6, 30, step+1, 1, 0); !ok {
		t.Fatal("forward drift did not verify")
	}
	// Outside the window does not.
	if _, ok := verifyTOTP(newHash, seed, code, 6, 30, step+2, 1, 0); ok {
		t.Fatal("code from outside the window verified")
	}
	// Replay: the same code against an advanced window does not verify.
	if _, ok := verifyTOTP(newHash, seed, code, 6, 30, step, 1, step); ok {
		t.Fatal("a code from the last used step verified twice")
	}
	// A code from an OLDER step never clears a newer window.
	old := hotp(newHash, seed, uint64(step-5), 6)
	if _, ok := verifyTOTP(newHash, seed, old, 6, 30, step, 1, step); ok {
		t.Fatal("an older code verified against the used window")
	}
	// Malformed input is refused, not panicked on.
	if _, ok := verifyTOTP(newHash, seed, "12", 6, 30, step, 1, 0); ok {
		t.Fatal("short code verified")
	}
}

func TestReformatCode(t *testing.T) {
	for in, want := range map[string]string{
		" 123 456 ": "123456",
		"123-456":   "123456",
		"ABC-DE-FG": "ABCDEFG",
	} {
		if got := ReformatCode(in); got != want {
			t.Errorf("ReformatCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTOTPOtpauthURI(t *testing.T) {
	uri := totpOtpauthURI("Olivares AI", "admin@example.com", "JBSWY3DPEHPK3PXP", "SHA1", 6, 30)
	want := "otpauth://totp/Olivares%20AI:admin@example.com?algorithm=SHA1&digits=6&issuer=Olivares+AI&period=30&secret=JBSWY3DPEHPK3PXP"
	if uri != want {
		t.Fatalf("uri = %q, want %q", uri, want)
	}
}

func TestTOTPQRPNGRendersScannablePNG(t *testing.T) {
	png, err := TOTPQRPNG("otpauth://totp/Olivares%20AI:admin@example.com?secret=JBSWY3DPEHPK3PXP", 240)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(png) == 0 || !bytes.HasPrefix(png, []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("not a PNG: %d bytes, head %x", len(png), png[:8])
	}
}

// The pending store is single-use and TTL-swept: a taken challenge never
// resolves twice, and an expired one resolves never.
func TestTOTPPendingStoreSingleUseAndTTL(t *testing.T) {
	now := time.Now()
	s := newTOTPPendingStore(func() time.Time { return now })
	hash := hashSecret("secret")
	s.putLogin("sel", &totpLoginPending{secretHash: hash, accountID: model.NewID(), expires: now.Add(time.Minute)})
	if _, ok := s.getLogin("sel"); !ok {
		t.Fatal("fresh pending login missing")
	}
	if _, ok := s.takeLogin("sel"); !ok {
		t.Fatal("take refused a live pending login")
	}
	if _, ok := s.takeLogin("sel"); ok {
		t.Fatal("take served the same pending login twice")
	}
	s.putEnrol("key", &totpEnrolPending{seed: []byte("s"), expires: now.Add(-time.Second)})
	if _, ok := s.takeEnrol("key"); ok {
		t.Fatal("expired enrolment served")
	}
}

func TestConstantTimeCodeEquals(t *testing.T) {
	if !constantTimeCodeEquals("123456", "123456") {
		t.Fatal("equal codes did not compare equal")
	}
	if constantTimeCodeEquals("123456", "123457") || constantTimeCodeEquals("123456", "12345") {
		t.Fatal("unequal codes compared equal")
	}
}

func TestNewTOTPRecoveryCodes(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 3; i++ {
		codes, err := newTOTPRecoveryCodes()
		if err != nil {
			t.Fatal(err)
		}
		if len(codes) != totpRecoveryCodes {
			t.Fatalf("minted %d codes, want %d", len(codes), totpRecoveryCodes)
		}
		for _, c := range codes {
			if seen[c] {
				t.Fatalf("recovery code repeated: %s", c)
			}
			seen[c] = true
			if len(c) != 24 || ReformatCode(c) != c {
				t.Fatalf("raw code %q is not a 24-char dashless string", c)
			}
			if d := displayRecoveryCode(c); len(d) != 27 || ReformatCode(d) != c {
				t.Fatalf("display form %q does not round-trip to %q", d, c)
			}
		}
	}
	// Sanity: hashes are of distinct values.
	if hex.EncodeToString(hashSecret("A")) == hex.EncodeToString(hashSecret("B")) {
		t.Fatal("hash collision on distinct inputs")
	}
}
