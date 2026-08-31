// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	setIssuer = "https://idp.example.test"
	setAud    = "https://olivares.example.test"
)

// es256Signer holds a freshly generated P-256 key and renders its public half as
// the PEM the SET config stores.
type es256Signer struct {
	priv *ecdsa.PrivateKey
	pem  string
}

func newES256Signer(t *testing.T) es256Signer {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return es256Signer{priv: priv, pem: string(pubPEM)}
}

// signSET serializes and ES256-signs a SET with the given claims, returning the
// compact JWS the receiver consumes.
func (s es256Signer) signSET(t *testing.T, kid string, claims map[string]any) []byte {
	t.Helper()
	b64 := base64.RawURLEncoding
	hdr, _ := json.Marshal(map[string]any{"alg": "ES256", "kid": kid, "typ": "secevent+jwt"})
	payload, _ := json.Marshal(claims)
	signingInput := b64.EncodeToString(hdr) + "." + b64.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signingInput))
	r, ss, err := ecdsa.Sign(rand.Reader, s.priv, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	ss.FillBytes(sig[32:])
	return []byte(signingInput + "." + b64.EncodeToString(sig))
}

// provEvent builds the SET claim set for one provisioning event against a user id.
func provEvent(eventURI, userID, jti string) map[string]any {
	return map[string]any{
		"iss":    setIssuer,
		"aud":    []string{setAud},
		"iat":    time.Now().Unix(),
		"jti":    jti,
		"sub_id": map[string]any{"format": "scim", "uri": "Users/" + userID},
		"events": map[string]any{eventURI: map[string]any{}},
	}
}

// envFromSET replays what the /Events handler does: parse the compact JWS, decode
// the SET, and translate the first access-affecting event URI into the action.
func envFromSET(t *testing.T, token []byte) auth.SCIMEventEnvelope {
	t.Helper()
	hdr, payload, signingInput, sig, err := scim.ParseCompactJWS(token)
	if err != nil {
		t.Fatalf("parse JWS: %v", err)
	}
	set, err := scim.DecodeSET(payload)
	if err != nil {
		t.Fatalf("decode SET: %v", err)
	}
	action := auth.SCIMSetIgnore
	for _, uri := range set.EventURIs() {
		switch scim.ActionForEvent(uri) {
		case scim.ActionDeprovision:
			action = auth.SCIMSetDeprovision
		case scim.ActionDisable:
			action = auth.SCIMSetDisable
		case scim.ActionActivate:
			action = auth.SCIMSetActivate
		}
		if action != auth.SCIMSetIgnore {
			break
		}
	}
	var id, ext string
	if set.SubjectID != nil {
		_, id = set.SubjectID.ResourcePath()
		ext = set.SubjectID.ExternalID
	}
	return auth.SCIMEventEnvelope{
		Alg: hdr.Alg, Kid: hdr.Kid, SigningInput: signingInput, Signature: sig,
		Issuer: set.Issuer, Audience: []string(set.Audience), JTI: set.JTI, IssuedAt: set.IssuedAt,
		SubjectID: id, SubjectExternalID: ext, Action: action,
	}
}

func enableSET(t *testing.T, ctx context.Context, a *auth.Authenticator, super auth.Principal, tenant model.TenantID, signer es256Signer) {
	t.Helper()
	if err := a.ConfigureSCIMSet(ctx, super, tenant, auth.SCIMSetConfig{
		SETPublisher: auth.SETPublisher{
			Enabled:   true,
			Issuer:    setIssuer,
			Audiences: []string{setAud},
			Keys:      []auth.SETVerificationKey{{Kid: "k1", Alg: "ES256", PEM: signer.pem}},
		},
	}); err != nil {
		t.Fatalf("configure SET: %v", err)
	}
}

func TestSCIMSetReceiverDenyClosedWhenUnconfigured(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "a@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	signer := newES256Signer(t)
	env := envFromSET(t, signer.signSET(t, "k1", provEvent(scim.EventProvDeactivate, u.ID.String(), "j1")))
	// No ConfigureSCIMSet call → deny-closed.
	if _, err := a.SCIMReceiveEvent(ctx, super, tenant, env); !errors.Is(err, auth.ErrSCIMSetDisabled) {
		t.Fatalf("receive without config = %v, want ErrSCIMSetDisabled", err)
	}
}

func TestSCIMSetReceiverDeactivateThenActivate(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "agent@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	sessTok, apiTok := mintUserCreds(t, st, u.ID, tenant)
	signer := newES256Signer(t)
	enableSET(t, ctx, a, super, tenant, signer)

	// prov:deactivate → disable: cut creds, keep the record.
	res, err := a.SCIMReceiveEvent(ctx, super, tenant, envFromSET(t, signer.signSET(t, "k1", provEvent(scim.EventProvDeactivate, u.ID.String(), "j1"))))
	if err != nil {
		t.Fatalf("deactivate = %v", err)
	}
	if res.Action != auth.SCIMSetDisable || res.UserID != u.ID {
		t.Errorf("result = %+v, want disable on %s", res, u.ID)
	}
	if _, err := a.Authenticate(ctx, sessTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("session after deactivate = %v, want revoked", err)
	}
	if _, err := a.Authenticate(ctx, apiTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("token after deactivate = %v, want revoked", err)
	}
	got, err := a.SCIMGetMember(ctx, tenant, u.ID)
	if err != nil || got.Status != model.StatusInactive {
		t.Fatalf("member after deactivate = (%v, status=%v), want present+inactive", err, got.Status)
	}

	// prov:activate → re-enable: record returns to active.
	if _, err := a.SCIMReceiveEvent(ctx, super, tenant, envFromSET(t, signer.signSET(t, "k1", provEvent(scim.EventProvActivate, u.ID.String(), "j2")))); err != nil {
		t.Fatalf("activate = %v", err)
	}
	got, err = a.SCIMGetMember(ctx, tenant, u.ID)
	if err != nil || got.Status != model.StatusActive {
		t.Fatalf("member after activate = (%v, status=%v), want active", err, got.Status)
	}
}

