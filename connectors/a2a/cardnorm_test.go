// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
)

// normCanon runs the full §8.4 payload preparation (decode generic → normalize →
// JCS) over a card base map.
func normCanon(t *testing.T, base map[string]any) []byte {
	t.Helper()
	generic, err := decodeGeneric(mustJSON(t, base))
	if err != nil {
		t.Fatalf("decode generic: %v", err)
	}
	canon, err := jcsCanonical(normalizeCard(generic))
	if err != nil {
		t.Fatalf("jcs: %v", err)
	}
	return canon
}

// TestNormalizeCardSpecWorkedExample reproduces the §8.4.1 worked example: implicit-
// presence fields at their defaults are dropped, REQUIRED fields stay even at their
// defaults ("description":"", "skills":[]), explicitly-set optional bools stay even
// when false.
func TestNormalizeCardSpecWorkedExample(t *testing.T) {
	base := map[string]any{
		"name":        "Example Agent",
		"description": "",      // REQUIRED → kept at default
		"skills":      []any{}, // REQUIRED → kept empty
		"capabilities": map[string]any{ // REQUIRED message
			"streaming":         false, // proto3 optional → presence == explicitly set → kept
			"pushNotifications": false, // idem
		},
		"securitySchemes":      map[string]any{}, // implicit map at default → dropped
		"securityRequirements": []any{},          // implicit repeated at default → dropped
	}
	got := string(normCanon(t, base))
	want := `{"capabilities":{"pushNotifications":false,"streaming":false},"description":"","name":"Example Agent","skills":[]}`
	if got != want {
		t.Errorf("normalized canon:\n got %s\nwant %s", got, want)
	}
}

// TestNormalizeCardRules covers the per-field classes: implicit defaults removed at
// every depth, non-defaults kept, unknown properties kept verbatim.
func TestNormalizeCardRules(t *testing.T) {
	base := map[string]any{
		"name":        "a",
		"description": "d",
		"version":     "1.0.0",
		"supportedInterfaces": []any{map[string]any{
			"url":             "https://a.example.com",
			"protocolBinding": "JSONRPC",
			"protocolVersion": "1.0",
			"tenant":          "", // implicit string at default → dropped
		}},
		"capabilities": map[string]any{
			"streaming": true,
			"extensions": []any{map[string]any{
				"uri":      "https://ext.example.com/x",
				"required": false, // implicit bool at default → dropped
			}},
		},
		"securitySchemes": map[string]any{
			"oauth": map[string]any{"oauth2SecurityScheme": map[string]any{
				"flows": map[string]any{"authorizationCode": map[string]any{
					"authorizationUrl": "https://a/x",
					"tokenUrl":         "https://a/t",
					"scopes":           map[string]any{"s": "d"},
					"pkceRequired":     false, // implicit bool at default → dropped
					"refreshUrl":       "",    // implicit string at default → dropped
				}},
			}},
		},
		"skills":             []any{map[string]any{"id": "s1", "name": "n", "description": "x", "tags": []any{"t"}, "examples": []any{}}},
		"x-custom":           "kept", // unknown → kept verbatim
		"x-zero":             0,      // unknown → kept even at a default-looking value
		"defaultInputModes":  []any{"text/plain"},
		"defaultOutputModes": []any{"text/plain"},
	}
	canon := string(normCanon(t, base))
	for _, dropped := range []string{`"tenant"`, `"pkceRequired"`, `"refreshUrl"`, `"required"`, `"examples"`} {
		if strings.Contains(canon, dropped) {
			t.Errorf("default-valued implicit field %s must be dropped, canon: %s", dropped, canon)
		}
	}
	for _, kept := range []string{`"x-custom":"kept"`, `"x-zero":0`, `"streaming":true`, `"uri":"https://ext.example.com/x"`} {
		if !strings.Contains(canon, kept) {
			t.Errorf("%s must be kept, canon: %s", kept, canon)
		}
	}
}

