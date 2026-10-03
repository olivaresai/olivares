// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package schemaregistry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistryErrorRedactsReflectedBasicCredential(t *testing.T) {
	const canary = "registry-password-canary"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		if !ok || password != canary {
			t.Error("credential not sent")
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("rejected " + password))
	}))
	defer s.Close()
	client := NewClient(Options{BaseURL: s.URL, HTTP: s.Client(), Auth: func(r *http.Request) { r.SetBasicAuth("fixture", canary) }})
	_, err := client.Resolve(context.Background(), Reference{IntID: 1})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatal("HTTP rejection lost")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatal("registry diagnostic retained credential")
	}
}
