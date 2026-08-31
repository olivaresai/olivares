// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// caepSetIssuer / caepSetAud are the CAEP publisher's iss and aud for tests.
const (
	caepSetIssuer = "https://caep.idp.example.test"
	caepSetAud    = "https://caep.olivares.example.test"
)

// caepPublisherID is the shared publisher registry id used in CAEP tests.
const caepPublisherID = "caep-prod"

// enableCAEP configures the shared publisher registry and the CAEP SET config
// for the tenant, pointing at caepPublisherID.
func enableCAEP(t *testing.T, ctx context.Context, a *auth.Authenticator, super auth.Principal, tenant model.TenantID, signer es256Signer) {
	t.Helper()
	pub := auth.SETPublisher{
		ID: caepPublisherID, Enabled: true,
		Issuer:    caepSetIssuer,
		Audiences: []string{caepSetAud},
		Keys:      []auth.SETVerificationKey{{Kid: "k1", Alg: "ES256", PEM: signer.pem}},
	}
	if err := a.ConfigurePublisher(ctx, super, tenant, pub); err != nil {
		t.Fatalf("configure publisher: %v", err)
	}
	cfg := auth.CAEPSetConfig{
		Enabled:     true,
		PublisherID: caepPublisherID,
	}
	if err := a.ConfigureCAEPSet(ctx, super, tenant, cfg); err != nil {
		t.Fatalf("configure caep set: %v", err)
	}
}

// enableCAEPStepUp configures CAEP with DeviceNonCompliantAction = "step_up".
func enableCAEPStepUp(t *testing.T, ctx context.Context, a *auth.Authenticator, super auth.Principal, tenant model.TenantID, signer es256Signer) {
	t.Helper()
	pub := auth.SETPublisher{
		ID: caepPublisherID, Enabled: true,
		Issuer:    caepSetIssuer,
		Audiences: []string{caepSetAud},
		Keys:      []auth.SETVerificationKey{{Kid: "k1", Alg: "ES256", PEM: signer.pem}},
	}
	if err := a.ConfigurePublisher(ctx, super, tenant, pub); err != nil {
		t.Fatalf("configure publisher: %v", err)
	}
	cfg := auth.CAEPSetConfig{
		Enabled:                  true,
		PublisherID:              caepPublisherID,
		DeviceNonCompliantAction: "step_up",
	}
	if err := a.ConfigureCAEPSet(ctx, super, tenant, cfg); err != nil {
		t.Fatalf("configure caep set: %v", err)
	}
}

// caepEvent builds a SET claim set for a CAEP/RISC event targeting a user by
// their internal ID (opaque sub_id format).
func caepEvent(eventURI, userID, jti string) map[string]any {
	return map[string]any{
		"iss": caepSetIssuer,
		"aud": []string{caepSetAud},
		"iat": time.Now().Unix(),
		"jti": jti,
		"sub_id": map[string]any{
			"format": "opaque",
			"id":     userID,
		},
		"events": map[string]any{eventURI: map[string]any{}},
	}
}

// caepEventByEmail builds a SET with an email-format sub_id.
func caepEventByEmail(eventURI, email, jti string) map[string]any {
	return map[string]any{
		"iss": caepSetIssuer,
		"aud": []string{caepSetAud},
		"iat": time.Now().Unix(),
		"jti": jti,
		"sub_id": map[string]any{
			"format": "email",
			"email":  email,
		},
		"events": map[string]any{eventURI: map[string]any{}},
	}
}

// caepEnvFromSET parses a compact JWS into a CAEPEventEnvelope: splits the
// header/payload/signature, extracts subject fields, and derives the action.
func caepEnvFromSET(t *testing.T, token []byte, action auth.CAEPEventAction) auth.CAEPEventEnvelope {
	t.Helper()
	b64 := base64.RawURLEncoding

	parts := splitJWS(t, token)
	hdrBytes, err := b64.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	payloadBytes, err := b64.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}

	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hdrBytes, &hdr); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}

	var claims struct {
		Iss   string          `json:"iss"`
		Aud   []string        `json:"aud"`
		IAT   int64           `json:"iat"`
		JTI   string          `json:"jti"`
		SubID json.RawMessage `json:"sub_id"`
	}
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	// Decode sub_id to extract subject fields.
	var subID struct {
		Format string `json:"format"`
		ID     string `json:"id"`    // opaque
		Email  string `json:"email"` // email
		Sub    string `json:"sub"`   // iss_sub
	}
	if len(claims.SubID) > 0 {
		_ = json.Unmarshal(claims.SubID, &subID)
	}

	signingInput := []byte(parts[0] + "." + parts[1])

	env := auth.CAEPEventEnvelope{
		SETEnvelope: auth.SETEnvelope{
			Alg:          hdr.Alg,
			Kid:          hdr.Kid,
			SigningInput: signingInput,
			Signature:    sig,
			Issuer:       claims.Iss,
			Audience:     claims.Aud,
			JTI:          claims.JTI,
			IssuedAt:     claims.IAT,
		},
		Action: action,
	}
	switch subID.Format {
	case "email":
		env.SubjectEmail = subID.Email
	case "iss_sub":
		env.SubjectExternalID = subID.Sub
	case "opaque":
		env.SubjectUserID = subID.ID
	}
	return env
}

