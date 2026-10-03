// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agent365

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTokenErrorRedactsReflectedSecret(t *testing.T) {
	const canary = "form-credential-canary"
	for _, body := range []string{"rejected " + canary, `{"error":"rejected \u0066orm-credential-canary"}`, strings.Repeat(".", (2<<10)-5) + canary} {
		t.Run(body[:8], func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil || r.Form.Get("client_secret") != canary {
					t.Error("client credential was not sent")
				}
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(body))
			}))
			defer s.Close()
			source := &Source{oauthTokenURL: s.URL, clientID: "fixture", clientSecret: canary, doer: s.Client()}
			_, err := source.token(context.Background())
			if err == nil || !strings.Contains(err.Error(), "status 401") {
				t.Fatal("provider rejection lost")
			}
			if strings.Contains(err.Error(), "form-") || strings.Contains(err.Error(), "credential-canary") {
				t.Fatal("token error retained complete or partial synthetic secret")
			}
		})
	}
}

func TestTokenErrorRedactsTransmittedCredential(t *testing.T) {
	for _, credential := range []string{"opaque+fixture", "opaque%fixture", "opaque fixture"} {
		var transmitted string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			form, parseErr := url.ParseQuery(string(body))
			if err != nil || parseErr != nil || form.Get("client_secret") != credential {
				t.Error("fixture did not receive the configured form credential")
			}
			for _, field := range strings.Split(string(body), "&") {
				if strings.HasPrefix(field, "client_secret=") {
					transmitted = strings.TrimPrefix(field, "client_secret=")
				}
			}
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error_description": "denied " + transmitted})
		}))
		source := &Source{oauthTokenURL: server.URL, clientID: "fixture", clientSecret: credential, doer: server.Client()}
		_, err := source.token(context.Background())
		server.Close()
		if transmitted == "" || err == nil || !strings.Contains(err.Error(), "status 401") || strings.Contains(err.Error(), credential) || strings.Contains(err.Error(), transmitted) {
			t.Fatal("token exchange lost the rejection or disclosed the transmitted credential")
		}
	}
}
