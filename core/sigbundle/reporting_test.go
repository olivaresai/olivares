// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sigbundle

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

func TestReportingDomainSignatures(t *testing.T) {
	if TagReporting != "olivares.reporting.v1\n" {
		t.Fatal("reporting signing domain changed without a version change")
	}
	pub, priv := testKey(t)
	otherPub, _ := testKey(t)
	payload := []byte(`{"report":"sample","version":1}`)
	input := append([]byte("olivares.reporting.v1\n"), payload...)
	if !bytes.Equal(SigningInput(TagReporting, payload), input) {
		t.Fatal("report signing does not cover the exact domain and payload bytes")
	}
	signature := Sign(TagReporting, payload, priv)
	if err := Verify(TagReporting, payload, signature, pub); err != nil {
		t.Fatalf("report signature round trip: %v", err)
	}
	tamperedPayload := append([]byte(nil), payload...)
	tamperedPayload[0] ^= 1
	tamperedSignature := append([]byte(nil), signature...)
	tamperedSignature[0] ^= 1
	for _, tc := range []struct {
		name               string
		payload, signature []byte
		pub                ed25519.PublicKey
		want               error
	}{
		{"changed payload", tamperedPayload, signature, pub, ErrBadSignature},
		{"changed signature", payload, tamperedSignature, pub, ErrBadSignature},
		{"wrong key", payload, signature, otherPub, ErrBadSignature},
		{"missing key", payload, signature, nil, ErrNoKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Verify(TagReporting, tc.payload, tc.signature, tc.pub); err != tc.want {
				t.Fatalf("report verification = %v, want %v", err, tc.want)
			}
		})
	}
	for _, tag := range Tags {
		if tag == TagReporting {
			continue
		}
		t.Run(tag, func(t *testing.T) {
			if err := Verify(tag, payload, signature, pub); err != ErrBadSignature {
				t.Fatalf("report signature accepted by another domain: %v", err)
			}
			if err := Verify(TagReporting, payload, Sign(tag, payload, priv), pub); err != ErrBadSignature {
				t.Fatalf("other domain signature accepted as a report: %v", err)
			}
		})
	}
	if err := Verify("olivares.reporting.v2\n", payload, signature, pub); err != ErrUnknownTag {
		t.Fatalf("unregistered report version accepted: %v", err)
	}
}
