// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package onepassword

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEventsErrorRedactsReflectedToken(t *testing.T) {
	const canary = "onepassword-token-canary"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+canary {
			t.Error("credential was not sent")
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("rejected " + canary))
	}))
	defer s.Close()
	source := &Source{baseURL: s.URL, token: canary, doer: s.Client()}
	err := source.postJSON(context.Background(), "/fail", usagesRequest{}, nil)
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatal("provider rejection lost")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatal("events error retained synthetic token")
	}
}
