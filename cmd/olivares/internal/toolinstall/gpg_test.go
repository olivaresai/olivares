// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
)

func TestClaudeReleaseKeyMatchesRetainedDigestAndPin(t *testing.T) {
	sum := sha256.Sum256([]byte(claudeReleaseKey))
	if got := hex.EncodeToString(sum[:]); got != claudeReleaseKeySHA256 {
		t.Fatalf("embedded key sha256 %s, retained %s", got, claudeReleaseKeySHA256)
	}
	toolinstalltest.RequireGPG(t)
	v, err := NewGPGVerifier(context.Background(), exec.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	// The embedded key material carries exactly the pinned fingerprint.
	fprs, err := v.showFingerprintsOf(t, []byte(claudeReleaseKey))
	if err != nil {
		t.Fatal(err)
	}
	if len(fprs) != 1 || fprs[0] != ClaudeReleaseKeyFingerprint {
		t.Fatalf("embedded key fingerprints %v, pin %s", fprs, ClaudeReleaseKeyFingerprint)
	}
}

// showFingerprintsOf stages key bytes and lists their primary fingerprints.
func (v *GPGVerifier) showFingerprintsOf(t *testing.T, key []byte) ([]string, error) {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/key.asc"
	if err := writeTestFile(path, key); err != nil {
		t.Fatal(err)
	}
	return v.showFingerprints(context.Background(), dir, path)
}

func TestGPGVerifierGoodBadAndWrongKey(t *testing.T) {
	toolinstalltest.RequireGPG(t)
	v, err := NewGPGVerifier(context.Background(), exec.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v.Describe(), "openpgp/gpg (GnuPG") {
		t.Fatalf("describe %q", v.Describe())
	}
	key := toolinstalltest.GenerateKey(t, "Fixture <fixture@olivares.invalid>")
	data := []byte(`{"version":"2.1.261"}`)
	sig := key.Sign(t, data)
	rep, err := v.Verify(context.Background(), key.Public, key.Fingerprint, sig, data)
	if err != nil {
		t.Fatalf("good signature refused: %v", err)
	}
	if rep.PrimaryFingerprint != key.Fingerprint || rep.SigningFingerprint == "" || rep.Created.IsZero() {
		t.Fatalf("report %+v", rep)
	}
	// Altered data.
	_, err = v.Verify(context.Background(), key.Public, key.Fingerprint, sig, append(data, ' '))
	requireKind(t, err, KindSignatureInvalid)
	// Signature by another key while the pinned key is the fixture's.
	other := toolinstalltest.GenerateKey(t, "Other <other@olivares.invalid>")
	_, err = v.Verify(context.Background(), key.Public, key.Fingerprint, other.Sign(t, data), data)
	requireKind(t, err, KindSignatureInvalid)
	// Pinned fingerprint that does not match the supplied key material.
	_, err = v.Verify(context.Background(), key.Public, other.Fingerprint, sig, data)
	requireKind(t, err, KindVerificationUnavailable)
	// Garbage signature bytes.
	_, err = v.Verify(context.Background(), key.Public, key.Fingerprint, []byte("not a signature"), data)
	requireKind(t, err, KindSignatureInvalid)
	// Malformed pin.
	_, err = v.Verify(context.Background(), key.Public, "abc", sig, data)
	requireKind(t, err, KindVerificationUnavailable)
}

func TestParseVerifyStatusPolicy(t *testing.T) {
	const pin = "31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE"
	valid := "[GNUPG:] GOODSIG BAA929FF1A7ECACE Anthropic\n[GNUPG:] VALIDSIG " + pin + " 2026-09-04 1788541337 0 4 0 1 10 00 " + pin + "\n[GNUPG:] TRUST_UNDEFINED 0 pgp\n"
	rep, err := parseVerifyStatus([]byte(valid), pin)
	if err != nil {
		t.Fatal(err)
	}
	if rep.PrimaryFingerprint != pin || rep.SigningFingerprint != pin || rep.Created.Unix() != 1788541337 || rep.HashAlgo != "openpgp-hash-10" {
		t.Fatalf("report %+v", rep)
	}
	// A subkey signature reports both fingerprints.
	sub := strings.Replace(valid, "VALIDSIG "+pin, "VALIDSIG 0000000000000000000000000000000000000001", 1)
	rep, err = parseVerifyStatus([]byte(sub), pin)
	if err != nil || rep.SigningFingerprint != "0000000000000000000000000000000000000001" || rep.PrimaryFingerprint != pin {
		t.Fatalf("subkey report %+v %v", rep, err)
	}
	cases := map[string]string{
		"BADSIG":           "[GNUPG:] BADSIG BAA929FF1A7ECACE X\n",
		"EXPKEYSIG":        valid + "[GNUPG:] EXPKEYSIG BAA929FF1A7ECACE X\n",
		"REVKEYSIG":        valid + "[GNUPG:] REVKEYSIG BAA929FF1A7ECACE X\n",
		"EXPSIG":           valid + "[GNUPG:] EXPSIG BAA929FF1A7ECACE X\n",
		"KEYREVOKED":       valid + "[GNUPG:] KEYREVOKED\n",
		"NO_PUBKEY":        "[GNUPG:] ERRSIG 0123456789ABCDEF 1 10 00 1788541337 9 FPR\n[GNUPG:] NO_PUBKEY 0123456789ABCDEF\n",
		"no VALIDSIG":      "[GNUPG:] GOODSIG BAA929FF1A7ECACE X\n",
		"two signatures":   valid + valid,
		"other primary":    strings.ReplaceAll(valid, "00 "+pin, "00 0000000000000000000000000000000000000002"),
		"NODATA":           "[GNUPG:] NODATA 1\n",
		"truncated status": "[GNUPG:] GOODSIG X Y\n[GNUPG:] VALIDSIG " + pin + " 2026-09-04\n",
	}
	for name, status := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseVerifyStatus([]byte(status), pin)
			requireKind(t, err, KindSignatureInvalid)
		})
	}
}
