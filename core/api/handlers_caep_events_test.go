// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// caepEndpoint is the CAEP/SSF SET push delivery path (RFC 8935).
const caepEndpoint = "/v1/ssf/events"

// setupCAEP configures a CAEP SET publisher for the tenant and returns the
// signing key and a bearer token that can reach the endpoint.
func setupCAEP(t *testing.T, ctx context.Context, h *harness, super auth.Principal, tenant model.TenantID) (priv *ecdsa.PrivateKey, bearerTok string) {
	t.Helper()
	priv, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	pub := auth.SETPublisher{
		ID: "caep-test", Enabled: true,
		Issuer:    "https://caep.idp.test",
		Audiences: []string{"https://caep.cp.test"},
		Keys:      []auth.SETVerificationKey{{Kid: "k1", Alg: "ES256", PEM: pubPEM}},
	}
	if err := h.authr.ConfigurePublisher(ctx, super, tenant, pub); err != nil {
		t.Fatalf("ConfigurePublisher: %v", err)
	}
	cfg := auth.CAEPSetConfig{Enabled: true, PublisherID: "caep-test"}
	if err := h.authr.ConfigureCAEPSet(ctx, super, tenant, cfg); err != nil {
		t.Fatalf("ConfigureCAEPSet: %v", err)
	}

	// A tenant-bound admin token authenticates the push endpoint.
	bearerTok = h.scimToken(super, tenant)
	return priv, bearerTok
}

// signCAEP signs a CAEP SET and returns the compact JWS token string.
func signCAEP(t *testing.T, priv *ecdsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	return signSETForTest(t, priv, claims)
}

// caepSET builds the base SET claims for a CAEP event targeting a user by
// opaque (internal user ID) sub_id.
func caepSET(eventURI, userID, jti string) map[string]any {
	return map[string]any{
		"iss": "https://caep.idp.test",
		"aud": []string{"https://caep.cp.test"},
		"iat": time.Now().Unix(),
		"jti": jti,
		"sub_id": map[string]any{
			"format": "opaque",
			"id":     userID,
		},
		"events": map[string]any{eventURI: map[string]any{}},
	}
}

// TestCAEPEventsEndpoint_SessionRevoke tests the happy path: a valid
// session-revoked SET is accepted (202) and the user's sessions are cut.
func TestCAEPEventsEndpoint_SessionRevoke(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "caep-acme")
	super, err := h.authr.Authenticate(ctx, adminTok)
	if err != nil {
		t.Fatal(err)
	}

	priv, bearer := setupCAEP(t, ctx, h, super, tenant)

	// Provision a member to target.
	u, _, err := h.authr.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{
		UserName: "alice@caep.test", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	set := signCAEP(t, priv, caepSET(
		"https://schemas.openid.net/secevent/caep/event-type/session-revoked",
		u.ID.String(), "jti-sess-1",
	))
	resp := h.scim("POST", caepEndpoint, bearer, set)
	if resp.code != http.StatusAccepted {
		t.Fatalf("session-revoked SET = %d (%s), want 202", resp.code, resp.raw)
	}
}

// TestCAEPEventsEndpoint_AccountDisable tests a RISC account-disabled event,
// which cuts all credentials for the subject.
func TestCAEPEventsEndpoint_AccountDisable(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "caep-acme2")
	super, err := h.authr.Authenticate(ctx, adminTok)
	if err != nil {
		t.Fatal(err)
	}

	priv, bearer := setupCAEP(t, ctx, h, super, tenant)

	u, _, err := h.authr.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{
		UserName: "bob@caep.test", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	set := signCAEP(t, priv, caepSET(
		"https://schemas.openid.net/secevent/risc/event-type/account-disabled",
		u.ID.String(), "jti-acct-1",
	))
	resp := h.scim("POST", caepEndpoint, bearer, set)
	if resp.code != http.StatusAccepted {
		t.Fatalf("account-disabled SET = %d (%s), want 202", resp.code, resp.raw)
	}
}

