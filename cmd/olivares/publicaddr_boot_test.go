// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/webaddr"
)

// ASK THE PRODUCT, NOT THE PARSER.
//
// Everything else in this lot tests a function. This boots the REAL composition
// root, completes setup through the real HTTP surface, logs in, and reads
// publicKey.rp.id out of the registration options the engine actually issues.
// It is the only test here that can tell "resolveWebAuthnRP returns the right
// struct" from "the engine uses it", and those are different claims: the first
// survives a boot that drops the value on the floor.
func bootWithPublicAddr(t *testing.T, declared string) (*engine, func(method, path, token string, body any) (int, map[string]any, string)) {
	t.Helper()
	addr, err := webaddr.Parse("--public-url", declared)
	if err != nil {
		t.Fatalf("Parse(%q): %v", declared, err)
	}
	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test",
		Logger: slog.Default(), PublicAddr: addr,
	})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	h := eng.api.Handler()
	do := func(method, path, token string, body any) (int, map[string]any, string) {
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rdr)
		req.RemoteAddr = "10.0.0.1:1234"
		// The Host a browser would send. Every test below makes it DIFFERENT from
		// the declared address on purpose: that is the only way to tell a derived
		// relying party from a declared one.
		req.Host = "reached-by.example.com"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m, rec.Body.String()
	}
	return eng, do
}

func adminTokenFor(t *testing.T, eng *engine, do func(string, string, string, any) (int, map[string]any, string)) string {
	t.Helper()
	tok, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if code, _, raw := do("POST", "/v1/setup", "", map[string]any{
		"token": tok, "email": "root@x.io", "password": "supersecret1",
	}); code != http.StatusCreated {
		t.Fatalf("setup = %d %s", code, raw)
	}
	code, body, raw := do("POST", "/v1/auth/login", "", map[string]any{
		"email": "root@x.io", "password": "supersecret1",
	})
	if code != http.StatusOK {
		t.Fatalf("login = %d %s", code, raw)
	}
	return body["token"].(string)
}

func registrationRPID(t *testing.T, do func(string, string, string, any) (int, map[string]any, string), token string) (int, string, string) {
	t.Helper()
	code, body, raw := do("POST", "/v1/auth/webauthn/register/options", token, nil)
	if code != http.StatusOK {
		return code, "", raw
	}
	pk, ok := body["publicKey"].(map[string]any)
	if !ok {
		t.Fatalf("registration options have no publicKey envelope: %s", raw)
	}
	rp, ok := pk["rp"].(map[string]any)
	if !ok {
		t.Fatalf("registration options have no rp entity: %s", raw)
	}
	id, _ := rp["id"].(string)
	return code, id, raw
}

// The declared address decides the relying party, and the request's Host does
// not. The two are deliberately different here — without that, a boot that
// ignored PublicAddr entirely would still produce a plausible-looking answer.
func TestBootDerivesTheRelyingPartyFromTheDeclaredAddress(t *testing.T) {
	eng, do := bootWithPublicAddr(t, "https://olivares.example.com:8443")
	token := adminTokenFor(t, eng, do)
	code, id, raw := registrationRPID(t, do, token)
	if code != http.StatusOK {
		t.Fatalf("register options = %d %s", code, raw)
	}
	if id != "olivares.example.com" {
		t.Fatalf("rp.id = %q, want the declared host (the request Host was reached-by.example.com): %s", id, raw)
	}
}

// The compatibility guard, asked of the real engine: declare nothing and the
// relying party is still derived from the request, exactly as before. A test
// that only asserted the row above would pass while the engine ignored the Host
// entirely.
func TestBootWithoutADeclaredAddressStillDerivesFromTheRequest(t *testing.T) {
	eng, do := bootWithPublicAddr(t, "")
	token := adminTokenFor(t, eng, do)
	code, id, raw := registrationRPID(t, do, token)
	if code != http.StatusOK {
		t.Fatalf("register options = %d %s", code, raw)
	}
	if id != "reached-by.example.com" {
		t.Fatalf("rp.id = %q, want the request Host: %s", id, raw)
	}
}

// A declared address that cannot be a relying party refuses the ceremony with
// the typed 503 — it does NOT quietly fall back to the request Host, which here
// is a perfectly usable name and would therefore have hidden the bug.
func TestBootWithAnUnusableDeclaredAddressRefusesRatherThanDerivingOne(t *testing.T) {
	eng, do := bootWithPublicAddr(t, "https://10.0.0.7:8443")
	token := adminTokenFor(t, eng, do)
	code, _, raw := registrationRPID(t, do, token)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("register options = %d %s, want 503", code, raw)
	}
	if !bytes.Contains([]byte(raw), []byte("webauthn_relying_party_unusable")) {
		t.Fatalf("the refusal is not the typed one: %s", raw)
	}
	if bytes.Contains([]byte(raw), []byte("10.0.0.7")) {
		t.Fatalf("the client response discloses the declared address: %s", raw)
	}
}

// And an explicit pin still beats the declared address in the real engine, which
// is what lets an operator serve a subdomain under a registrable parent.
func TestBootPrefersAnExplicitPinOverTheDeclaredAddress(t *testing.T) {
	t.Setenv("OLIVARES_WEBAUTHN_RPID", "example.com")
	t.Setenv("OLIVARES_WEBAUTHN_ORIGINS", "https://olivares.example.com:8443")
	eng, do := bootWithPublicAddr(t, "https://olivares.example.com:8443")
	token := adminTokenFor(t, eng, do)
	code, id, raw := registrationRPID(t, do, token)
	if code != http.StatusOK {
		t.Fatalf("register options = %d %s", code, raw)
	}
	if id != "example.com" {
		t.Fatalf("rp.id = %q, want the pinned parent domain: %s", id, raw)
	}
}