// splitJWS splits a compact JWS (a.b.c) into its three parts.
func splitJWS(t *testing.T, token []byte) [3]string {
	t.Helper()
	var parts [3]string
	i, j := 0, 0
	p := 0
	for k, b := range token {
		if b == '.' {
			if p >= 2 {
				t.Fatalf("too many dots in JWS")
			}
			parts[p] = string(token[i:k])
			p++
			i = k + 1
			j = k
		}
	}
	_ = j
	parts[2] = string(token[i:])
	return parts
}

// --- Tests ---

func TestCAEPReceiverDenyClosedWhenUnconfigured(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "user@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	env := caepEnvFromSET(t, signer.signSET(t, "k1", caepEvent(
		"https://schemas.openid.net/secevent/caep/event-type/session-revoked",
		u.ID.String(), "j1",
	)), auth.CAEPSessionRevoke)

	// No ConfigureCAEPSet call → deny-closed.
	if _, err := a.CAEPReceiveEvent(ctx, super, tenant, env); !errors.Is(err, auth.ErrCAEPSetDisabled) {
		t.Fatalf("unconfigured = %v, want ErrCAEPSetDisabled", err)
	}
}

func TestCAEPReceiverSessionRevoke(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "alice@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	sessTok, apiTok := mintUserCreds(t, st, u.ID, tenant)
	enableCAEP(t, ctx, a, super, tenant, signer)

	res, err := a.CAEPReceiveEvent(ctx, super, tenant, caepEnvFromSET(t, signer.signSET(t, "k1", caepEvent(
		"https://schemas.openid.net/secevent/caep/event-type/session-revoked",
		u.ID.String(), "j1",
	)), auth.CAEPSessionRevoke))
	if err != nil {
		t.Fatalf("session revoke = %v", err)
	}
	if res.Action != string(auth.CAEPSessionRevoke) || res.UserID != u.ID {
		t.Errorf("result = %+v, want session_revoke on %s", res, u.ID)
	}

	// Session MUST be revoked.
	if _, err := a.Authenticate(ctx, sessTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("session after revoke = %v, want ErrUnauthenticated", err)
	}
	// Tenant-bound token also revoked (revokeUserAccess revokes tenant-bound tokens).
	if _, err := a.Authenticate(ctx, apiTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("api token after session revoke = %v, want ErrUnauthenticated", err)
	}
}

func TestCAEPReceiverTokenRevoke(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "bob@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	sessTok, apiTok := mintUserCreds(t, st, u.ID, tenant)
	enableCAEP(t, ctx, a, super, tenant, signer)

	res, err := a.CAEPReceiveEvent(ctx, super, tenant, caepEnvFromSET(t, signer.signSET(t, "k1", caepEvent(
		"https://schemas.openid.net/secevent/caep/event-type/token-claims-change",
		u.ID.String(), "j1",
	)), auth.CAEPTokenRevoke))
	if err != nil {
		t.Fatalf("token revoke = %v", err)
	}
	if res.Action != string(auth.CAEPTokenRevoke) {
		t.Errorf("result action = %q, want token_revoke", res.Action)
	}

	// API token MUST be revoked.
	if _, err := a.Authenticate(ctx, apiTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("api token after token revoke = %v, want ErrUnauthenticated", err)
	}
	// Session is NOT revoked (token-claims-change only cuts tokens).
	if _, err := a.Authenticate(ctx, sessTok); err != nil {
		t.Errorf("session after token revoke = %v, want still valid", err)
	}
}

