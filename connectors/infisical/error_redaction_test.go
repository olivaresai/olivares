// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package infisical

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginErrorRedactsReflectedSecret(t *testing.T) {
	const canary = "infisical-client-secret-canary"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body loginRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ClientSecret != canary {
			t.Error("client credential was not sent")
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("rejected " + canary))
	}))
	defer s.Close()
	source := &Source{loginURL: s.URL, clientID: "fixture", clientSecret: canary, doer: s.Client()}
	_, err := source.login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatal("provider rejection lost")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatal("login error retained synthetic secret")
	}
}
