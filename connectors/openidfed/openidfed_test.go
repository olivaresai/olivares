// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openidfed

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	leafID   = "https://leaf.example"
	imID     = "https://im.example"
	anchorID = "https://ta.example"
	imFetch  = "https://im.example/fetch"
	taFetch  = "https://ta.example/fetch"
)

func fixedClock() time.Time { return time.Unix(1780000000, 0).UTC() }

// entity is a federation entity with a signing key.
type entity struct {
	id      string
	signJWK jose.JSONWebKey
	pubSet  jose.JSONWebKeySet
}

func newEntity(t *testing.T, id, kid string) entity {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return entity{
		id:      id,
		signJWK: jose.JSONWebKey{Key: priv, KeyID: kid, Algorithm: "RS256", Use: "sig"},
		pubSet:  jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &priv.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"}}},
	}
}

func (e entity) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: e.signJWK},
		(&jose.SignerOptions{}).WithType("entity-statement+jwt").WithHeader("kid", e.signJWK.KeyID),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	payload, _ := json.Marshal(claims)
	obj, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	s, _ := obj.CompactSerialize()
	return s
}

func config(e entity, hints []string, fetchEP string) map[string]any {
	c := map[string]any{
		"iss": e.id, "sub": e.id, "iat": 1779990000, "exp": 1780900000,
		"jwks": e.pubSet,
	}
	if len(hints) > 0 {
		c["authority_hints"] = hints
	}
	if fetchEP != "" {
		c["metadata"] = map[string]any{"federation_entity": map[string]any{"federation_fetch_endpoint": fetchEP}}
	}
	return c
}

func subStatement(iss entity, sub entity) map[string]any {
	return map[string]any{
		"iss": iss.id, "sub": sub.id, "iat": 1779990000, "exp": 1780900000,
		"jwks": sub.pubSet,
	}
}

// mockFetcher serves pre-built statements.
type mockFetcher struct {
	configs    map[string]string
	statements map[string]string // key: fetchEP|iss|sub
}

func (m mockFetcher) FetchConfig(_ context.Context, entityID string) (string, error) {
	if s, ok := m.configs[entityID]; ok {
		return s, nil
	}
	return "", http.ErrNoLocation
}

func (m mockFetcher) FetchStatement(_ context.Context, fetchEndpoint, iss, sub string) (string, error) {
	if s, ok := m.statements[fetchEndpoint+"|"+iss+"|"+sub]; ok {
		return s, nil
	}
	return "", http.ErrNoLocation
}

func buildFederation(t *testing.T) (mockFetcher, entity, entity, entity) {
	t.Helper()
	leaf := newEntity(t, leafID, "leaf-k")
	im := newEntity(t, imID, "im-k")
	ta := newEntity(t, anchorID, "ta-k")

	f := mockFetcher{configs: map[string]string{}, statements: map[string]string{}}
	f.configs[leafID] = leaf.sign(t, config(leaf, []string{imID}, ""))
	f.configs[imID] = im.sign(t, config(im, []string{anchorID}, imFetch))
	f.configs[anchorID] = ta.sign(t, config(ta, nil, taFetch))
	f.statements[imFetch+"|"+imID+"|"+leafID] = im.sign(t, subStatement(im, leaf))
	f.statements[taFetch+"|"+anchorID+"|"+imID] = ta.sign(t, subStatement(ta, im))
	return f, leaf, im, ta
}

func TestResolveToTrustAnchor(t *testing.T) {
	f, _, _, _ := buildFederation(t)
	r := NewResolver([]string{anchorID}, f)
	r.now = fixedClock
	chain, err := r.Resolve(context.Background(), leafID)
	if err != nil {
		t.Fatalf("Resolve rejected a valid chain: %v", err)
	}
	if chain.TrustAnchor != anchorID {
		t.Errorf("TrustAnchor = %q, want %q", chain.TrustAnchor, anchorID)
	}
	if len(chain.Statements) == 0 {
		t.Error("empty chain")
	}
}

func TestRejectNoPathToAnchor(t *testing.T) {
	f, _, _, _ := buildFederation(t)
	// Trust an anchor that is NOT on the leaf's authority-hint path.
	r := NewResolver([]string{"https://other-ta.example"}, f)
	r.now = fixedClock
	if _, err := r.Resolve(context.Background(), leafID); err == nil {
		t.Fatal("Resolve accepted a chain that does not reach a configured trust anchor")
	}
}