func TestCAEPReceiverCredentialRevoke(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "carol@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	sessTok, apiTok := mintUserCreds(t, st, u.ID, tenant)
	enableCAEP(t, ctx, a, super, tenant, signer)

	res, err := a.CAEPReceiveEvent(ctx, super, tenant, caepEnvFromSET(t, signer.signSET(t, "k1", caepEvent(
		"https://schemas.openid.net/secevent/caep/event-type/credential-change",
		u.ID.String(), "j1",
	)), auth.CAEPCredentialRevoke))
	if err != nil {
		t.Fatalf("credential revoke = %v", err)
	}
	if res.Action != string(auth.CAEPCredentialRevoke) {
		t.Errorf("result action = %q, want credential_revoke", res.Action)
	}

	// Both session AND API token must be revoked.
	if _, err := a.Authenticate(ctx, sessTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("session after credential revoke = %v, want ErrUnauthenticated", err)
	}
	if _, err := a.Authenticate(ctx, apiTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("api token after credential revoke = %v, want ErrUnauthenticated", err)
	}
}

func TestCAEPReceiverDeviceNonCompliantRevoke(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "dave@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	sessTok, apiTok := mintUserCreds(t, st, u.ID, tenant)
	// Default device action = "revoke".
	enableCAEP(t, ctx, a, super, tenant, signer)

	res, err := a.CAEPReceiveEvent(ctx, super, tenant, caepEnvFromSET(t, signer.signSET(t, "k1", caepEvent(
		"https://schemas.openid.net/secevent/caep/event-type/device-compliance-change",
		u.ID.String(), "j1",
	)), auth.CAEPDeviceNonCompliant))
	if err != nil {
		t.Fatalf("device non-compliant revoke = %v", err)
	}
	if res.Action != string(auth.CAEPDeviceNonCompliant) {
		t.Errorf("result action = %q, want device_noncompliant", res.Action)
	}

	// Default action is revoke: both session and token must be cut.
	if _, err := a.Authenticate(ctx, sessTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("session after device revoke = %v, want ErrUnauthenticated", err)
	}
	if _, err := a.Authenticate(ctx, apiTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("api token after device revoke = %v, want ErrUnauthenticated", err)
	}
}

