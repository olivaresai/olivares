// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIErrorRedactsReflectedCredential(t *testing.T) {
	const canary = "github-opaque-canary"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+canary {
			t.Error("credential was not sent")
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("rejected " + canary))
	}))
	defer s.Close()
	source := &Source{token: canary, client: s.Client()}
	_, err := source.apiGet(context.Background(), s.URL+"/fixture", nil)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatal("provider rejection lost")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatal("API error retained synthetic credential")
	}
}
