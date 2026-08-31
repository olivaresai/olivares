// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/bundle/spiffebundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

// signedSVIDForTD mints a JWT-SVID for a subject in trust domain td, signed with a
// fresh EC key, and returns the token plus a *spiffebundle.Bundle carrying that
// domain's matching JWT authority (the federated trust bundle).
func signedSVIDForTD(t *testing.T, td, kid string, claims jwt.Claims) (token string, bundle *spiffebundle.Bundle) {
	t.Helper()
	tok, pub := mintJWTSVID(t, kid, claims)
	domain, err := spiffeid.TrustDomainFromString(td)
	if err != nil {
		t.Fatalf("trust domain: %v", err)
	}
	b := spiffebundle.New(domain)
	if err := b.AddJWTAuthority(kid, pub); err != nil {
		t.Fatalf("add jwt authority: %v", err)
	}
	return tok, b
}

// mintJWTSVID signs claims with a fresh EC key under kid and returns the token plus
// the matching public key (the JWT authority a bundle must carry to verify it).
func mintJWTSVID(t *testing.T, kid string, claims jwt.Claims) (token string, pub crypto.PublicKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw, key.Public()
}

func claimsForTD(td, path, audience string) jwt.Claims {
	now := svidClock()
	return jwt.Claims{
		Subject:  scheme + td + path,
		Audience: jwt.Audience{audience},
		IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)),
		Expiry:   jwt.NewNumericDate(now.Add(time.Hour)),
	}
}

func marshalBundle(t *testing.T, b *spiffebundle.Bundle) string {
	t.Helper()
	doc, err := b.Marshal()
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	return string(doc)
}

// TestVerifyFederatedInlineBundle verifies a JWT-SVID from a FEDERATED foreign trust
// domain against an inline SPIFFE bundle for that domain — impossible before
// (the verifier accepted only one static JWKS).
func TestVerifyFederatedInlineBundle(t *testing.T) {
	token, bundle := signedSVIDForTD(t, "partner.example", "p1",
		claimsForTD("partner.example", "/workload/api", "anthropic-wif"))

	v, err := NewVerifier(VerifierConfig{
		Audience:      "anthropic-wif",
		InlineBundles: map[string]string{"partner.example": marshalBundle(t, bundle)},
	}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	v.now = svidClock

	got, err := v.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify federated: %v", err)
	}
	if got.SpiffeID != "spiffe://partner.example/workload/api" {
		t.Errorf("spiffe id = %q", got.SpiffeID)
	}
}

// TestVerifyFederatedEndpointFetched verifies a JWT-SVID against a bundle FETCHED
// lazily from a SPIFFE trust-bundle endpoint (the fetcher is injected so no live
// endpoint is needed).
func TestVerifyFederatedEndpointFetched(t *testing.T) {
	token, bundle := signedSVIDForTD(t, "partner.example", "p1",
		claimsForTD("partner.example", "/workload/api", "anthropic-wif"))

	var fetches int32
	v, err := NewVerifier(VerifierConfig{
		Audience: "anthropic-wif",
		Federations: []FederationEndpoint{{
			TrustDomain: "partner.example", BundleEndpointURL: "https://spire.partner.example/bundle",
		}},
	}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	v.now = svidClock
	v.fetch = func(_ context.Context, fe FederationEndpoint) (*spiffebundle.Bundle, error) {
		atomic.AddInt32(&fetches, 1)
		if fe.TrustDomain != "partner.example" {
			t.Errorf("fetch td = %q", fe.TrustDomain)
		}
		if fe.BundleEndpointURL != "https://spire.partner.example/bundle" {
			t.Errorf("fetch url = %q", fe.BundleEndpointURL)
		}
		return bundle, nil
	}

	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("Verify federated endpoint: %v", err)
	}
	if atomic.LoadInt32(&fetches) != 1 {
		t.Errorf("expected exactly one lazy fetch, got %d", fetches)
	}
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("Verify (cached): %v", err)
	}
	if atomic.LoadInt32(&fetches) != 1 {
		t.Errorf("a cached bundle must not be re-fetched; fetches = %d", fetches)
	}
}

