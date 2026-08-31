// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

type asMetadataReceiver struct{}

func (asMetadataReceiver) ValidateAssertion(context.Context, string, string) (auth.EMAResult, error) {
	return auth.EMAResult{}, nil
}

func getASMetadata(t *testing.T, h *harness) (int, http.Header, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, api.OAuthAuthorizationServerMetadataPath, nil)
	req.Host = "cp.example.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, rec.Header(), body, rec.Body.String()
}

func TestAuthorizationServerMetadataWithoutEMA(t *testing.T) {
	h := newHarness(t)
	code, hdr, body, raw := getASMetadata(t, h)
	if code != http.StatusOK {
		t.Fatalf("AS metadata without EMA = %d %s", code, raw)
	}
	if ct := hdr.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if body["issuer"] != "https://cp.example.test" {
		t.Fatalf("issuer without EMA = %v", body["issuer"])
	}
	if body["token_endpoint"] != "https://cp.example.test/v1/auth/token" {
		t.Fatalf("token_endpoint = %v", body["token_endpoint"])
	}
	if _, ok := body["authorization_grant_profiles_supported"]; ok {
		t.Fatalf("unwired EMA must not advertise grant profiles: %s", raw)
	}
	if grants, ok := body["grant_types_supported"]; ok {
		t.Fatalf("unwired EMA must not advertise jwt-bearer grants: %v", grants)
	}
	if got := stringSlice(body["token_endpoint_auth_methods_supported"]); !reflect.DeepEqual(got, []string{"client_secret_basic"}) {
		t.Fatalf("auth methods = %v", got)
	}
	if got := stringSlice(body["response_types_supported"]); len(got) != 0 {
		t.Fatalf("response_types_supported = %v, want []", got)
	}
}

func TestAuthorizationServerMetadataWithEMA(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		grant, err := auth.NewEMAGrant(auth.EMAGrantConfig{
			Receiver:   asMetadataReceiver{},
			SigningKey: priv,
			Issuer:     "https://issuer.example.test",
		}, o.Authenticator)
		if err != nil {
			t.Fatal(err)
		}
		o.EMAGrant = grant
	})
	code, _, body, raw := getASMetadata(t, h)
	if code != http.StatusOK {
		t.Fatalf("AS metadata with EMA = %d %s", code, raw)
	}
	if body["issuer"] != "https://issuer.example.test" {
		t.Fatalf("issuer with EMA = %v", body["issuer"])
	}
	if got := stringSlice(body["grant_types_supported"]); !reflect.DeepEqual(got, []string{auth.GrantTypeJWTBearer}) {
		t.Fatalf("grant_types_supported = %v", got)
	}
	if got := stringSlice(body["authorization_grant_profiles_supported"]); !reflect.DeepEqual(got, []string{"urn:ietf:params:oauth:grant-profile:id-jag"}) {
		t.Fatalf("authorization_grant_profiles_supported = %v", got)
	}
}

func stringSlice(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
