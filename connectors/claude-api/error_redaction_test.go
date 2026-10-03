// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminWriteErrorRedactsReflectedCredential(t *testing.T) {
	const canary = "claude-admin-opaque-canary"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != canary {
			t.Error("credential not sent")
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("rejected " + canary))
	}))
	defer s.Close()
	a := &Actuator{baseURL: s.URL, adminKey: canary, doer: s.Client()}
	err := a.doWithHeaders(context.Background(), http.MethodPost, "/fixture", []byte(`{}`), nil)
	if err == nil || !strings.Contains(err.Error(), "status 403") {
		t.Fatal("HTTP rejection lost")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatal("admin write diagnostic retained credential")
	}
}
