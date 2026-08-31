// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

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
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

func signSETForTest(t *testing.T, priv *ecdsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	b64 := base64.RawURLEncoding
	hdr, _ := json.Marshal(map[string]any{"alg": "ES256", "kid": "k1", "typ": "secevent+jwt"})
	payload, _ := json.Marshal(claims)
	signingInput := b64.EncodeToString(hdr) + "." + b64.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, priv, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + b64.EncodeToString(sig)
}

func TestSCIMEventsEndpoint(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "acme")
	super, err := h.authr.Authenticate(ctx, adminTok)
	if err != nil {
		t.Fatal(err)
	}
	tok := h.scimToken(super, tenant)
	const base = "/v1/scim/v2"

	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if err := h.authr.ConfigureSCIMSet(ctx, super, tenant, auth.SCIMSetConfig{
		SETPublisher: auth.SETPublisher{
			Enabled: true, Issuer: "https://idp.test", Audiences: []string{"https://cp.test"},
			Keys: []auth.SETVerificationKey{{Kid: "k1", Alg: "ES256", PEM: pubPEM}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	u, _, err := h.authr.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "leaver@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}

	// A valid prov:delete SET → 202 Accepted, and the member is offboarded.
	set := signSETForTest(t, priv, map[string]any{
		"iss": "https://idp.test", "aud": []string{"https://cp.test"}, "iat": time.Now().Unix(), "jti": "j1",
		"sub_id": map[string]any{"format": "scim", "uri": "Users/" + u.ID.String()},
		"events": map[string]any{"urn:ietf:params:scim:event:prov:delete": map[string]any{}},
	})
	if resp := h.scim("POST", base+"/Events", tok, set); resp.code != http.StatusAccepted {
		t.Fatalf("POST /Events = %d (%s), want 202", resp.code, resp.raw)
	}
	if _, err := h.authr.SCIMGetMember(ctx, tenant, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("member after SET delete = %v, want ErrNotFound", err)
	}

	// A malformed SET body → 400 with an RFC 8935 {err} payload.
	resp := h.scim("POST", base+"/Events", tok, "not-a-jws")
	if resp.code != http.StatusBadRequest || resp.body["err"] == nil {
		t.Errorf("malformed SET = %d body=%v, want 400 with err", resp.code, resp.body)
	}

	// Unauthenticated → 401 (bearer required).
	if resp := h.scim("POST", base+"/Events", "", set); resp.code != http.StatusUnauthorized {
		t.Errorf("no-token /Events = %d, want 401", resp.code)
	}
}