func TestCAEPReceiverDeviceNonCompliantStepUp(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "eve@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}

	// Mint a session with elevated AAL (simulate step-up session).
	sessTok, _ := mintUserCredsWithAAL(t, st, u.ID, tenant, 3)
	// Also mint a normal session (AAL1) — should be left alone.
	sessTokNormal, _ := mintUserCredsWithAAL(t, st, u.ID, tenant, 1)

	// DeviceNonCompliantAction = "step_up".
	enableCAEPStepUp(t, ctx, a, super, tenant, signer)

	_, err = a.CAEPReceiveEvent(ctx, super, tenant, caepEnvFromSET(t, signer.signSET(t, "k1", caepEvent(
		"https://schemas.openid.net/secevent/caep/event-type/device-compliance-change",
		u.ID.String(), "j1",
	)), auth.CAEPDeviceNonCompliant))
	if err != nil {
		t.Fatalf("device non-compliant step-up = %v", err)
	}

	// The elevated session should still be accessible (not revoked — just degraded).
	if _, err := a.Authenticate(ctx, sessTok); err != nil {
		t.Errorf("elevated session after step-up = %v, want still valid (just degraded)", err)
	}
	if _, err := a.Authenticate(ctx, sessTokNormal); err != nil {
		t.Errorf("normal session after step-up = %v, want still valid", err)
	}

	// Verify AAL was degraded in the store by reading the session back.
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		sessions, _, err := as.Sessions().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: u.ID.String()}},
			Limit:   10,
		})
		if err != nil {
			return err
		}
		for _, s := range sessions {
			if s.Revoked {
				t.Errorf("session %s is revoked after step-up (should only be degraded)", s.ID)
				continue
			}
			if s.AAL > 1 {
				t.Errorf("session %s AAL = %d after step-up, want ≤ 1", s.ID, s.AAL)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCAEPReceiverAccountDisable(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "frank@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	sessTok, apiTok := mintUserCreds(t, st, u.ID, tenant)
	enableCAEP(t, ctx, a, super, tenant, signer)

	res, err := a.CAEPReceiveEvent(ctx, super, tenant, caepEnvFromSET(t, signer.signSET(t, "k1", caepEvent(
		"https://schemas.openid.net/secevent/risc/event-type/account-disabled",
		u.ID.String(), "j1",
	)), auth.CAEPAccountDisable))
	if err != nil {
		t.Fatalf("account disable = %v", err)
	}
	if res.Action != string(auth.CAEPAccountDisable) || res.UserID != u.ID {
		t.Errorf("result = %+v, want account_disable on %s", res, u.ID)
	}

	// User must be inactive in the store.
	got, err := a.SCIMGetMember(ctx, tenant, u.ID)
	if err != nil || got.Status != model.StatusInactive {
		t.Fatalf("member after disable = (%v, status=%v), want present+inactive", err, got.Status)
	}

	// Total cut: both session and API token must be revoked.
	if _, err := a.Authenticate(ctx, sessTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("session after account disable = %v, want ErrUnauthenticated", err)
	}
	if _, err := a.Authenticate(ctx, apiTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("api token after account disable = %v, want ErrUnauthenticated", err)
	}
}

func TestCAEPReceiverCredentialCompromise(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "grace@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	sessTok, apiTok := mintUserCreds(t, st, u.ID, tenant)
	enableCAEP(t, ctx, a, super, tenant, signer)

	res, err := a.CAEPReceiveEvent(ctx, super, tenant, caepEnvFromSET(t, signer.signSET(t, "k1", caepEvent(
		"https://schemas.openid.net/secevent/risc/event-type/credential-compromise",
		u.ID.String(), "j1",
	)), auth.CAEPCredentialCompromise))
	if err != nil {
		t.Fatalf("credential compromise = %v", err)
	}
	if res.Action != string(auth.CAEPCredentialCompromise) {
		t.Errorf("result action = %q, want credential_compromise", res.Action)
	}

	// Total cut: both session and API token must be revoked.
	if _, err := a.Authenticate(ctx, sessTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("session after compromise = %v, want ErrUnauthenticated", err)
	}
	if _, err := a.Authenticate(ctx, apiTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("api token after compromise = %v, want ErrUnauthenticated", err)
	}
}

func TestCAEPReceiverIgnoreUnknownEvent(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "hank@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	sessTok, _ := mintUserCreds(t, st, u.ID, tenant)
	enableCAEP(t, ctx, a, super, tenant, signer)

	// Send a valid, signed SET with CAEPIgnore action.
	res, err := a.CAEPReceiveEvent(ctx, super, tenant, caepEnvFromSET(t, signer.signSET(t, "k1", caepEvent(
		"https://schemas.openid.net/secevent/caep/event-type/unknown-future-event",
		u.ID.String(), "j1",
	)), auth.CAEPIgnore))
	if err != nil {
		t.Fatalf("ignore event = %v", err)
	}
	if res.Action != string(auth.CAEPIgnore) {
		t.Errorf("result action = %q, want ignore", res.Action)
	}

	// No credential cut: session must still be valid.
	if _, err := a.Authenticate(ctx, sessTok); err != nil {
		t.Errorf("session after ignore = %v, want still valid", err)
	}
}

func TestCAEPReceiverRejectsBadSignature(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "ivy@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	enableCAEP(t, ctx, a, super, tenant, signer)

	// A SET signed by a DIFFERENT key must be rejected.
	attacker := newES256Signer(t)
	env := caepEnvFromSET(t, attacker.signSET(t, "k1", caepEvent(
		"https://schemas.openid.net/secevent/caep/event-type/session-revoked",
		u.ID.String(), "j1",
	)), auth.CAEPSessionRevoke)

	if _, err := a.CAEPReceiveEvent(ctx, super, tenant, env); !errors.Is(err, auth.ErrSCIMSetSignature) {
		t.Errorf("foreign-signed SET = %v, want ErrSCIMSetSignature", err)
	}
}

func TestCAEPReceiverRejectsWrongIssuer(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "jake@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	enableCAEP(t, ctx, a, super, tenant, signer)

	wrongIss := signer.signSET(t, "k1", map[string]any{
		"iss": "https://evil.example", "aud": []string{caepSetAud},
		"iat": time.Now().Unix(), "jti": "j1",
		"sub_id": map[string]any{"format": "opaque", "id": u.ID.String()},
		"events": map[string]any{
			"https://schemas.openid.net/secevent/caep/event-type/session-revoked": map[string]any{},
		},
	})
	env := caepEnvFromSET(t, wrongIss, auth.CAEPSessionRevoke)

	if _, err := a.CAEPReceiveEvent(ctx, super, tenant, env); !errors.Is(err, auth.ErrSCIMSetIssuer) {
		t.Errorf("wrong-issuer SET = %v, want ErrSCIMSetIssuer", err)
	}
}

func TestCAEPReceiverRejectsStaleIAT(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "kate@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	enableCAEP(t, ctx, a, super, tenant, signer)

	// SET with an iat 48 hours in the past (way beyond the default 1-hour max age).
	stale := signer.signSET(t, "k1", map[string]any{
		"iss": caepSetIssuer, "aud": []string{caepSetAud},
		"iat": time.Now().Add(-48 * time.Hour).Unix(), "jti": "j1",
		"sub_id": map[string]any{"format": "opaque", "id": u.ID.String()},
		"events": map[string]any{
			"https://schemas.openid.net/secevent/caep/event-type/session-revoked": map[string]any{},
		},
	})
	env := caepEnvFromSET(t, stale, auth.CAEPSessionRevoke)

	if _, err := a.CAEPReceiveEvent(ctx, super, tenant, env); !errors.Is(err, auth.ErrSCIMSetIssuer) {
		t.Errorf("stale SET = %v, want ErrSCIMSetIssuer", err)
	}
}

func TestCAEPReceiverRejectsJTIReplay(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "lena@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	enableCAEP(t, ctx, a, super, tenant, signer)

	env := caepEnvFromSET(t, signer.signSET(t, "k1", caepEvent(
		"https://schemas.openid.net/secevent/caep/event-type/session-revoked",
		u.ID.String(), "replay-jti",
	)), auth.CAEPSessionRevoke)

	// First delivery succeeds.
	if _, err := a.CAEPReceiveEvent(ctx, super, tenant, env); err != nil {
		t.Fatalf("first delivery = %v", err)
	}

	// Second delivery with same jti must be rejected (any non-nil error → rejection).
	if _, err := a.CAEPReceiveEvent(ctx, super, tenant, env); err == nil {
		t.Fatal("jti replay = nil, want error (duplicate)")
	}
}

func TestCAEPReceiverRejectsUnknownSubject(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)
	enableCAEP(t, ctx, a, super, tenant, signer)

	// A valid SET targeting a user ID that is NOT a member of this tenant.
	env := caepEnvFromSET(t, signer.signSET(t, "k1", caepEvent(
		"https://schemas.openid.net/secevent/caep/event-type/session-revoked",
		"00000000-0000-0000-0000-000000000000", "j1",
	)), auth.CAEPSessionRevoke)

	if _, err := a.CAEPReceiveEvent(ctx, super, tenant, env); !errors.Is(err, auth.ErrSCIMSetSubject) {
		t.Errorf("unknown subject = %v, want ErrSCIMSetSubject", err)
	}
}

func TestCAEPReceiverSubjectByEmail(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	signer := newES256Signer(t)

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "mia@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	sessTok, _ := mintUserCreds(t, st, u.ID, tenant)
	enableCAEP(t, ctx, a, super, tenant, signer)

	// Use email-format sub_id (no opaque user ID).
	env := caepEnvFromSET(t, signer.signSET(t, "k1", caepEventByEmail(
		"https://schemas.openid.net/secevent/caep/event-type/session-revoked",
		"mia@acme.com", "j1",
	)), auth.CAEPSessionRevoke)

	res, err := a.CAEPReceiveEvent(ctx, super, tenant, env)
	if err != nil {
		t.Fatalf("email-subject session revoke = %v", err)
	}
	if res.UserID != u.ID {
		t.Errorf("resolved user = %s, want %s", res.UserID, u.ID)
	}
	if _, err := a.Authenticate(ctx, sessTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("session after email-subject revoke = %v, want ErrUnauthenticated", err)
	}
}

// mintUserCredsWithAAL creates a session with a specific AAL level and a
// tenant-bound API token. Returns the session wire token and API token.
func mintUserCredsWithAAL(t *testing.T, st store.Store, userID model.ID, tenant model.TenantID, aal int) (sessionTok, apiTok string) {
	t.Helper()
	ctx := context.Background()
	sc, err := auth.NewCredential(auth.PrefixSession)
	if err != nil {
		t.Fatal(err)
	}
	tc, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		if _, err := as.Sessions().Create(ctx, model.AuthSession{
			UserID: userID, Selector: sc.Selector, SecretHash: sc.SecretHash,
			ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)),
			AAL:       aal,
		}); err != nil {
			return err
		}
		_, err := as.Tokens().Create(ctx, model.APIToken{
			Name: "u-tok-aal", UserID: userID, Selector: tc.Selector, SecretHash: tc.SecretHash,
			BoundTenantID: tenant, Role: auth.RoleViewer,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return sc.Token, tc.Token
}
