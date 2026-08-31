// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/url"
	"os"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"

	"github.com/olivaresai/olivares/sdk"
)

// --- test fakes (injected sources prove rotation transparency without a SPIRE agent) ---

// fakeX509Source returns the SVID currently in svid; mutate it to simulate rotation.
type fakeX509Source struct {
	svid   *x509svid.SVID
	bundle *x509bundle.Bundle
}

func (f *fakeX509Source) GetX509SVID() (*x509svid.SVID, error) { return f.svid, nil }
func (f *fakeX509Source) GetX509BundleForTrustDomain(td spiffeid.TrustDomain) (*x509bundle.Bundle, error) {
	return f.bundle, nil
}

// fakeJWTSource returns the next SVID from a queue and records the requested params,
// so a test proves the wrapper reads the live source every call (no caching).
type fakeJWTSource struct {
	next   []*jwtsvid.SVID
	calls  int
	lastes []string // last requested (audience + extra)
}

func (f *fakeJWTSource) FetchJWTSVID(_ context.Context, p jwtsvid.Params) (*jwtsvid.SVID, error) {
	f.lastes = append([]string{p.Audience}, p.ExtraAudiences...)
	svid := f.next[f.calls%len(f.next)]
	f.calls++
	return svid, nil
}

// mintAnthropicSVID mints a signed JWT-SVID (sub, aud) and parses it back into a
// *jwtsvid.SVID so Marshal() round-trips the raw token (as the real source returns).
func mintAnthropicSVID(t *testing.T, sub string, aud []string) *jwtsvid.SVID {
	t.Helper()
	claims := jwt.Claims{
		Subject:  sub,
		Audience: jwt.Audience(aud),
		Issuer:   "https://oidc.spire.example",
		IssuedAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}
	raw, _ := signedSVID(t, jose.ES256, "k1", claims)
	svid, err := jwtsvid.ParseInsecure(raw, aud)
	if err != nil {
		t.Fatalf("ParseInsecure mint: %v", err)
	}
	return svid
}

// --- BuildAuthorizer: deny-by-default + correct rule behavior ---

func TestBuildAuthorizerDenyByDefault(t *testing.T) {
	if _, err := BuildAuthorizer(AuthorizerConfig{}); err == nil {
		t.Fatal("empty authorizer config must be an error (deny-by-default; never AuthorizeAny)")
	}
	// Ambiguous (two rules) is rejected, not silently merged.
	if _, err := BuildAuthorizer(AuthorizerConfig{AllowID: "spiffe://corp.example/a", AllowTrustDomain: "corp.example"}); err == nil {
		t.Fatal("ambiguous authorizer config (two rules) must be an error")
	}
}

func TestBuildAuthorizerRules(t *testing.T) {
	home := spiffeid.RequireFromString("spiffe://corp.example/workload/api")
	foreign := spiffeid.RequireFromString("spiffe://evil.example/workload/api")
	other := spiffeid.RequireFromString("spiffe://corp.example/other")

	t.Run("trust-domain accepts same, rejects foreign", func(t *testing.T) {
		az, err := BuildAuthorizer(AuthorizerConfig{AllowTrustDomain: "corp.example"})
		if err != nil {
			t.Fatal(err)
		}
		if err := az(home, nil); err != nil {
			t.Errorf("same-domain peer must be authorized: %v", err)
		}
		if err := az(foreign, nil); err == nil {
			t.Error("foreign-domain peer must be rejected (deny-by-default)")
		}
	})
	t.Run("id accepts exact, rejects other", func(t *testing.T) {
		az, err := BuildAuthorizer(AuthorizerConfig{AllowID: home.String()})
		if err != nil {
			t.Fatal(err)
		}
		if err := az(home, nil); err != nil {
			t.Errorf("exact id must be authorized: %v", err)
		}
		if err := az(other, nil); err == nil {
			t.Error("a different id must be rejected")
		}
	})
	t.Run("one-of accepts listed, rejects unlisted", func(t *testing.T) {
		az, err := BuildAuthorizer(AuthorizerConfig{AllowOneOf: []string{home.String(), other.String()}})
		if err != nil {
			t.Fatal(err)
		}
		if err := az(other, nil); err != nil {
			t.Errorf("a listed id must be authorized: %v", err)
		}
		if err := az(foreign, nil); err == nil {
			t.Error("an unlisted id must be rejected")
		}
	})
}

// --- mTLS configs require an explicit authorizer (deny-closed) ---

