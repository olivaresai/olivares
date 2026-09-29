// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperschema

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelperSpool_FilenameIsDerivedFromTheNonceTheHelperIssued(t *testing.T) {
	var key, other SpoolKey
	copy(key[:], "the helper's own spool key, 32 b")
	copy(other[:], "another helper's spool key, 32 b")
	random := bytes.NewReader(bytes.Repeat([]byte{0x5a}, 16*4))

	nonce, err := IssueNonce(key, random)
	if err != nil {
		t.Fatal(err)
	}
	if !nonceShape(string(nonce)) {
		t.Fatalf("an issued nonce %q is not 64 lowercase hexadecimal digits", nonce)
	}

	t.Run("the name is the issued nonce and the kind, with no directory", func(t *testing.T) {
		name, err := key.SpoolName(string(nonce), SpoolBundle)
		if err != nil {
			t.Fatalf("a nonce this helper issued was refused: %v", err)
		}
		if name != string(nonce)+".bundle.json" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
			t.Fatalf("derived %q, want the nonce and the kind with no directory", name)
		}
	})

	t.Run("a nonce the helper did not issue names nothing", func(t *testing.T) {
		foreign, err := IssueNonce(other, random)
		if err != nil {
			t.Fatal(err)
		}
		for name, candidate := range map[string]string{
			"issued under another key":   string(foreign),
			"random digits":              strings.Repeat("0123456789abcdef", 4),
			"the issued nonce, one flip": flipLast(string(nonce)),
		} {
			if got, err := key.SpoolName(candidate, SpoolBundle); err == nil {
				t.Errorf("%s derived %q", name, got)
			}
		}
	})

	t.Run("a caller supplies a nonce, never a name", func(t *testing.T) {
		for _, candidate := range []string{
			string(nonce) + ".bundle.json",
			"bundle.json",
			"../../etc/DO-NOT-PRINT",
			"/var/lib/olivares-support-bundle/" + string(nonce) + ".bundle.json",
			string(nonce) + "/../x",
			strings.ToUpper(string(nonce)),
			"",
		} {
			got, err := key.SpoolName(candidate, SpoolBundle)
			if err == nil {
				t.Errorf("%q derived %q", candidate, got)
				continue
			}
			if strings.Contains(err.Error(), "DO-NOT-PRINT") {
				t.Errorf("the refusal repeats the value: %v", err)
			}
		}
	})

	t.Run("the kind is closed", func(t *testing.T) {
		if got, err := key.SpoolName(string(nonce), SpoolKind("../pem")); err == nil {
			t.Errorf("an unknown kind derived %q", got)
		}
		// The certificate helper's spool keeps the portal's certificate under the nonce, <nonce>.pem.
		if got, err := key.SpoolName(string(nonce), SpoolCertificate); err != nil || got != string(nonce)+".pem" {
			t.Errorf("the certificate kind derived %q (%v), want the nonce and .pem", got, err)
		}
	})

	t.Run("each issue is a new nonce", func(t *testing.T) {
		fresh := bytes.NewReader([]byte("0123456789abcdefFEDCBA9876543210"))
		first, err := IssueNonce(key, fresh)
		if err != nil {
			t.Fatal(err)
		}
		second, err := IssueNonce(key, fresh)
		if err != nil || first == second {
			t.Fatalf("two issues gave %q and %q (%v)", first, second, err)
		}
		if _, err := IssueNonce(key, bytes.NewReader(nil)); err == nil {
			t.Fatal("a nonce was issued without random bytes")
		}
	})
}

// flipLast returns nonce with its last hexadecimal digit changed.
func flipLast(nonce string) string {
	last := nonce[len(nonce)-1]
	replacement := byte('0')
	if last == '0' {
		replacement = '1'
	}
	return nonce[:len(nonce)-1] + string([]byte{replacement})
}
