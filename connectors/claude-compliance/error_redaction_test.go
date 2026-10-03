// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudecompliance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeleteErrorRedactsReflectedCredential(t *testing.T) {
	const canary = "compliance-delete-opaque-canary"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != canary {
			t.Error("credential not sent")
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("rejected " + canary))
	}))
	defer s.Close()
	e := &ComplianceEraser{baseURL: s.URL, deleteKey: canary, doer: s.Client()}
	err := e.do(context.Background(), "/fixture")
	if err == nil || !strings.Contains(err.Error(), "status 403") {
		t.Fatal("HTTP rejection lost")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatal("delete diagnostic retained credential")
	}
}