func TestSCIMSetReceiverDeleteOffboards(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "leaver@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	sessTok, _ := mintUserCreds(t, st, u.ID, tenant)
	signer := newES256Signer(t)
	enableSET(t, ctx, a, super, tenant, signer)

	if _, err := a.SCIMReceiveEvent(ctx, super, tenant, envFromSET(t, signer.signSET(t, "k1", provEvent(scim.EventProvDelete, u.ID.String(), "j1")))); err != nil {
		t.Fatalf("delete = %v", err)
	}
	if _, err := a.SCIMGetMember(ctx, tenant, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("member after delete = %v, want ErrNotFound (offboarded)", err)
	}
	if _, err := a.Authenticate(ctx, sessTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("session after delete = %v, want revoked", err)
	}
}

func TestSCIMSetReceiverStrictKidAndStaleReject(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "k@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	signer := newES256Signer(t)
	// Configure a KEYLESS key (no kid).
	if err := a.ConfigureSCIMSet(ctx, super, tenant, auth.SCIMSetConfig{
		SETPublisher: auth.SETPublisher{
			Enabled: true, Issuer: setIssuer, Audiences: []string{setAud},
			Keys: []auth.SETVerificationKey{{Kid: "", Alg: "ES256", PEM: signer.pem}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Strict kid: a SET that NAMES a kid must NOT be verified by a keyless key
	// (the bypass the adversarial review caught). It is rejected and the member
	// is untouched.
	withKid := signer.signSET(t, "attacker-kid", provEvent(scim.EventProvDelete, u.ID.String(), "j1"))
	if _, err := a.SCIMReceiveEvent(ctx, super, tenant, envFromSET(t, withKid)); !errors.Is(err, auth.ErrSCIMSetSignature) {
		t.Errorf("kid-bearing SET vs keyless key = %v, want ErrSCIMSetSignature (strict kid)", err)
	}
	if _, err := a.SCIMGetMember(ctx, tenant, u.ID); err != nil {
		t.Errorf("member after rejected kid SET = %v, want still present", err)
	}

	// Lenient when the SET carries NO kid: the keyless key verifies it.
	noKid := signer.signSET(t, "", provEvent(scim.EventProvDeactivate, u.ID.String(), "j2"))
	if _, err := a.SCIMReceiveEvent(ctx, super, tenant, envFromSET(t, noKid)); err != nil {
		t.Errorf("no-kid SET vs keyless key = %v, want accepted", err)
	}

	// A stale (captured-and-replayed) SET is rejected by the iat past-skew bound.
	stale := signer.signSET(t, "", map[string]any{
		"iss": setIssuer, "aud": []string{setAud}, "iat": time.Now().Add(-48 * time.Hour).Unix(), "jti": "j3",
		"sub_id": map[string]any{"format": "scim", "uri": "Users/" + u.ID.String()},
		"events": map[string]any{scim.EventProvDelete: map[string]any{}},
	})
	if _, err := a.SCIMReceiveEvent(ctx, super, tenant, envFromSET(t, stale)); !errors.Is(err, auth.ErrSCIMSetIssuer) {
		t.Errorf("stale SET = %v, want ErrSCIMSetIssuer (past-skew bound)", err)
	}
}

func TestSCIMSetReceiverRejectsBadSignatureAndIssuer(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "v@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	signer := newES256Signer(t)
	enableSET(t, ctx, a, super, tenant, signer)

	// A SET signed by a DIFFERENT key fails verification (deny-closed).
	attacker := newES256Signer(t)
	if _, err := a.SCIMReceiveEvent(ctx, super, tenant, envFromSET(t, attacker.signSET(t, "k1", provEvent(scim.EventProvDelete, u.ID.String(), "j1")))); !errors.Is(err, auth.ErrSCIMSetSignature) {
		t.Errorf("foreign-signed SET = %v, want ErrSCIMSetSignature", err)
	}
	// The member must be untouched after a rejected SET.
	if _, err := a.SCIMGetMember(ctx, tenant, u.ID); err != nil {
		t.Errorf("member after rejected SET = %v, want still present", err)
	}

	// A correctly-signed SET with the wrong issuer is rejected.
	badIss := signer.signSET(t, "k1", map[string]any{
		"iss": "https://evil.example", "aud": []string{setAud}, "iat": time.Now().Unix(), "jti": "j2",
		"sub_id": map[string]any{"format": "scim", "uri": "Users/" + u.ID.String()},
		"events": map[string]any{scim.EventProvDelete: map[string]any{}},
	})
	if _, err := a.SCIMReceiveEvent(ctx, super, tenant, envFromSET(t, badIss)); !errors.Is(err, auth.ErrSCIMSetIssuer) {
		t.Errorf("wrong-issuer SET = %v, want ErrSCIMSetIssuer", err)
	}
}
