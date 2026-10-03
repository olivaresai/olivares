// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// HU-R17: once the Ollama this engine runs answers, its endpoint is a provider in the
// tenant it was started from, with no key, and starting it again does not add a second
// one. Root on FH 033: ONLY that tenant; another tenant adds it in Providers if it wants.
func TestLocalOllamaIsRegisteredOnceAsAProvider(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	register := registerLocalOllama(h.set.sessions, h.st)
	for i := 0; i < 2; i++ {
		if err := register(ctx, p, model.TenantID(h.tenantA), "http://127.0.0.1:11434"); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	records, _, err := h.set.sessions.ListProviderRecords(ctx, model.TenantID(h.tenantA), "active", sessions.ProviderKindOllama, model.Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].BaseURL != "http://127.0.0.1:11434" || records[0].DisplayName != localOllamaName || records[0].SecretRef != "" {
		t.Fatalf("records = %+v, want one keyless %q at the endpoint", records, localOllamaName)
	}
}

// Root on FH 033: started from tenant A, the record is A's alone; tenant B has none.
// A start outside a business tenant registers nothing and says what to do.
func TestLocalOllamaIsRegisteredOnlyInTheStartingTenant(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	register := registerLocalOllama(h.set.sessions, h.st)
	if err := register(ctx, p, model.TenantID(h.tenantA), "http://127.0.0.1:11434"); err != nil {
		t.Fatalf("register from A: %v", err)
	}
	for tenant, want := range map[string]int{h.tenantA: 1, h.tenantB: 0} {
		records, _, err := h.set.sessions.ListProviderRecords(ctx, model.TenantID(tenant), "active", sessions.ProviderKindOllama, model.Query{Limit: 10})
		if err != nil || len(records) != want {
			t.Fatalf("tenant %s has %d Ollama records (%v), want %d", tenant, len(records), err, want)
		}
	}
	if err := register(ctx, p, model.SystemTenantID, "http://127.0.0.1:11434"); err == nil ||
		!strings.Contains(err.Error(), "add its endpoint in Providers") {
		t.Fatalf("a start outside a business tenant = %v, want the sentence", err)
	}
}

// FH 087: an OpenCode launch on the local Ollama uses the record's model list, and
// nobody should have to test the provider by hand first. Registering (at start and
// after each download) reads the endpoint's models into the record.
func TestLocalOllamaRegistrationReadsTheEndpointsModels(t *testing.T) {
	var mu sync.Mutex
	listed := []string{"qwen3:8b"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		models := make([]map[string]string, 0, len(listed))
		for _, name := range listed {
			models = append(models, map[string]string{"name": name})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	}))
	defer srv.Close()
	h := newHarness(t)
	// The harness wires no provider probe; boot always wires this one (boot.go).
	h.set.sessions.UseProviderProbe(newProviderProbe())
	ctx := context.Background()
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	register := registerLocalOllama(h.set.sessions, h.st)
	recordModels := func() []string {
		t.Helper()
		records, _, err := h.set.sessions.ListProviderRecords(ctx, model.TenantID(h.tenantA), "active", sessions.ProviderKindOllama, model.Query{Limit: 10})
		if err != nil || len(records) != 1 {
			t.Fatalf("records = %+v %v", records, err)
		}
		return records[0].Models
	}
	if err := register(ctx, p, model.TenantID(h.tenantA), srv.URL); err != nil {
		t.Fatalf("register at start: %v", err)
	}
	if got := recordModels(); !reflect.DeepEqual(got, []string{"qwen3:8b"}) {
		t.Fatalf("models after start = %v", got)
	}
	mu.Lock()
	listed = append(listed, "llama3.2:1b")
	mu.Unlock()
	if err := register(ctx, p, model.TenantID(h.tenantA), srv.URL); err != nil {
		t.Fatalf("register after a download: %v", err)
	}
	if got := recordModels(); !reflect.DeepEqual(got, []string{"llama3.2:1b", "qwen3:8b"}) {
		t.Fatalf("models after a download = %v", got)
	}
}
