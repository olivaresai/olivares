// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package scim

import (
	"bytes"
	"testing"
)

// FuzzParseCompactJWS exercises the compact JWS/JWT splitter on hostile SSF/CAEP
// Security Event Tokens. It runs on UNTRUSTED bytes before any signature
// verification, so it must never panic; and on the success path the signing input
// it reports must be exactly the "header.payload" prefix the signature covers —
// a regression there would let a signature be checked over the wrong bytes.
func FuzzParseCompactJWS(f *testing.F) {
	f.Add([]byte("eyJhbGciOiJFZERTQSJ9.eyJpc3MiOiJ4In0.c2ln"))
	f.Add([]byte("a.b.c"))
	f.Add([]byte(".."))
	f.Add([]byte("only-one-part"))
	f.Add([]byte(""))
	f.Add([]byte("=.=.="))
	f.Add([]byte("  eyJhIjoxfQ.eyJiIjoyfQ.zzz  "))

	f.Fuzz(func(t *testing.T, tok []byte) {
		hdr, payload, signingInput, sig, err := ParseCompactJWS(tok)
		if err != nil {
			return
		}
		_, _ = hdr, sig
		// Success implies exactly three non-empty dot segments over the trimmed input,
		// and signingInput == segment0 + "." + segment1.
		parts := bytes.Split(bytes.TrimSpace(tok), []byte("."))
		if len(parts) != 3 {
			t.Fatalf("accepted a token that is not exactly 3 dot-segments: %d", len(parts))
		}
		want := append(append(append([]byte{}, parts[0]...), '.'), parts[1]...)
		if !bytes.Equal(signingInput, want) {
			t.Fatalf("signingInput %q != header.payload %q", signingInput, want)
		}
		// The claims decoder must also survive the decoded payload without panicking.
		_, _ = DecodeSET(payload)
	})
}