// TestVerifyMultiDomainCoexist proves the legacy single-JWKS path and the federated
// bundle path coexist: a corp token verifies via the legacy keyset, a partner token
// via the federated bundle, in the SAME verifier.
func TestVerifyMultiDomainCoexist(t *testing.T) {
	corpToken, corpJWKS := signedSVID(t, jose.ES256, "c1", validClaims()) // corp.example via legacy JWKS
	partnerToken, partnerBundle := signedSVIDForTD(t, "partner.example", "p1",
		claimsForTD("partner.example", "/workload/api", "anthropic-wif"))

	v, err := NewVerifier(VerifierConfig{
		TrustDomain:   "corp.example",
		Audience:      "anthropic-wif",
		JWKS:          corpJWKS,
		InlineBundles: map[string]string{"partner.example": marshalBundle(t, partnerBundle)},
	}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	v.now = svidClock

	if got, err := v.Verify(context.Background(), corpToken); err != nil {
		t.Fatalf("corp via legacy: %v", err)
	} else if got.SpiffeID != "spiffe://corp.example/workload/api" {
		t.Errorf("corp spiffe id = %q", got.SpiffeID)
	}
	if got, err := v.Verify(context.Background(), partnerToken); err != nil {
		t.Fatalf("partner via federation: %v", err)
	} else if got.SpiffeID != "spiffe://partner.example/workload/api" {
		t.Errorf("partner spiffe id = %q", got.SpiffeID)
	}
}

// TestVerifyUntrustedDomainRejected proves a JWT-SVID from an UNFEDERATED foreign
// trust domain is NOT accepted via the legacy keyset (no cross-domain key confusion),
// even though a legacy keyset (corp.example) and another federated domain
// (partner.example) are both configured.
func TestVerifyUntrustedDomainRejected(t *testing.T) {
	_, partnerBundle := signedSVIDForTD(t, "partner.example", "p1",
		claimsForTD("partner.example", "/workload/api", "anthropic-wif"))
	corpToken, corpJWKS := signedSVID(t, jose.ES256, "c1", validClaims()) // legacy corp keyset
	evilToken, _ := signedSVIDForTD(t, "evil.example", "e1",
		claimsForTD("evil.example", "/workload/api", "anthropic-wif"))

	v, err := NewVerifier(VerifierConfig{
		TrustDomain:   "corp.example",
		Audience:      "anthropic-wif",
		JWKS:          corpJWKS,
		InlineBundles: map[string]string{"partner.example": marshalBundle(t, partnerBundle)},
	}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	v.now = svidClock

	// Sanity: the legacy keyset DOES verify its own domain.
	if _, err := v.Verify(context.Background(), corpToken); err != nil {
		t.Fatalf("corp token via legacy keyset: %v", err)
	}
	// evil.example is neither federated nor the legacy domain: it must be rejected —
	// the legacy corp keyset must NOT be used for an unfederated foreign domain.
	if _, err := v.Verify(context.Background(), evilToken); err == nil {
		t.Fatal("a token from an unfederated foreign domain must not be accepted via the legacy keyset")
	}
}

// TestVerifyFederatedUntrustedKeyRejected reaches the FEDERATED deny-closed path: a
// partner.example token signed by a key NOT in partner's trust bundle is rejected,
// and the error names the federated trust domain (proving the bundle was consulted).
func TestVerifyFederatedUntrustedKeyRejected(t *testing.T) {
	_, trusted := signedSVIDForTD(t, "partner.example", "p1",
		claimsForTD("partner.example", "/workload/api", "anthropic-wif"))
	// A partner.example-subject token signed by a DIFFERENT key under a kid (p2) the
	// federated bundle does not carry.
	rogue, _ := mintJWTSVID(t, "p2", claimsForTD("partner.example", "/workload/rogue", "anthropic-wif"))

	v, err := NewVerifier(VerifierConfig{
		Audience:      "anthropic-wif",
		InlineBundles: map[string]string{"partner.example": marshalBundle(t, trusted)},
	}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	v.now = svidClock

	_, err = v.Verify(context.Background(), rogue)
	if err == nil {
		t.Fatal("a federated token signed by an untrusted key must be rejected (deny-closed)")
	}
	if !strings.Contains(err.Error(), "partner.example") {
		t.Errorf("federated rejection must name the trust domain (proving the bundle was consulted), got: %v", err)
	}
}

// TestVerifyFederatedKeyRotation proves a rotated signing key is picked up across
// verifications: a first token caches a pre-rotation bundle (kid p1); a later token
// signed by the rotated key (kid p2) misses the cached bundle, forcing a re-fetch.
func TestVerifyFederatedKeyRotation(t *testing.T) {
	oldToken, staleBundle := signedSVIDForTD(t, "partner.example", "p1",
		claimsForTD("partner.example", "/workload/old", "anthropic-wif"))
	newToken, freshBundle := signedSVIDForTD(t, "partner.example", "p2",
		claimsForTD("partner.example", "/workload/new", "anthropic-wif"))

	var calls int32
	v, err := NewVerifier(VerifierConfig{
		Audience: "anthropic-wif",
		Federations: []FederationEndpoint{{
			TrustDomain: "partner.example", BundleEndpointURL: "https://spire.partner.example/bundle",
		}},
	}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	v.now = svidClock
	v.fetch = func(_ context.Context, _ FederationEndpoint) (*spiffebundle.Bundle, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return staleBundle, nil // initial lazy load: pre-rotation key (p1)
		}
		return freshBundle, nil // re-fetch on kid miss: rotated key (p2)
	}

	if _, err := v.Verify(context.Background(), oldToken); err != nil {
		t.Fatalf("Verify old token: %v", err)
	}
	if _, err := v.Verify(context.Background(), newToken); err != nil {
		t.Fatalf("Verify new token (post-rotation): %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("expected a re-fetch on kid miss (2 fetches total), got %d", calls)
	}
}

// TestVerifyRefreshHint proves the verifier honors a bundle's spiffe_refresh_hint:
// once the cached bundle is older than its hint, the NEXT verification proactively
// re-fetches the endpoint — even though the token's kid is still in the cache.
func TestVerifyRefreshHint(t *testing.T) {
	token, bundle := signedSVIDForTD(t, "partner.example", "p1",
		claimsForTD("partner.example", "/workload/api", "anthropic-wif"))
	bundle.SetRefreshHint(10 * time.Second)

	var calls int32
	v, err := NewVerifier(VerifierConfig{
		Audience: "anthropic-wif",
		Federations: []FederationEndpoint{{
			TrustDomain: "partner.example", BundleEndpointURL: "https://spire.partner.example/bundle",
		}},
	}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	clk := svidClock()
	v.now = func() time.Time { return clk }
	v.fetch = func(_ context.Context, _ FederationEndpoint) (*spiffebundle.Bundle, error) {
		atomic.AddInt32(&calls, 1)
		return bundle, nil
	}

	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("first verify fetches = %d, want 1", calls)
	}
	// Within the hint window: no re-fetch.
	clk = svidClock().Add(5 * time.Second)
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("verify within hint: %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("within refresh_hint must not re-fetch; fetches = %d", calls)
	}
	// Past the hint: the cached bundle is stale → proactive re-fetch.
	clk = svidClock().Add(20 * time.Second)
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("verify past hint: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("past refresh_hint must proactively re-fetch; fetches = %d", calls)
	}
}

// TestVerifyRefreshHintFetchFailureKeepsCache covers the availability-over-failure
// branch of refreshIfStale: when a past-refresh_hint re-fetch ERRORS, the cached
// bundle is retained and an otherwise-valid token still verifies.
func TestVerifyRefreshHintFetchFailureKeepsCache(t *testing.T) {
	token, bundle := signedSVIDForTD(t, "partner.example", "p1",
		claimsForTD("partner.example", "/workload/api", "anthropic-wif"))
	bundle.SetRefreshHint(10 * time.Second)

	var calls int32
	v, err := NewVerifier(VerifierConfig{
		Audience: "anthropic-wif",
		Federations: []FederationEndpoint{{
			TrustDomain: "partner.example", BundleEndpointURL: "https://spire.partner.example/bundle",
		}},
	}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	clk := svidClock()
	v.now = func() time.Time { return clk }
	v.fetch = func(_ context.Context, _ FederationEndpoint) (*spiffebundle.Bundle, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return bundle, nil // initial lazy load succeeds
		}
		return nil, errors.New("bundle endpoint unreachable") // the refresh fails
	}

	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	clk = svidClock().Add(20 * time.Second) // past the hint: a refresh is attempted and fails
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("a failed stale-refresh must keep the cached bundle (availability), got: %v", err)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Errorf("expected a refresh attempt past the hint, got %d fetch calls", calls)
	}
}

func TestNewVerifierHTTPSSpiffeRequiresEndpointID(t *testing.T) {
	_, err := NewVerifier(VerifierConfig{
		Federations: []FederationEndpoint{{
			TrustDomain: "gov.example", BundleEndpointURL: "https://spire.gov.example/bundle",
			Profile: ProfileHTTPSSpiffe, // no endpoint_spiffe_id
		}},
	}, nil)
	if err == nil {
		t.Fatal("https_spiffe without endpoint_spiffe_id must be rejected (deny-closed)")
	}
}

func TestNewVerifierRejectsUnknownProfile(t *testing.T) {
	_, err := NewVerifier(VerifierConfig{
		Federations: []FederationEndpoint{{
			TrustDomain: "gov.example", BundleEndpointURL: "https://x/bundle", Profile: "ftp",
		}},
	}, nil)
	if err == nil {
		t.Fatal("an unknown federation profile must be rejected")
	}
}

// --- https_spiffe profile (real TLS endpoint validation) --------------------

// spiffeTLSServer mints a CA and an X.509-SVID server certificate carrying spiffeID
// as its single URI SAN, then starts a TLS bundle endpoint serving bundleJSON. It
// returns the endpoint URL and the CA certificate (the bootstrap X.509 authority a
// verifier must trust to authenticate the endpoint).
func spiffeTLSServer(t *testing.T, spiffeID string, bodies ...string) (endpointURL string, caCert *x509.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "spiffe-fed-test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, caKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	uri := mustParseURL(t, spiffeID)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		URIs:        []*url.URL{uri}, // the SPIFFE ID — the single URI SAN of an X.509-SVID
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, leafKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}

	var n int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(atomic.AddInt32(&n, 1)) - 1 // serve bodies in order; the last one repeats
		if i >= len(bodies) {
			i = len(bodies) - 1
		}
		_, _ = w.Write([]byte(bodies[i]))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.URL, caCert
}

func mustParseURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestVerifyHTTPSSpiffeProfile drives the REAL https_spiffe fetch end-to-end: the
// verifier bootstraps from an inline X.509 bundle, authenticates the live TLS bundle
// endpoint by its X.509-SVID (deny-closed AuthorizeID), fetches the JWT bundle, and
// verifies a federated JWT-SVID against it.
func TestVerifyHTTPSSpiffeProfile(t *testing.T) {
	const govTD = "gov.example"
	const endpointID = "spiffe://gov.example/bundle-endpoint"
	gov, err := spiffeid.TrustDomainFromString(govTD)
	if err != nil {
		t.Fatal(err)
	}

	// The federated JWT-SVID and the JWT authority that the endpoint will serve.
	token, jwtPub := mintJWTSVID(t, "g1", claimsForTD(govTD, "/workload/svc", "anthropic-wif"))

	// The bundle the endpoint SERVES carries the JWT authority that signed the token
	// (the verifier needs only this to verify the federated token after the fetch).
	served := spiffebundle.New(gov)
	if err := served.AddJWTAuthority("g1", jwtPub); err != nil {
		t.Fatal(err)
	}
	servedJSON := marshalBundle(t, served)

	run := func(t *testing.T, serverSpiffeID string, wantOK bool) {
		// The TLS endpoint presents an X.509-SVID for serverSpiffeID, signed by a CA
		// the verifier will trust via its bootstrap bundle.
		endpointURL, caCert := spiffeTLSServer(t, serverSpiffeID, servedJSON)

		// Bootstrap: an inline bundle for gov.example carrying ONLY the endpoint CA's
		// X.509 authority, so the verifier can authenticate the endpoint but must FETCH
		// it to learn the JWT authority (forcing the real https_spiffe path).
		bootstrap := spiffebundle.New(gov)
		bootstrap.AddX509Authority(caCert)

		v, err := NewVerifier(VerifierConfig{
			Audience:      "anthropic-wif",
			InlineBundles: map[string]string{govTD: marshalBundle(t, bootstrap)},
			Federations: []FederationEndpoint{{
				TrustDomain: govTD, BundleEndpointURL: endpointURL,
				Profile: ProfileHTTPSSpiffe, EndpointSpiffeID: endpointID,
			}},
		}, nil)
		if err != nil {
			t.Fatalf("NewVerifier: %v", err)
		}
		v.now = svidClock

		_, err = v.Verify(context.Background(), token)
		if wantOK && err != nil {
			t.Fatalf("https_spiffe verify should succeed, got %v", err)
		}
		if !wantOK && err == nil {
			t.Fatal("https_spiffe verify should be rejected (endpoint id mismatch)")
		}
	}

	t.Run("endpoint id matches", func(t *testing.T) { run(t, endpointID, true) })
	t.Run("endpoint id mismatch rejected", func(t *testing.T) {
		run(t, "spiffe://gov.example/some-other-workload", false)
	})
}

// TestVerifyHTTPSSpiffeReFetch proves the endpoint stays authenticatable across a
// SECOND fetch (key rotation): the first fetch replaces fed[gov] with a JWT-only
// served bundle that does NOT re-carry the endpoint CA, so the re-fetch's TLS
// handshake must rely on the IMMUTABLE bootstrap X.509 store — the regression the
// reviewer found when bootstrap and the live cache were the same Set.
func TestVerifyHTTPSSpiffeReFetch(t *testing.T) {
	const govTD = "gov.example"
	const endpointID = "spiffe://gov.example/bundle-endpoint"
	gov := mustTD(t, govTD)

	tok1, pub1 := mintJWTSVID(t, "g1", claimsForTD(govTD, "/workload/a", "anthropic-wif"))
	tok2, pub2 := mintJWTSVID(t, "g2", claimsForTD(govTD, "/workload/b", "anthropic-wif"))

	// Two successive served bundles, both JWT-only (no X.509): the first carries g1,
	// the second (post-rotation) carries g2. Neither re-carries the endpoint CA.
	b1 := spiffebundle.New(gov)
	if err := b1.AddJWTAuthority("g1", pub1); err != nil {
		t.Fatal(err)
	}
	b2 := spiffebundle.New(gov)
	if err := b2.AddJWTAuthority("g2", pub2); err != nil {
		t.Fatal(err)
	}
	endpointURL, caCert := spiffeTLSServer(t, endpointID, marshalBundle(t, b1), marshalBundle(t, b2))

	bootstrap := spiffebundle.New(gov)
	bootstrap.AddX509Authority(caCert)

	v, err := NewVerifier(VerifierConfig{
		Audience:      "anthropic-wif",
		InlineBundles: map[string]string{govTD: marshalBundle(t, bootstrap)},
		Federations: []FederationEndpoint{{
			TrustDomain: govTD, BundleEndpointURL: endpointURL,
			Profile: ProfileHTTPSSpiffe, EndpointSpiffeID: endpointID,
		}},
	}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	v.now = svidClock

	// First fetch authenticates via the bootstrap CA and learns g1.
	if _, err := v.Verify(context.Background(), tok1); err != nil {
		t.Fatalf("first https_spiffe verify: %v", err)
	}
	// tok2 (g2) misses the cached bundle → re-fetch. The re-fetch must STILL
	// authenticate the endpoint (bootstrap X.509 survived the first store) and learn g2.
	if _, err := v.Verify(context.Background(), tok2); err != nil {
		t.Fatalf("re-fetch https_spiffe verify failed — the bootstrap X.509 root must survive a store: %v", err)
	}
}

func mustTD(t *testing.T, name string) spiffeid.TrustDomain {
	t.Helper()
	td, err := spiffeid.TrustDomainFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	return td
}
