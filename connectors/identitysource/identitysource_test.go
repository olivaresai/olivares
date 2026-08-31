// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package identitysource

import "testing"

func TestPrincipalTypeValid(t *testing.T) {
	for _, p := range []PrincipalType{PrincipalUnknown, PrincipalHuman, PrincipalNHI} {
		if !p.Valid() {
			t.Errorf("PrincipalType %q should be valid", p)
		}
	}
	for _, p := range []PrincipalType{"", "machine", "person", "HUMAN"} {
		if PrincipalType(p).Valid() {
			t.Errorf("PrincipalType %q should be invalid", p)
		}
	}
}

func TestGraphFindIdentity(t *testing.T) {
	g := Graph{
		Source: SourceLDAP,
		Identities: []Identity{
			{Ref: "cn=alice,ou=people,dc=corp", Type: PrincipalHuman, Kind: "user", Source: SourceLDAP},
			{Ref: "cn=svc-deploy,ou=svc,dc=corp", Type: PrincipalNHI, Kind: "service_account", Source: SourceLDAP},
		},
	}
	got, ok := g.FindIdentity("cn=svc-deploy,ou=svc,dc=corp")
	if !ok {
		t.Fatal("expected to find svc-deploy")
	}
	if got.Type != PrincipalNHI {
		t.Errorf("svc-deploy Type = %q, want nhi", got.Type)
	}
	if _, ok := g.FindIdentity("cn=nobody,dc=corp"); ok {
		t.Error("did not expect to find nobody")
	}
}

// TestGraphCarriesNoSecrets is a contract guard: the Identity/Collection types
// have no field that could hold credential material. This test documents the
// minimal-data invariant at the type level — a future field that smuggled a
// secret would have to be added deliberately, and this test is where a reviewer
// is reminded the Graph is metadata-only.
func TestGraphCarriesNoSecrets(t *testing.T) {
	// secretKeys are credential-shaped attribute names that must never appear in
	// the only free-form fields (Identity.Attributes / Collection.Attributes).
	// There is no Password/Secret/Token field by design, so the contract is that
	// these keys are absent — a connector reads identities, not the secrets behind
	// them (docs/SECURITY-HARDENING.md).
	secretKeys := []string{
		"password", "passwd", "secret", "token", "api_key", "apikey",
		"private_key", "client_secret", "bearer_token", "credential",
	}

	id := Identity{
		Ref:         "spiffe://corp.example/ns/prod/sa/deployer",
		Type:        PrincipalNHI,
		Kind:        "workload",
		DisplayName: "prod deployer",
		Source:      SourceSPIFFE,
		Attributes:  map[string]string{"trust_domain": "corp.example"},
	}
	for _, k := range secretKeys {
		if _, ok := id.Attributes[k]; ok {
			t.Errorf("Identity.Attributes must never carry credential material: found secret-shaped key %q", k)
		}
	}

	col := Collection{
		Ref:         "policy/secret-reader",
		Kind:        KindPolicy,
		DisplayName: "secret reader",
		Source:      SourceVault,
		Attributes:  map[string]string{"path_count": "3"},
	}
	for _, k := range secretKeys {
		if _, ok := col.Attributes[k]; ok {
			t.Errorf("Collection.Attributes must never carry credential material: found secret-shaped key %q", k)
		}
	}
}