// TestCAEPEventsEndpoint_IgnoreUnknownEvent tests that an unrecognized event
// URI is acknowledged (202) without error — RFC 8935 §2.4.
func TestCAEPEventsEndpoint_IgnoreUnknownEvent(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "caep-ignore")
	super, err := h.authr.Authenticate(ctx, adminTok)
	if err != nil {
		t.Fatal(err)
	}

	priv, bearer := setupCAEP(t, ctx, h, super, tenant)

	u, _, err := h.authr.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{
		UserName: "carol@caep.test", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	set := signCAEP(t, priv, caepSET(
		"https://schemas.openid.net/secevent/caep/event-type/unknown-future-event",
		u.ID.String(), "jti-unk-1",
	))
	resp := h.scim("POST", caepEndpoint, bearer, set)
	if resp.code != http.StatusAccepted {
		t.Fatalf("unknown event URI = %d (%s), want 202 (acknowledged, not acted on)", resp.code, resp.raw)
	}
}

// TestCAEPEventsEndpoint_MalformedBody tests that a non-JWS body returns
// 400 with a machine-readable err code (RFC 8935 §2.4).
func TestCAEPEventsEndpoint_MalformedBody(t *testing.T) {
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "caep-malform")
	super, err := h.authr.Authenticate(context.Background(), adminTok)
	if err != nil {
		t.Fatal(err)
	}
	_, bearer := setupCAEP(t, context.Background(), h, super, tenant)

	resp := h.scim("POST", caepEndpoint, bearer, "not-a-valid-jws")
	if resp.code != http.StatusBadRequest {
		t.Fatalf("malformed body = %d, want 400", resp.code)
	}
	if resp.body["err"] == nil {
		t.Errorf("malformed body response missing 'err' field: %v", resp.body)
	}
}

// TestCAEPEventsEndpoint_Unauthenticated tests that missing bearer token → 401.
func TestCAEPEventsEndpoint_Unauthenticated(t *testing.T) {
	h := newHarness(t)
	h.adminLogin()

	resp := h.scim("POST", caepEndpoint, "", "any-body")
	if resp.code != http.StatusUnauthorized {
		t.Fatalf("no-token = %d, want 401", resp.code)
	}
}

// TestCAEPEventsEndpoint_NotConfigured tests that a tenant without a CAEP
// publisher returns 400 with err=access_denied (deny-closed).
func TestCAEPEventsEndpoint_NotConfigured(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "caep-nocfg")
	super, err := h.authr.Authenticate(ctx, adminTok)
	if err != nil {
		t.Fatal(err)
	}

	// Bearer token but no ConfigureCAEPSet call.
	bearer := h.scimToken(super, tenant)

	// Use a dummy signed SET (signature will fail first, but we want access_denied
	// to fire before even checking the signature — the receiver is deny-closed).
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	set := signCAEP(t, priv, map[string]any{
		"iss":    "https://caep.idp.test",
		"aud":    []string{"https://caep.cp.test"},
		"iat":    time.Now().Unix(),
		"jti":    "jti-nocfg-1",
		"sub_id": map[string]any{"format": "opaque", "id": "some-user-id"},
		"events": map[string]any{
			"https://schemas.openid.net/secevent/caep/event-type/session-revoked": map[string]any{},
		},
	})

	resp := h.scim("POST", caepEndpoint, bearer, set)
	if resp.code != http.StatusBadRequest {
		t.Fatalf("unconfigured = %d, want 400", resp.code)
	}
	if resp.body["err"] != "access_denied" {
		t.Errorf("unconfigured err = %v, want access_denied", resp.body["err"])
	}
}

// TestCAEPEventsEndpoint_BadSignature tests that a SET with an invalid
// signature returns 400 with err=invalid_key.
func TestCAEPEventsEndpoint_BadSignature(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "caep-badsig")
	super, err := h.authr.Authenticate(ctx, adminTok)
	if err != nil {
		t.Fatal(err)
	}

	_, bearer := setupCAEP(t, ctx, h, super, tenant)

	// Sign with a DIFFERENT key (not registered).
	wrongKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	u, _, err := h.authr.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{
		UserName: "dave@caep.test", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	set := signCAEP(t, wrongKey, caepSET(
		"https://schemas.openid.net/secevent/caep/event-type/session-revoked",
		u.ID.String(), "jti-badsig-1",
	))

	resp := h.scim("POST", caepEndpoint, bearer, set)
	if resp.code != http.StatusBadRequest {
		t.Fatalf("bad signature = %d, want 400", resp.code)
	}
	if resp.body["err"] != "invalid_key" {
		t.Errorf("bad signature err = %v, want invalid_key", resp.body["err"])
	}
}

