// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// TestPaperclipWiredAsSourceAndRoster pins both registrations of the observe-only
// Paperclip connector: the source kind that carries runs and cost, and the roster
// provider that carries companies and agents. A dropped case would make the kind
// an unknown-kind no-op in OLIVARES_SOURCES_CONFIG.
func TestPaperclipWiredAsSourceAndRoster(t *testing.T) {
	const want = "olivares.paperclip"

	src, ok := buildInProcSource("paperclip")
	if !ok || src == nil {
		t.Fatal("paperclip is not a wired in-process source kind")
	}
	if got := src.Descriptor().Name; got != want {
		t.Errorf("source Descriptor.Name = %q, want %q", got, want)
	}

	prov, conn, ok := buildRosterProvider("paperclip")
	if !ok || prov == nil || conn == nil {
		t.Fatal("paperclip is not a wired roster provider")
	}
	if got := conn.Descriptor().Name; got != want {
		t.Errorf("roster Descriptor.Name = %q, want %q", got, want)
	}

	// The registered roster provider reads the configured Paperclip: a base_url
	// routed through the wiring reaches the server with the board key as a bearer.
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		_, _ = w.Write([]byte(`[{"id":"c1","name":"Acme","status":"active"}]`))
	}))
	defer srv.Close()
	if err := conn.Open(context.Background(), sdk.Config{Settings: map[string]string{"base_url": srv.URL, "api_key": "board-key-1"}}); err != nil {
		t.Fatal(err)
	}
	g, err := prov.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot through the wiring: %v", err)
	}
	if len(g.Collections) != 1 || g.Collections[0].DisplayName != "Acme" {
		t.Errorf("graph = %+v", g)
	}
	if auth != "Bearer board-key-1" || path == "" {
		t.Errorf("request reached %q with Authorization %q", path, auth)
	}

	// The board key is a Secret field, so the connector store seals it and never
	// echoes it back.
	secret := false
	for _, f := range src.Descriptor().ConfigFields {
		if f.Key == "api_key" {
			secret = f.Secret
		}
	}
	if !secret {
		t.Error("api_key must be declared Secret")
	}
}
