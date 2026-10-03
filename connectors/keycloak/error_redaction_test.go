// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package keycloak

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

func TestTokenErrorsRedactReflectedSecret(t *testing.T) {
	const canary = "keycloak-client-secret-canary"
	for _, ping := range []bool{false, true} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ping {
				_, password, ok := r.BasicAuth()
				if !ok || password != canary {
					t.Error("client credential was not sent")
				}
			} else if err := r.ParseForm(); err != nil || r.Form.Get("client_secret") != canary {
				t.Error("client credential was not sent")
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("rejected " + canary))
		}))
		source := &Source{tokenURL: s.URL, clientID: "fixture", clientSecret: canary, doer: s.Client()}
		var err error
		if ping {
			_, err = source.pingToken(context.Background())
		} else {
			_, err = source.token(context.Background())
		}
		s.Close()
		if err == nil || !strings.Contains(err.Error(), "status 401") {
			t.Fatal("provider rejection lost")
		}
		if strings.Contains(err.Error(), canary) {
			t.Fatal("token error retained synthetic secret")
		}
	}
}

func TestTokenErrorRedactsTransmittedCredential(t *testing.T) {
	for _, ping := range []bool{false, true} {
		for _, credential := range []string{"opaque+fixture", "opaque%fixture", "opaque fixture"} {
			var transmitted string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if ping {
					_, password, ok := r.BasicAuth()
					if !ok || password != credential {
						t.Error("fixture did not receive the Basic credential")
					}
					transmitted = r.Header.Get("Authorization")
				} else {
					body, err := io.ReadAll(r.Body)
					form, parseErr := url.ParseQuery(string(body))
					if err != nil || parseErr != nil || form.Get("client_secret") != credential {
						t.Error("fixture did not receive the form credential")
					}
					for _, field := range strings.Split(string(body), "&") {
						if strings.HasPrefix(field, "client_secret=") {
							transmitted = strings.TrimPrefix(field, "client_secret=")
						}
					}
				}
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error_description": "denied " + transmitted})
			}))
			source := &Source{tokenURL: server.URL, clientID: "fixture", clientSecret: credential, doer: server.Client()}
			var err error
			if ping {
				_, err = source.pingToken(context.Background())
			} else {
				_, err = source.token(context.Background())
			}
			server.Close()
			if transmitted == "" || err == nil || !strings.Contains(err.Error(), "status 401") || strings.Contains(err.Error(), credential) || strings.Contains(err.Error(), transmitted) {
				t.Fatal("token exchange lost the rejection or disclosed the transmitted credential")
			}
		}
	}
}