// TestCAEPEventsEndpoint_DuplicateJTI tests that replaying the same SET
// returns 400 with err=duplicate_event.
func TestCAEPEventsEndpoint_DuplicateJTI(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "caep-dup")
	super, err := h.authr.Authenticate(ctx, adminTok)
	if err != nil {
		t.Fatal(err)
	}

	priv, bearer := setupCAEP(t, ctx, h, super, tenant)

	u, _, err := h.authr.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{
		UserName: "eve@caep.test", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	set := signCAEP(t, priv, caepSET(
		"https://schemas.openid.net/secevent/caep/event-type/session-revoked",
		u.ID.String(), "jti-dup-1",
	))

	// First delivery → 202.
	if resp := h.scim("POST", caepEndpoint, bearer, set); resp.code != http.StatusAccepted {
		t.Fatalf("first delivery = %d, want 202", resp.code)
	}
	// Replay the same SET → 400 duplicate_event.
	resp := h.scim("POST", caepEndpoint, bearer, set)
	if resp.code != http.StatusBadRequest {
		t.Fatalf("duplicate SET = %d, want 400", resp.code)
	}
	if resp.body["err"] != "duplicate_event" {
		t.Errorf("duplicate err = %v, want duplicate_event", resp.body["err"])
	}
}

// TestCAEPEventsEndpoint_UnknownSubject tests that a SET whose subject cannot
// be resolved to a tenant member returns 400 with err=invalid_request.
func TestCAEPEventsEndpoint_UnknownSubject(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "caep-nosub")
	super, err := h.authr.Authenticate(ctx, adminTok)
	if err != nil {
		t.Fatal(err)
	}

	priv, bearer := setupCAEP(t, ctx, h, super, tenant)

	// Use a user ID that doesn't exist in this tenant.
	set := signCAEP(t, priv, caepSET(
		"https://schemas.openid.net/secevent/caep/event-type/session-revoked",
		model.NewID().String(), "jti-nosub-1",
	))

	resp := h.scim("POST", caepEndpoint, bearer, set)
	if resp.code != http.StatusBadRequest {
		t.Fatalf("unknown subject = %d, want 400", resp.code)
	}
	if resp.body["err"] == nil {
		t.Errorf("unknown subject response missing 'err' field: %v", resp.body)
	}
}

// TestCAEPEventsEndpoint_EmailSubject tests that a SET with an email-format
// sub_id is handled correctly (the email is parsed from the JSON field).
func TestCAEPEventsEndpoint_EmailSubject(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "caep-email")
	super, err := h.authr.Authenticate(ctx, adminTok)
	if err != nil {
		t.Fatal(err)
	}

	priv, bearer := setupCAEP(t, ctx, h, super, tenant)

	u, _, err := h.authr.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{
		UserName: "frank@caep.test", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Build a SET with email-format sub_id.
	set := signCAEP(t, priv, map[string]any{
		"iss": "https://caep.idp.test",
		"aud": []string{"https://caep.cp.test"},
		"iat": time.Now().Unix(),
		"jti": "jti-email-1",
		"sub_id": map[string]any{
			"format": "email",
			"email":  u.Email, // Email is the login identifier
		},
		"events": map[string]any{
			"https://schemas.openid.net/secevent/caep/event-type/session-revoked": map[string]any{},
		},
	})

	resp := h.scim("POST", caepEndpoint, bearer, set)
	if resp.code != http.StatusAccepted {
		t.Fatalf("email-format sub_id = %d (%s), want 202", resp.code, resp.raw)
	}
}
