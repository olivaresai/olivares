// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
)

const testClientID = "client-1"

// oidcTestIDP is an in-process OpenID Provider: it serves discovery + JWKS + a token
// endpoint that returns an id_token whose claims the test fully controls. It lets the
// real oidcProvider.validate run end-to-end (discovery → code exchange → ID-token
// signature/iss/aud/exp verify → our nonce/azp/email checks) without a live IdP.
type oidcTestIDP struct {
	srv      *httptest.Server
	key      *rsa.PrivateKey
	idToken  string         // returned by the token endpoint; set per test before validate
	userinfo map[string]any // returned by /userinfo; nil ⇒ 404 (endpoint not exercised)
}

func newOIDCTestIDP(t *testing.T) *oidcTestIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &oidcTestIDP{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONTest(w, map[string]any{
			"issuer":                                idp.srv.URL,
			"authorization_endpoint":                idp.srv.URL + "/authorize",
			"token_endpoint":                        idp.srv.URL + "/token",
			"jwks_uri":                              idp.srv.URL + "/jwks",
			"userinfo_endpoint":                     idp.srv.URL + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := key.PublicKey
		eBuf := make([]byte, 8)
		binary.BigEndian.PutUint64(eBuf, uint64(pub.E))
		eBuf = trimLeftZeros(eBuf)
		writeJSONTest(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "kid": "test-key", "alg": "RS256",
			"n": b64url(pub.N.Bytes()), "e": b64url(eBuf),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONTest(w, map[string]any{
			"access_token": "at", "token_type": "Bearer", "id_token": idp.idToken,
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		if idp.userinfo == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSONTest(w, idp.userinfo)
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

// provider builds the real oidcProvider against this IdP (runs discovery).
func (idp *oidcTestIDP) provider(t *testing.T) *Provider {
	t.Helper()
	p, err := oidcFromEnv(envFrom(map[string]string{
		envProtocol:         auth.ProtocolOIDC,
		envOIDCIssuer:       idp.srv.URL,
		envOIDCClientID:     testClientID,
		envOIDCClientSecret: "secret",
	}))
	if err != nil {
		t.Fatalf("oidcFromEnv: %v", err)
	}
	return p
}

// signRS256 mints an RS256-signed JWT with the IdP key and "test-key" kid.
func (idp *oidcTestIDP) signRS256(claims map[string]any) string {
	header := b64url(mustJSON(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "test-key"}))
	payload := b64url(mustJSON(claims))
	signingInput := header + "." + payload
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return signingInput + "." + b64url(sig)
}

// baseClaims is a valid id_token for testClientID; tests override fields.
func (idp *oidcTestIDP) baseClaims(nonce string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": idp.srv.URL, "sub": "user-123", "aud": testClientID,
		"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
		"nonce": nonce, "email": "alice@corp.example", "email_verified": true,
	}
}

// validateWith runs the full validate path; the id_token comes from the IdP's token
// endpoint (idp.idToken), so the test controls the claims, and reqNonce is the nonce
// the engine persisted for this login (checked against the token's nonce).
func validateWith(t *testing.T, p *Provider, reqNonce string) (auth.FederatedIdentity, error) {
	t.Helper()
	return p.ValidateAssertion(context.Background(), auth.Assertion{
		Protocol: auth.ProtocolOIDC, Raw: "auth-code", Nonce: reqNonce,
		PKCEVerifier: "verifier", RedirectURI: "https://app.example/callback",
	})
}

func TestOIDC_ValidToken(t *testing.T) {
	idp := newOIDCTestIDP(t)
	p := idp.provider(t)
	idp.idToken = idp.signRS256(idp.baseClaims("n-1"))

	id, err := validateWith(t, p, "n-1")
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if id.Email != "alice@corp.example" || id.Subject != "user-123" {
		t.Errorf("identity = %+v, want alice@corp.example / user-123", id)
	}
}

// TestOIDC_SingleAudienceForeignAZPRejected is the azp regression (C4): a token
// whose single audience IS this client but whose `azp` names a DIFFERENT client must
// be rejected (OIDC Core §3.1.3.7 r.5). Before the fix the azp check was gated on
// len(aud)>1, so this token was ACCEPTED — an audience-confusion hole.
func TestOIDC_SingleAudienceForeignAZPRejected(t *testing.T) {
	idp := newOIDCTestIDP(t)
	p := idp.provider(t)
	claims := idp.baseClaims("n-1")
	claims["aud"] = testClientID // single audience == this client
	claims["azp"] = "attacker-client"
	idp.idToken = idp.signRS256(claims)

	_, err := validateWith(t, p, "n-1")
	if err == nil || !strings.Contains(err.Error(), "azp") {
		t.Fatalf("a single-audience token with a foreign azp must be rejected for azp; got err=%v", err)
	}
}

func TestOIDC_MultiAudienceWithoutAZPRejected(t *testing.T) {
	idp := newOIDCTestIDP(t)
	p := idp.provider(t)
	claims := idp.baseClaims("n-1")
	claims["aud"] = []string{testClientID, "other-aud"} // multi-audience, no azp
	idp.idToken = idp.signRS256(claims)

	_, err := validateWith(t, p, "n-1")
	if err == nil || !strings.Contains(err.Error(), "azp") {
		t.Fatalf("a multi-audience token without azp must be rejected; got err=%v", err)
	}
}

func TestOIDC_MultiAudienceWithMatchingAZPAccepted(t *testing.T) {
	idp := newOIDCTestIDP(t)
	p := idp.provider(t)
	claims := idp.baseClaims("n-1")
	claims["aud"] = []string{testClientID, "other-aud"}
	claims["azp"] = testClientID // azp authorizes THIS client → valid
	idp.idToken = idp.signRS256(claims)

	if _, err := validateWith(t, p, "n-1"); err != nil {
		t.Fatalf("a multi-audience token whose azp == client_id must be accepted; got %v", err)
	}
}

func TestOIDC_NonceMismatchRejected(t *testing.T) {
	idp := newOIDCTestIDP(t)
	p := idp.provider(t)
	idp.idToken = idp.signRS256(idp.baseClaims("token-nonce"))

	// The request carried a DIFFERENT nonce than the token → replay/mixup rejected.
	_, err := validateWith(t, p, "request-nonce")
	if err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("a nonce mismatch must be rejected; got err=%v", err)
	}
}

func TestOIDC_UnverifiedEmailRejected(t *testing.T) {
	idp := newOIDCTestIDP(t)
	p := idp.provider(t)
	claims := idp.baseClaims("n-1")
	claims["email_verified"] = false
	idp.idToken = idp.signRS256(claims)

	_, err := validateWith(t, p, "n-1")
	if err == nil || !strings.Contains(err.Error(), "verified") {
		t.Fatalf("an explicitly unverified email must be rejected; got err=%v", err)
	}
}

func TestOIDC_ExpiredTokenRejected(t *testing.T) {
	idp := newOIDCTestIDP(t)
	p := idp.provider(t)
	claims := idp.baseClaims("n-1")
	claims["exp"] = time.Now().Add(-time.Minute).Unix() // already expired
	idp.idToken = idp.signRS256(claims)

	if _, err := validateWith(t, p, "n-1"); err == nil {
		t.Fatal("an expired id_token must be rejected (verifier must not skip expiry)")
	}
}

func TestOIDC_WrongAudienceRejected(t *testing.T) {
	idp := newOIDCTestIDP(t)
	p := idp.provider(t)
	claims := idp.baseClaims("n-1")
	claims["aud"] = "some-other-client" // does not include this client
	idp.idToken = idp.signRS256(claims)

	if _, err := validateWith(t, p, "n-1"); err == nil {
		t.Fatal("a token whose audience does not include this client must be rejected")
	}
}

func TestOIDC_NonRSAlgRejected(t *testing.T) {
	idp := newOIDCTestIDP(t)
	p := idp.provider(t)
	// An HS256-headed token: the verifier pins RS256/ES256, so the alg is refused
	// before any signature check (defeats an alg-confusion / "none" downgrade).
	header := b64url(mustJSON(map[string]any{"alg": "HS256", "typ": "JWT"}))
	payload := b64url(mustJSON(idp.baseClaims("n-1")))
	idp.idToken = header + "." + payload + "." + b64url([]byte("not-a-real-sig"))

	if _, err := validateWith(t, p, "n-1"); err == nil {
		t.Fatal("a token signed with a non-pinned algorithm must be rejected")
	}
}

// providerWithGroups builds the real oidcProvider with a configured groups claim, so a
// test can exercise the UserInfo-for-groups enrichment path (U1/U3).
func (idp *oidcTestIDP) providerWithGroups(t *testing.T, groupsClaim string) *Provider {
	t.Helper()
	p, err := oidcFromEnv(envFrom(map[string]string{
		envProtocol:         auth.ProtocolOIDC,
		envOIDCIssuer:       idp.srv.URL,
		envOIDCClientID:     testClientID,
		envOIDCClientSecret: "secret",
		envOIDCGroupsClaim:  groupsClaim,
	}))
	if err != nil {
		t.Fatalf("oidcFromEnv (groups): %v", err)
	}
	return p
}

// TestOIDC_UserInfoSubMismatchDiscarded verifies OIDC Core §5.3.2: when the ID token
// omits email (so validate fetches UserInfo) and the UserInfo response carries a
// DIFFERENT sub, its claims MUST NOT be used — so the substituted email is discarded
// and the login fails at the "no email" check (proving the bad email never leaked in,
// and the subject — the U3 correlation key — is never redefined by UserInfo).
func TestOIDC_UserInfoSubMismatchDiscarded(t *testing.T) {
	idp := newOIDCTestIDP(t)
	p := idp.provider(t)
	claims := idp.baseClaims("n-1")
	delete(claims, "email") // force the UserInfo fetch
	delete(claims, "email_verified")
	idp.idToken = idp.signRS256(claims)
	idp.userinfo = map[string]any{"sub": "someone-else", "email": "attacker@evil.example", "email_verified": true}

	if _, err := validateWith(t, p, "n-1"); err == nil || !strings.Contains(err.Error(), "no email") {
		t.Fatalf("mismatched-sub UserInfo must be discarded (login fails 'no email'); err = %v", err)
	}
}

// TestOIDC_UserInfoSubMatchUsed is the positive control: UserInfo supplying the missing
// email with a MATCHING sub completes, and the identity's subject is the verified
// id_token sub (never a value the UserInfo body could have redefined).
func TestOIDC_UserInfoSubMatchUsed(t *testing.T) {
	idp := newOIDCTestIDP(t)
	p := idp.provider(t)
	claims := idp.baseClaims("n-1")
	delete(claims, "email")
	delete(claims, "email_verified")
	idp.idToken = idp.signRS256(claims)
	idp.userinfo = map[string]any{"sub": "user-123", "email": "alice@corp.example", "email_verified": true}

	id, err := validateWith(t, p, "n-1")
	if err != nil {
		t.Fatalf("matching userinfo rejected: %v", err)
	}
	if id.Subject != "user-123" || id.Email != "alice@corp.example" {
		t.Fatalf("identity = %+v, want user-123 / alice@corp.example", id)
	}
}

// TestOIDC_UserInfoAbsentSubDiscardedNotFatal proves the discard-don't-DoS behavior for
// the groups-enrichment path: the id_token carries a valid email but no groups, so
// UserInfo is fetched only to enrich groups; a UserInfo response that omits `sub`
// (non-conformant) is discarded — the login STILL SUCCEEDS on the id_token identity,
// it simply carries no groups (the discarded response could not supply them).
func TestOIDC_UserInfoAbsentSubDiscardedNotFatal(t *testing.T) {
	idp := newOIDCTestIDP(t)
	p := idp.providerWithGroups(t, "groups")
	claims := idp.baseClaims("n-1") // has a valid email; no groups claim
	idp.idToken = idp.signRS256(claims)
	// UserInfo returns groups but NO sub (non-conformant) → discarded.
	idp.userinfo = map[string]any{"groups": []any{"eng", "admins"}, "email": "alice@corp.example"}

	id, err := validateWith(t, p, "n-1")
	if err != nil {
		t.Fatalf("valid id_token login must not fail over a malformed groups-only UserInfo: %v", err)
	}
	if id.Email != "alice@corp.example" || id.Subject != "user-123" {
		t.Fatalf("identity = %+v, want the id_token identity", id)
	}
	if len(id.Groups) != 0 {
		t.Fatalf("groups from a sub-less (discarded) UserInfo must not be used, got %v", id.Groups)
	}
}

// --- small JOSE/JSON helpers (stdlib only) ----------------------------------

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func writeJSONTest(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func trimLeftZeros(b []byte) []byte {
	i := 0
	for i < len(b)-1 && b[i] == 0 {
		i++
	}
	return b[i:]
}