func TestRejectBadSubordinateSignature(t *testing.T) {
	leaf := newEntity(t, leafID, "leaf-k")
	im := newEntity(t, imID, "im-k")
	ta := newEntity(t, anchorID, "ta-k")
	evil := newEntity(t, imID, "im-k") // different key, same id/kid

	f := mockFetcher{configs: map[string]string{}, statements: map[string]string{}}
	f.configs[leafID] = leaf.sign(t, config(leaf, []string{imID}, ""))
	f.configs[imID] = im.sign(t, config(im, []string{anchorID}, imFetch))
	f.configs[anchorID] = ta.sign(t, config(ta, nil, taFetch))
	// The subordinate statement about the leaf is signed by the WRONG key (evil),
	// not im's published key — the signature must fail to verify.
	f.statements[imFetch+"|"+imID+"|"+leafID] = evil.sign(t, subStatement(im, leaf))
	f.statements[taFetch+"|"+anchorID+"|"+imID] = ta.sign(t, subStatement(ta, im))

	r := NewResolver([]string{anchorID}, f)
	r.now = fixedClock
	if _, err := r.Resolve(context.Background(), leafID); err == nil {
		t.Fatal("Resolve accepted a subordinate statement with an invalid signature")
	}
}

func TestRejectNonSelfIssuedConfig(t *testing.T) {
	leaf := newEntity(t, leafID, "leaf-k")
	f := mockFetcher{configs: map[string]string{}, statements: map[string]string{}}
	// Config claims sub=leaf but iss=someone-else => not self-issued.
	bad := config(leaf, nil, "")
	bad["iss"] = "https://impostor.example"
	f.configs[leafID] = leaf.sign(t, bad)

	r := NewResolver([]string{leafID}, f)
	r.now = fixedClock
	if _, err := r.Resolve(context.Background(), leafID); err == nil {
		t.Fatal("Resolve accepted a non-self-issued entity configuration")
	}
}

func TestAssuranceMapping(t *testing.T) {
	a := MapAssurance([]string{"IAL2", "aal-3", "https://refeds.org/assurance/IAP/high", "urn:foo"})
	if a.IAL != "IAL2" || a.AAL != "AAL3" {
		t.Errorf("explicit levels wrong: %+v", a)
	}
	if a.FAL != "" {
		t.Errorf("FAL should be empty (none declared): %q", a.FAL)
	}
	// RAF/eduPersonAssurance values pass through UNMAPPED (no fabricated NIST mapping).
	found := false
	for _, r := range a.RawAssurance {
		if strings.Contains(r, "refeds.org/assurance") {
			found = true
		}
	}
	if !found {
		t.Errorf("RAF value not surfaced as RawAssurance: %+v", a.RawAssurance)
	}
}

// --- Source wiring via a Doer stub (no network) ---

type doerStub struct{ fetcher mockFetcher }

func (d doerStub) Do(req *http.Request) (*http.Response, error) {
	var body string
	switch {
	case strings.HasSuffix(req.URL.Path, wellKnownPath):
		entity := req.URL.Scheme + "://" + req.URL.Host
		body = d.fetcher.configs[entity]
	default:
		ep := req.URL.Scheme + "://" + req.URL.Host + req.URL.Path
		body = d.fetcher.statements[ep+"|"+req.URL.Query().Get("iss")+"|"+req.URL.Query().Get("sub")]
	}
	status := http.StatusOK
	if body == "" {
		status = http.StatusNotFound
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

func TestSourceResolvedFinding(t *testing.T) {
	f, _, _, _ := buildFederation(t)
	s := New()
	s.doer = doerStub{fetcher: f}
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"entities": leafID, "trust_anchors": anchorID,
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &capture{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.findings()) != 1 || sink.findings()[0].Kind != "openid_federation_resolved" {
		t.Fatalf("want one resolved finding, got %+v", sink.findings())
	}
}

type capture struct{ obs []model.Observation }

func (c *capture) Emit(_ context.Context, o model.Observation) error {
	c.obs = append(c.obs, o)
	return nil
}
func (c *capture) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range c.obs {
		if fr, ok := o.(model.FindingReport); ok {
			out = append(out, fr)
		}
	}
	return out
}
