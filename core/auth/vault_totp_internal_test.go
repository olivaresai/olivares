// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"fmt"
	"testing"
	"time"
)

// rfcSeed is the RFC 6238 Appendix B shared secret for one algorithm: the ASCII digits
// repeated to the hash length (20, 32 or 64 bytes).
func rfcSeed(algorithm string) []byte {
	const digits = "12345678901234567890"
	seed := []byte(digits)
	n := map[string]int{"SHA1": 20, "SHA256": 32, "SHA512": 64}[algorithm]
	for len(seed) < n {
		seed = append(seed, digits...)
	}
	return seed[:n]
}

// The vault's code is the RFC 6238 code: every Appendix B vector, all three hashes,
// and the seconds left in the window.
func TestTotpCodeAtMatchesRFC6238(t *testing.T) {
	for _, tc := range rfc6238Vectors {
		for alg, want := range tc.digits {
			got, err := totpCodeAt(alg, rfcSeed(alg), 8, 30, time.Unix(tc.t, 0))
			if err != nil {
				t.Fatalf("%s %s: %v", tc.name, alg, err)
			}
			if got.Code != want || got.Period != 30 || got.Digits != 8 || got.SecondsLeft != 30-int(tc.t%30) {
				t.Errorf("%s %s: %+v, want code %s with %d seconds left", tc.name, alg, got, want, 30-int(tc.t%30))
			}
		}
	}
	if _, err := totpCodeAt("MD5", rfcSeed("SHA1"), 6, 30, time.Unix(59, 0)); err == nil {
		t.Error("an unsupported algorithm produced a code")
	}
}

// An item's own parameters (an otpauth URI from the service's QR page) decide the
// code, not the defaults.
func TestVaultItemTOTPCodeUsesTheURIParameters(t *testing.T) {
	for alg, want := range map[string]string{"SHA1": "94287082", "SHA256": "46119246", "SHA512": "90693936"} {
		seed := totpB32.EncodeToString(rfcSeed(alg))
		item, err := ParseVaultItem(fmt.Sprintf(
			`{"type":"totp","totp":"otpauth://totp/Bank:ann?secret=%s&algorithm=%s&digits=8&period=30"}`, seed, alg))
		if err != nil {
			t.Fatalf("%s: %v", alg, err)
		}
		got, err := item.TOTPCode(time.Unix(59, 0))
		if err != nil || got.Code != want || got.Digits != 8 || got.Period != 30 || got.SecondsLeft != 1 {
			t.Errorf("%s: %+v, %v, want %s", alg, got, err, want)
		}
	}
	plain, err := ParseVaultItem(`{"type":"totp","totp":"` + totpB32.EncodeToString(rfcSeed("SHA1")) + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := plain.TOTPCode(time.Unix(59, 0)); err != nil || got.Code != "287082" || got.Digits != 6 {
		t.Errorf("bare seed: %+v, %v, want the 6-digit SHA1 code 287082", got, err)
	}
	if _, err := (VaultItem{Type: "note", Note: "x"}).TOTPCode(time.Unix(59, 0)); err == nil {
		t.Error("a note produced a code")
	}
}