func TestMTLSConfigRequiresAuthorizer(t *testing.T) {
	w := newWorkload(&fakeX509Source{}, &fakeX509Source{}, &fakeJWTSource{})
	if _, err := w.MTLSClientConfig(nil); err == nil {
		t.Error("MTLSClientConfig must reject a nil authorizer (deny-by-default)")
	}
	if _, err := w.MTLSServerConfig(nil); err == nil {
		t.Error("MTLSServerConfig must reject a nil authorizer (deny-by-default)")
	}
	az, err := BuildAuthorizer(AuthorizerConfig{AllowTrustDomain: "corp.example"})
	if err != nil {
		t.Fatal(err)
	}
	cc, err := w.MTLSClientConfig(az)
	if err != nil || cc == nil {
		t.Fatalf("MTLSClientConfig with an authorizer: cfg=%v err=%v", cc, err)
	}
	sc, err := w.MTLSServerConfig(az)
	if err != nil || sc == nil {
		t.Fatalf("MTLSServerConfig with an authorizer: cfg=%v err=%v", sc, err)
	}
	// A server config must REQUIRE a client cert (mutual auth) and install go-spiffe's
	// own SVID verification: go-spiffe uses RequireAnyClientCert + a VerifyPeerCertificate
	// hook (it verifies the X509-SVID + runs the authorizer itself, rather than Go's
	// default chain check), so both must be present — never an opt-out.
	if sc.ClientAuth != tls.RequireAnyClientCert {
		t.Errorf("server mTLS must require a client cert, got ClientAuth=%v", sc.ClientAuth)
	}
	if sc.VerifyPeerCertificate == nil {
		t.Error("server mTLS must install go-spiffe's SVID/authorizer verification (VerifyPeerCertificate)")
	}
}

// --- rotation transparency: the wrapper always reads the live source ---

func TestWorkloadRotationTransparent(t *testing.T) {
	ctx := context.Background()

	// JWT: two distinct minted SVIDs returned across two fetches → no caching.
	a := mintAnthropicSVID(t, "spiffe://corp.example/wl", []string{AnthropicAudience})
	b := mintAnthropicSVID(t, "spiffe://corp.example/wl", []string{AnthropicAudience})
	jsrc := &fakeJWTSource{next: []*jwtsvid.SVID{a, b}}
	w := newWorkload(&fakeX509Source{}, &fakeX509Source{}, jsrc)

	got1, err := w.FetchJWTSVID(ctx, AnthropicAudience)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := w.FetchJWTSVID(ctx, AnthropicAudience)
	if err != nil {
		t.Fatal(err)
	}
	if got1.Marshal() == got2.Marshal() {
		t.Error("FetchJWTSVID must read the live source each call (got the same token twice)")
	}
	if jsrc.calls != 2 {
		t.Errorf("expected 2 source fetches, got %d", jsrc.calls)
	}
	if len(jsrc.lastes) == 0 || jsrc.lastes[0] != AnthropicAudience {
		t.Errorf("audience not threaded to the source: %v", jsrc.lastes)
	}

	// X.509: mutate the source's SVID; the wrapper reflects it without reconstruction.
	xsrc := &fakeX509Source{svid: &x509svid.SVID{ID: spiffeid.RequireFromString("spiffe://corp.example/v1")}}
	w2 := newWorkload(xsrc, xsrc, &fakeJWTSource{})
	if s, _ := w2.X509SVID(); s.ID.String() != "spiffe://corp.example/v1" {
		t.Fatalf("x509 svid = %q", s.ID.String())
	}
	xsrc.svid = &x509svid.SVID{ID: spiffeid.RequireFromString("spiffe://corp.example/v2")}
	if s, _ := w2.X509SVID(); s.ID.String() != "spiffe://corp.example/v2" {
		t.Errorf("rotated x509 svid not reflected: %q", s.ID.String())
	}
}

// --- live mode off when no Workload API endpoint is configured ---

func TestNewWorkloadFromConfigOffWhenNoSocket(t *testing.T) {
	if _, ok := os.LookupEnv("SPIFFE_ENDPOINT_SOCKET"); ok {
		t.Skip("SPIFFE_ENDPOINT_SOCKET is set in this environment; off-mode not exercisable")
	}
	w, err := NewWorkloadFromConfig(context.Background(), sdk.Config{Settings: map[string]string{}})
	if err != nil {
		t.Fatalf("off-mode must not error: %v", err)
	}
	if w != nil {
		t.Error("with no socket and no env, the live client must be off (nil), keeping offline paths")
	}
}

// --- go-spiffe enforces the X509-SVID single-URI-SAN rule MTLSServerConfig relies on ---

func TestX509SVIDMultiSANRejected(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	u1, _ := url.Parse("spiffe://corp.example/a")
	u2, _ := url.Parse("spiffe://corp.example/b")
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "multi-san"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		URIs:         []*url.URL{u1, u2}, // TWO URI SANs — violates the SPIFFE X509-SVID rule
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x509svid.ParseRaw(der, keyDER); err == nil {
		t.Fatal("a cert with two URI SANs must be rejected as an X509-SVID (exactly one URI SAN required)")
	}
}
