// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/olivaresai/olivares/sdk/model"
)

// --- fakeSink ----------------------------------------------------------------

type fakeSink struct {
	mu  sync.Mutex
	obs []model.Observation
}

func (f *fakeSink) Emit(_ context.Context, o model.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obs = append(f.obs, o)
	return nil
}

func (f *fakeSink) edges() []model.EdgeObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.EdgeObservation
	for _, o := range f.obs {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeSink) findings() []model.FindingReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.FindingReport
	for _, o := range f.obs {
		if r, ok := o.(model.FindingReport); ok {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeSink) findingsOfKind(kind string) []model.FindingReport {
	var out []model.FindingReport
	for _, r := range f.findings() {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// --- signed Agent Card minting (Ed25519) -------------------------------------

// keypair mints an Ed25519 keypair plus an operator trust-anchor JWKS containing
// the public key under kid.
func keypair(t *testing.T, kid string) (ed25519.PrivateKey, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: pub, KeyID: kid, Algorithm: "EdDSA", Use: "sig"}}}
	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return priv, raw
}

// canonOf reproduces the connector's payload computation: marshal → decode with
// UseNumber → JCS — so a signature minted over it verifies against the connector's
// own canonicalization.
func canonOf(t *testing.T, base map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("marshal base: %v", err)
	}
	generic, err := decodeGeneric(b)
	if err != nil {
		t.Fatalf("decode generic: %v", err)
	}
	canon, err := jcsCanonical(generic)
	if err != nil {
		t.Fatalf("jcs: %v", err)
	}
	return canon
}

// signedCardBytes builds an Agent Card whose `signatures` is a detached JWS over the
// JCS-canonical card-minus-signatures, signed by priv under kid.
func signedCardBytes(t *testing.T, priv ed25519.PrivateKey, kid string, base map[string]any) []byte {
	t.Helper()
	canon := canonOf(t, base)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.EdDSA, Key: priv},
		(&jose.SignerOptions{}).WithHeader("kid", kid),
	)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	jws, err := signer.Sign(canon)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	compact, err := jws.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		t.Fatalf("compact jws should have 3 parts, got %d", len(parts))
	}
	full := cloneMap(base)
	full["signatures"] = []any{map[string]any{"protected": parts[0], "signature": parts[2]}}
	out, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	return out
}

// cloneMap shallow-clones a card map so appending signatures does not mutate base.
func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// baseCard is a minimal but realistic A2A v1.0 Agent Card (without signatures):
// the endpoint lives in supportedInterfaces (the v0.x top-level url/protocolVersion
// no longer exist), securitySchemes use the v1.0 oneof members, and the REQUIRED
// fields (description, defaultInput/OutputModes, skills tags, ...) are present.
func baseCard(name string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": name + " agent",
		"version":     "1.0.0",
		"supportedInterfaces": []any{
			map[string]any{
				"url":             "https://" + name + ".example.com",
				"protocolBinding": "JSONRPC",
				"protocolVersion": "1.0",
			},
		},
		"capabilities": map[string]any{"streaming": true},
		"securitySchemes": map[string]any{
			"oauth": map[string]any{"oauth2SecurityScheme": map[string]any{
				"flows": map[string]any{"authorizationCode": map[string]any{
					"authorizationUrl": "https://auth." + name + ".example.com/authorize",
					"tokenUrl":         "https://auth." + name + ".example.com/token",
					"scopes":           map[string]any{"reports:read": "read reports"},
					"pkceRequired":     true,
				}},
			}},
			"key": map[string]any{"apiKeySecurityScheme": map[string]any{
				"location": "header", "name": "X-API-Key",
			}},
		},
		"defaultInputModes":  []any{"text/plain"},
		"defaultOutputModes": []any{"text/plain"},
		"skills": []any{map[string]any{
			"id": "s1", "name": "summarize", "description": "summarizes documents",
			"tags": []any{"nlp"},
		}},
	}
}

// legacyCard is a pre-1.0 (v0.3-style) Agent Card: top-level url/protocolVersion,
// OpenAPI-style securityScheme `type` discriminators — the lenient-parse fallback
// surface.
func legacyCard(name string) map[string]any {
	return map[string]any{
		"name":            name,
		"description":     name + " agent",
		"url":             "https://" + name + ".example.com",
		"version":         "1.0.0",
		"protocolVersion": "0.3",
		"capabilities":    map[string]any{"streaming": true},
		"securitySchemes": map[string]any{
			"oauth": map[string]any{"type": "oauth2"},
			"key":   map[string]any{"type": "apiKey"},
		},
		"skills": []any{map[string]any{"id": "s1", "name": "summarize"}},
	}
}

// staticFetch returns a fetcher that serves the given name→card-bytes map.
func staticFetch(cards map[string][]byte) func(context.Context, agentSpec) ([]byte, error) {
	return func(_ context.Context, spec agentSpec) ([]byte, error) {
		if b, ok := cards[spec.Name]; ok {
			return b, nil
		}
		return nil, errNoCard
	}
}

var errNoCard = errStr("no card for agent")

type errStr string

func (e errStr) Error() string { return string(e) }