// signedCardBytesNormalized mints a card signed per the v1.0 §8.4.2 procedure: the
// JWS payload is the JCS canon of the NORMALIZED card (defaults removed), while the
// served card bytes still carry the default-valued properties.
func signedCardBytesNormalized(t *testing.T, priv ed25519.PrivateKey, kid string, base map[string]any) []byte {
	t.Helper()
	generic, err := decodeGeneric(mustJSON(t, base))
	if err != nil {
		t.Fatalf("decode generic: %v", err)
	}
	canon, err := jcsCanonical(normalizeCard(generic))
	if err != nil {
		t.Fatalf("jcs: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: priv},
		(&jose.SignerOptions{}).WithHeader("kid", kid))
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
	full := cloneMap(base)
	full["signatures"] = []any{map[string]any{"protected": parts[0], "signature": parts[2]}}
	return mustJSON(t, full)
}

// cardWithDefaults is a v1.0 card carrying default-valued implicit-presence
// properties (tenant "", pkceRequired false) that a §8.4-conformant signer strips
// before signing — the two canonical forms differ for it.
func cardWithDefaults(name string) map[string]any {
	c := baseCard(name)
	c["supportedInterfaces"] = []any{map[string]any{
		"url":             "https://" + name + ".example.com",
		"protocolBinding": "JSONRPC",
		"protocolVersion": "1.0",
		"tenant":          "",
	}}
	c["securitySchemes"] = map[string]any{
		"oauth": map[string]any{"oauth2SecurityScheme": map[string]any{
			"flows": map[string]any{"authorizationCode": map[string]any{
				"authorizationUrl": "https://auth/x", "tokenUrl": "https://auth/t",
				"scopes": map[string]any{"s": "d"}, "pkceRequired": false,
			}},
		}},
	}
	return c
}

// TestVerifyCardNormalizedSigner: a card signed per the v1.0 procedure (defaults
// stripped before JCS) verifies even though the SERVED bytes carry those defaults —
// the §8.4.3 verification path.
func TestVerifyCardNormalizedSigner(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	card := signedCardBytesNormalized(t, priv, "k1", cardWithDefaults("researcher"))
	if lvl := verifyBytes(t, card, jwks); lvl != trustVerified {
		t.Fatalf("a §8.4-signed card must verify via the normalized payload, got %q", lvl)
	}
}

// TestVerifyCardLiteralSignerFallback: a pre-formalization signer that signed the
// LITERAL card (defaults included) still verifies via the legacy fallback payload.
func TestVerifyCardLiteralSignerFallback(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	card := signedCardBytes(t, priv, "k1", cardWithDefaults("researcher")) // literal canon
	if lvl := verifyBytes(t, card, jwks); lvl != trustVerified {
		t.Fatalf("a literal-canon signature must verify via the fallback payload, got %q", lvl)
	}
}

// TestVerifyCardNormalizedTamperStillFails: normalization must not open a tamper
// hole — changing a NON-default field after a normalized signing still fails.
func TestVerifyCardNormalizedTamperStillFails(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	card := signedCardBytesNormalized(t, priv, "k1", cardWithDefaults("researcher"))
	var obj map[string]any
	if err := json.Unmarshal(card, &obj); err != nil {
		t.Fatal(err)
	}
	obj["description"] = "TAMPERED"
	if lvl := verifyBytes(t, mustJSON(t, obj), jwks); lvl != trustUnverified {
		t.Fatalf("a tampered card must stay unverified under both payload forms, got %q", lvl)
	}
}

// TestFetchJKURequiresHTTPS: the jku URL is card-supplied (untrusted) — a plain-HTTP
// jku is refused before any dial (RFC 7515 §4.1.2 MUST use TLS).
func TestFetchJKURequiresHTTPS(t *testing.T) {
	s := New()
	if _, err := s.fetchJKU(context.Background(), "http://evil.example.com/jwks.json"); err == nil ||
		!strings.Contains(err.Error(), "https") {
		t.Fatalf("a non-HTTPS jku must be refused, got %v", err)
	}
}
