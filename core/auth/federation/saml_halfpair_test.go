// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"errors"
	"testing"
)

// A HALF KEYPAIR IS FATAL, NOT IGNORED (C2).
//
// This is the POSITIVE CONTROL for the invariant PutConfigIdP now maintains
// (core/auth/federation_config.go: both halves of an SP keypair obey "blank = keep").
// Without it, a reader cannot tell whether that invariant is load-bearing or merely tidy,
// and the service-level test that asserts it looks like a preference.
//
// It is load-bearing. samlFromParts enters each keypair branch on
// `certPEM != "" || keyPEM != ""` — an OR, deliberately, so that supplying only one half
// is reported rather than silently ignored. The loader then refuses. The consequence is
// the whole of C2: a config that keeps its sealed private key while its public cert is
// blanked does not build, so ResolveLogin yields NoFederation and NOBODY CAN LOG IN.
//
// The existing keypair tests only ever pass both halves, which is why fifteen production
// readers of SAMLSPSignCertPEM had no test that could go red.
//
// DECLARED LIMIT, measured 2026-08-11 by mutation, not assumed. Weakening the explicit
// `certPEM == "" || keyPEM == ""` guard to `&&` leaves this test GREEN, so that line is
// NOT what this proves. The half-pair is rejected a second time, redundantly, by
// tls.X509KeyPair — witnessed error: "SP signing keypair: tls: failed to find any PEM
// data in certificate input", still wrapped in ErrNotConfigured. The guard buys the
// operator a legible reason, not the refusal.
//
// The test is still exactly right about what it claims — a half-pair IS fatal, by two
// independent mechanisms — and that is the property the write-path invariant rests on.
// But do not read a pass here as "the guard is load-bearing": the property is, the line
// is not. Pinning the message text would make the line load-bearing at the price of a
// test that fails on editorial rewording, which this repo deliberately avoids
// (ssoschema_parity_test.go makes the same trade explicitly).
func TestSAMLKeypairHalfIsFatalNotIgnored(t *testing.T) {
	certPEM, keyPEM := keypairPEM(t, false) // RSA: valid for BOTH roles

	// Prove the fixture is good, so a failure below is about the missing half and not
	// about a malformed certificate.
	if _, _, _, err := loadSigningKeypair(certPEM, keyPEM); err != nil {
		t.Fatalf("fixture keypair is not loadable, so the half-pair cases below prove nothing: %v", err)
	}
	if _, _, err := loadEncryptionKeypair(certPEM, keyPEM); err != nil {
		t.Fatalf("fixture keypair is not loadable for encryption: %v", err)
	}

	for _, tc := range []struct {
		name      string
		cert, key string
		load      func(c, k string) error
		whichHalf string
	}{
		{"signing: key without cert", "", keyPEM, signLoad, "cert"},
		{"signing: cert without key", certPEM, "", signLoad, "key"},
		{"encryption: key without cert", "", keyPEM, encLoad, "cert"},
		{"encryption: cert without key", certPEM, "", encLoad, "key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.load(tc.cert, tc.key)
			if err == nil {
				t.Fatalf("a keypair missing its %s was ACCEPTED. If that is now the intended "+
					"behavior, the 'blank = keep' rule in PutConfigIdP is no longer load-bearing "+
					"and its comment must be rewritten — do not just delete this case.", tc.whichHalf)
			}
			if !errors.Is(err, ErrNotConfigured) {
				t.Fatalf("half keypair error = %v, want ErrNotConfigured (the core resolver maps it to "+
					"NoFederation / login 501; another error class would surface differently)", err)
			}
		})
	}
}

func signLoad(c, k string) error {
	_, _, _, err := loadSigningKeypair(c, k)
	return err
}

func encLoad(c, k string) error {
	_, _, err := loadEncryptionKeypair(c, k)
	return err
}
