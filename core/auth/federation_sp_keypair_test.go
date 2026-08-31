// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// THE WRITE THAT BROKE THE LOGIN (C2).
//
// An SP keypair is ONE credential in two halves: a public cert stored in the clear and a
// private key stored sealed. PutConfigIdP treated them under OPPOSITE rules — the sealed
// key was preserved when the input left it blank ("blank = keep", so editing a config
// never forces re-entering a secret), while the public cert was replaced VERBATIM. Any
// client that did not send the cert therefore blanked it and kept the key.
//
// That state is fatal, not degraded: TestSAMLKeypairHalfIsFatalNotIgnored
// (core/auth/federation) proves the loaders refuse a half-pair with ErrNotConfigured, so
// the provider never builds and SSO through that IdP stops. (Password login is a separate
// path — a TOTAL lockout needs the enterprise policy enforcing require-SSO on top.) And
// the console was exactly such a client: its SAML form had no signing fields at all, so
// saving ANY change from the console blanked the signing cert of every deployment that
// had one.
//
// The console now sends both halves (guarded in core/api and by the SAML save test in
// console.test.tsx), but a guard on ONE client is not a fix. A contrast swept cmd/olivares,
// terraform-provider-olivares, connectors, modules, sdk and clients and found NO other
// writer of this field — the generated SDKs pass JSON through untyped, so an external
// caller of the raw API cannot be ruled out and is the real reason this belongs at the
// write path, not "the CLI does it too".
func TestPutConfigPreservesBothHalvesOfAnSPKeypair(t *testing.T) {
	svc := u4Svc(t, nil) // open build; the cap plays no part here
	ctx, actor := context.Background(), fedTestActor()
	scope := auth.GlobalFederationScope

	full := auth.FederationConfigInput{
		Protocol: auth.ProtocolSAML, Enabled: true,
		SAMLMetadataURL: "https://idp.example/metadata", SAMLEntityID: "sp-entity",
		SAMLACSURL: "https://sp.example/acs", SAMLIDPSSOURL: "https://idp.example/sso",
		SAMLSPCertPEM: "ENC-CERT", SAMLSPKeyPEM: "ENC-KEY",
		SAMLSPSignCertPEM: "SIGN-CERT", SAMLSPSignKeyPEM: "SIGN-KEY",
	}
	view, err := svc.PutConfigIdP(ctx, actor, scope, model.DefaultFederationAlias, full)
	if err != nil {
		t.Fatalf("initial put: %v", err)
	}
	// Control: both keypairs really are stored, so the re-save below is measuring
	// preservation and not an empty starting state.
	if view.SAMLSPSignCertPEM == "" || view.SAMLSPSignKeyHint == "" {
		t.Fatalf("signing keypair did not store (cert=%q hint=%q) — the assertion below would pass vacuously",
			view.SAMLSPSignCertPEM, view.SAMLSPSignKeyHint)
	}
	if view.SAMLSPCertPEM == "" || view.SAMLSPKeyHint == "" {
		t.Fatalf("encryption keypair did not store (cert=%q hint=%q)", view.SAMLSPCertPEM, view.SAMLSPKeyHint)
	}
	signHint, encHint := view.SAMLSPSignKeyHint, view.SAMLSPKeyHint

	// THE REGRESSION: a legitimate edit that leaves every SP credential field blank —
	// which is precisely what a client that does not know about the fields sends, and
	// what the console sent for the signing pair on every single save.
	edit := full
	edit.SAMLSPCertPEM, edit.SAMLSPKeyPEM = "", ""
	edit.SAMLSPSignCertPEM, edit.SAMLSPSignKeyPEM = "", ""
	edit.SAMLEmailAttr = "mail" // a real change, so this is a normal edit and not a no-op

	view, err = svc.PutConfigIdP(ctx, actor, scope, model.DefaultFederationAlias, edit)
	if err != nil {
		t.Fatalf("edit put: %v", err)
	}
	if view.SAMLEmailAttr != "mail" {
		t.Fatalf("the edit did not apply (email attr = %q); this test is not exercising a real write", view.SAMLEmailAttr)
	}

	for _, c := range []struct {
		what, cert, hint, wantHint string
	}{
		{"signing", view.SAMLSPSignCertPEM, view.SAMLSPSignKeyHint, signHint},
		{"encryption", view.SAMLSPCertPEM, view.SAMLSPKeyHint, encHint},
	} {
		if c.hint != c.wantHint {
			t.Errorf("%s KEY was not preserved across a blank edit (hint %q -> %q)", c.what, c.wantHint, c.hint)
		}
		if c.cert == "" {
			t.Errorf("%s CERT was blanked by an edit that left it empty, while its private key survived.\n"+
				"That is the half-pair: core/auth/federation/saml.go enters the branch on `cert != \"\" || key != \"\"`\n"+
				"and then refuses the missing half with ErrNotConfigured, so the provider fails to build and LOGIN STOPS.\n"+
				"Both halves must obey the same 'blank = keep' rule; clearing a keypair is DeleteConfigIdP's job.", c.what)
		}
	}
}

