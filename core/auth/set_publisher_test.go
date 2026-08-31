// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

func TestValidatePublisherKeysRejectsNoneAndHS(t *testing.T) {
	bad := auth.SETPublisher{
		Keys: []auth.SETVerificationKey{{Alg: "none", PEM: "irrelevant"}},
	}
	if err := auth.ValidatePublisherKeys(bad); err == nil {
		t.Fatal("should reject alg=none")
	}
	hs := auth.SETPublisher{
		Keys: []auth.SETVerificationKey{{Alg: "HS256", PEM: "irrelevant"}},
	}
	if err := auth.ValidatePublisherKeys(hs); err == nil {
		t.Fatal("should reject HS256")
	}
}

func TestValidatePublisherKeysRejectsBadPEM(t *testing.T) {
	bad := auth.SETPublisher{
		Keys: []auth.SETVerificationKey{{Alg: "ES256", PEM: "not-a-pem"}},
	}
	if err := auth.ValidatePublisherKeys(bad); err == nil {
		t.Fatal("should reject malformed PEM")
	}
}

func TestValidatePublisherKeysAcceptsES256(t *testing.T) {
	signer := newES256Signer(t)
	good := auth.SETPublisher{
		Keys: []auth.SETVerificationKey{{Alg: "ES256", PEM: signer.pem}},
	}
	if err := auth.ValidatePublisherKeys(good); err != nil {
		t.Fatalf("valid ES256 key rejected: %v", err)
	}
}

func TestPublisherRegistryCRUD(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	pub := auth.SETPublisher{
		ID: "okta-prod", Enabled: true, Issuer: "https://okta.example.com",
		Audiences: []string{"https://olivares.example.com"},
		Keys:      []auth.SETVerificationKey{{Kid: "k1", Alg: "ES256", PEM: signer.pem}},
	}
	if err := a.ConfigurePublisher(ctx, super, tenant, pub); err != nil {
		t.Fatalf("configure: %v", err)
	}
	got, err := a.PublisherFor(ctx, tenant, "okta-prod")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.Issuer != "https://okta.example.com" {
		t.Fatalf("issuer = %q", got.Issuer)
	}
	if _, err := a.PublisherFor(ctx, tenant, "nonexistent"); !errors.Is(err, auth.ErrSETPublisherNotFound) {
		t.Fatalf("missing publisher err = %v", err)
	}
	if err := a.RemovePublisher(ctx, super, tenant, "okta-prod"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := a.PublisherFor(ctx, tenant, "okta-prod"); !errors.Is(err, auth.ErrSETPublisherNotFound) {
		t.Fatalf("after remove err = %v", err)
	}
}

func TestJTIDuplicateSuppression(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)

	if err := a.CheckJTIDuplicate(ctx, "pub1", "jti-1", 3600); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := a.CheckJTIDuplicate(ctx, "pub1", "jti-1", 3600); !errors.Is(err, auth.ErrSETJTIDuplicate) {
		t.Fatalf("duplicate err = %v", err)
	}
	// Different publisher, same jti is OK
	if err := a.CheckJTIDuplicate(ctx, "pub2", "jti-1", 3600); err != nil {
		t.Fatalf("different publisher: %v", err)
	}
	// Empty jti is always OK (some SETs may omit it)
	if err := a.CheckJTIDuplicate(ctx, "pub1", "", 3600); err != nil {
		t.Fatalf("empty jti: %v", err)
	}
}
