// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminErrorRedactsReflectedBasicKey(t *testing.T) {
	const canary = "cursor-basic-canary"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != canary || password != "" {
			t.Error("Basic credential was not sent")
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("rejected " + username))
	}))
	defer s.Close()
	err := newClient(s.URL, canary, s.Client()).getJSON(context.Background(), "/teams/members", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatal("provider rejection lost")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatal("Basic key survived in error")
	}
}