// TestTestConfigResolvesTheStoredCertToo covers the OTHER site of the same asymmetry.
// TestConfigIdP builds a provider from the candidate config without persisting it, and it
// resolved the sealed KEY from the store while taking the CERT verbatim from the input —
// so "Test connection" failed with ErrNotConfigured on a perfectly good stored keypair,
// pointing the operator at their IdP for a defect on our side. It is a separate code path
// from PutConfigIdP and was fixed separately; without this case, fixing one and not the
// other stays green.
func TestTestConfigResolvesTheStoredCertToo(t *testing.T) {
	// A CAPTURING builder, because what matters is the params the provider is built FROM.
	// Asserting only that TestConfigIdP returned nil would prove nothing here: the stub
	// builder accepts a half-pair that the real SAML loaders reject, so the "pass" would
	// come from the double being unable to reproduce what production does.
	var got auth.FederationParams
	capturing := func(_ context.Context, p auth.FederationParams) (auth.Federation, error) {
		got = p
		return &fedTestFed{proto: p.Protocol}, nil
	}
	svc := auth.NewFederationService(testStore(t), fedTestSealer{}, capturing, auth.NoFederation{}, nil)
	ctx, actor := context.Background(), fedTestActor()
	scope := auth.GlobalFederationScope

	stored := auth.FederationConfigInput{
		Protocol: auth.ProtocolSAML, Enabled: true,
		SAMLMetadataURL: "https://idp.example/metadata", SAMLEntityID: "sp-entity",
		SAMLACSURL: "https://sp.example/acs", SAMLIDPSSOURL: "https://idp.example/sso",
		SAMLSPSignCertPEM: "SIGN-CERT", SAMLSPSignKeyPEM: "SIGN-KEY",
	}
	if _, err := svc.PutConfigIdP(ctx, actor, scope, model.DefaultFederationAlias, stored); err != nil {
		t.Fatalf("seed put: %v", err)
	}

	// The console's "Test" sends the form as-is: the secret blank (it is never returned)
	// and, before the fix, the cert blank too.
	probe := stored
	probe.SAMLSPSignCertPEM, probe.SAMLSPSignKeyPEM = "", ""

	if err := svc.TestConfigIdP(ctx, scope, model.DefaultFederationAlias, probe); err != nil {
		t.Fatalf("test config: %v", err)
	}
	if got.SAMLSPSignKeyPEM == "" {
		t.Fatalf("TestConfigIdP resolved no signing key from the store, so the cert assertion below " +
			"cannot mean anything (there would be no half-pair either way)")
	}
	if got.SAMLSPSignCertPEM == "" {
		t.Fatalf("TestConfigIdP built the provider with an EMPTY signing cert while resolving the stored "+
			"key (%q): that is the same half-pair, and against the REAL SAML builder it is ErrNotConfigured "+
			"— the operator sees \"Test\" fail on a config that is fine", got.SAMLSPSignKeyPEM)
	}
}

// TestPutConfigRefusesAResolvedHalfPair covers what "blank = keep" alone does NOT.
//
// Keeping both halves on the same rule removes the OMISSION route, but a contrast pointed
// out the state is still reachable BY CONSTRUCTION: a brand-new IdP supplying one half, or
// a rotation that replaces one half of a stored pair. Nothing validated the resolved row,
// and PutConfigIdP persists without ever building a provider — so the write succeeded and
// the login died later, which is exactly the shape of failure this session exists to stop.
//
// validateFederationInput already PROMISED this ("a config can never be stored active yet
// fail to build") while checking only the four SAML URLs. Now the promise is kept for key
// material too, on the resolved row, where the sealed half is known.
func TestPutConfigRefusesAResolvedHalfPair(t *testing.T) {
	ctx, actor := context.Background(), fedTestActor()
	scope := auth.GlobalFederationScope
	base := auth.FederationConfigInput{
		Protocol: auth.ProtocolSAML, Enabled: true,
		SAMLMetadataURL: "https://idp.example/metadata", SAMLEntityID: "sp-entity",
		SAMLACSURL: "https://sp.example/acs", SAMLIDPSSOURL: "https://idp.example/sso",
	}

	// Control: with NEITHER half, the config is valid — both SP keypairs are optional and
	// SAML works unsigned. Without this, a check that refused everything would pass the
	// two cases below and quietly break every unsigned deployment.
	if _, err := u4Svc(t, nil).PutConfigIdP(ctx, actor, scope, model.DefaultFederationAlias, base); err != nil {
		t.Fatalf("a SAML config with no SP keypairs must be valid (SAML works unsigned): %v", err)
	}

	for _, tc := range []struct {
		name string
		in   auth.FederationConfigInput
	}{
		{"new IdP: signing cert without its key", func() auth.FederationConfigInput {
			c := base
			c.SAMLSPSignCertPEM = "SIGN-CERT"
			return c
		}()},
		{"new IdP: signing key without its cert", func() auth.FederationConfigInput {
			c := base
			c.SAMLSPSignKeyPEM = "SIGN-KEY"
			return c
		}()},
		{"new IdP: encryption cert without its key", func() auth.FederationConfigInput {
			c := base
			c.SAMLSPCertPEM = "ENC-CERT"
			return c
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := u4Svc(t, nil).PutConfigIdP(ctx, actor, scope, model.DefaultFederationAlias, tc.in)
			if err == nil {
				t.Fatal("a half keypair was STORED. It cannot build (core/auth/federation/saml.go enters on " +
					"`cert != \"\" || key != \"\"` and refuses the missing half), so this row saves cleanly and " +
					"takes SSO down at the next resolve — the write must refuse it instead.")
			}
			if !errors.Is(err, auth.ErrBadFederationConfig) {
				t.Fatalf("half-pair rejected with %v, want ErrBadFederationConfig (the API maps it to 400; "+
					"another class would surface as a 500 the operator cannot act on)", err)
			}
		})
	}
}
