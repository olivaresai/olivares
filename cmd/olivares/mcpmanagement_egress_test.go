// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagedMCPRejectsRedirectAndWrongDestination(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
	defer target.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL+"/secret-location")
		w.WriteHeader(307)
	}))
	defer first.Close()
	c := first.Client()
	c.Transport = managedMCPTransport{inner: c.Transport, endpoint: first.URL}
	req, _ := http.NewRequest("POST", first.URL, strings.NewReader(`{}`))
	_, err := c.Do(req)
	if err == nil || hits != 0 || strings.Contains(err.Error(), "secret-location") {
		t.Fatalf("redirect reached destination or leaked location: hits=%d", hits)
	}
	req, _ = http.NewRequest("POST", target.URL, strings.NewReader(`{}`))
	if _, err = c.Do(req); err == nil || hits != 0 {
		t.Fatal("unconfigured destination admitted")
	}
}
