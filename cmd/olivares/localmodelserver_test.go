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

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// keyedModelServer is a local OpenAI-compatible server that lists its model only to the
// bearer key it currently holds, the way llama-server and vllm do with their API key.
type keyedModelServer struct {
	*httptest.Server
	mu  sync.Mutex
	key string
}

func newKeyedModelServer(t *testing.T) *keyedModelServer {
	t.Helper()
	s := &keyedModelServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		key := s.key
		s.mu.Unlock()
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "gemma-3-1b"}}})
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *keyedModelServer) holdKey(key string) {
	s.mu.Lock()
	s.key = key
	s.mu.Unlock()
}

// withProviderPlane wires what boot wires for the provider plane and the harness leaves out: the
// connection probe and the REAL sealed vault (the engine's secret sealer and store), so a key is
// sealed the way it is in production.
func withProviderPlane(t *testing.T, h *harness) {
	t.Helper()
	sealer, err := newSecretSealer(t.TempDir(), osGetenv)
	if err != nil {
		t.Fatalf("secret sealer: %v", err)
	}
	sessions.WithProviderProbe(newProviderProbe())(h.set.sessions)
	sessions.WithProviderSecretVault(providerSecretVault{store: auth.NewSecretStore(h.st, sealer)})(h.set.sessions)
}

func localServerRecords(t *testing.T, h *harness) []sessions.ProviderRecord {
	t.Helper()
	records, _, err := h.set.sessions.ListProviderRecords(context.Background(), model.TenantID(h.tenantA), "active", sessions.ProviderKindOpenAICompatible, model.Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return records
}

// A llama.cpp or vLLM server the engine runs is an ordinary openai_compatible provider in the
// tenant that started it, holding the key the engine made for this start. Starting it again
// makes a new key; the same record takes it (the server answers only the new one), so no
// second record appears and every profile bound to it keeps working. The record's models are
// read at registration, so an OpenCode launch needs no hand test first.
func TestLocalModelServerIsOneProviderThatFollowsTheServersKey(t *testing.T) {
	srv := newKeyedModelServer(t)
	h := newHarness(t)
	withProviderPlane(t, h)
	ctx := context.Background()
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	register := registerLocalModelServer(h.set.sessions, h.st)
	endpoint := srv.URL + "/v1"
	for _, key := range []string{"olv-0123456789abcdef-first", "olv-0123456789abcdef-second"} {
		srv.holdKey(key)
		if err := register(ctx, p, model.TenantID(h.tenantA), "llama.cpp on this server", endpoint, key); err != nil {
			t.Fatalf("register with %s: %v", key, err)
		}
		records := localServerRecords(t, h)
		if len(records) != 1 || records[0].BaseURL != endpoint || records[0].DisplayName != "llama.cpp on this server" || records[0].SecretRef == "" {
			t.Fatalf("records = %+v, want one openai_compatible record with a sealed key at %s", records, endpoint)
		}
		if records[0].ProbeState != sessions.ProbeOK || !reflect.DeepEqual(records[0].Models, []string{"gemma-3-1b"}) {
			t.Fatalf("record probe = %s %v after key %s, want ok and the server's model (the record holds the server's current key)", records[0].ProbeState, records[0].Models, key)
		}
	}
}

// The port can change between starts; the record follows the server by its name.
func TestLocalModelServerRecordFollowsTheServerToANewAddress(t *testing.T) {
	first, second := newKeyedModelServer(t), newKeyedModelServer(t)
	h := newHarness(t)
	withProviderPlane(t, h)
	ctx := context.Background()
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	register := registerLocalModelServer(h.set.sessions, h.st)
	for i, srv := range []*keyedModelServer{first, second} {
		held := "olv-0123456789abcdef-" + string(rune('a'+i))
		srv.holdKey(held)
		if err := register(ctx, p, model.TenantID(h.tenantA), "vLLM on this server", srv.URL+"/v1", held); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	records := localServerRecords(t, h)
	if len(records) != 1 || records[0].BaseURL != second.URL+"/v1" {
		t.Fatalf("records = %+v, want the one record moved to the new address", records)
	}
}

// A provider a person named like the server's and pointed at a remote gateway is theirs: the
// engine neither rewires it to a local server nor replaces its key.
func TestLocalModelServerNeverTakesOverAProviderAtAnotherAddress(t *testing.T) {
	h := newHarness(t)
	withProviderPlane(t, h)
	ctx := context.Background()
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := h.set.sessions.CreateProviderRecord(ctx, model.TenantID(h.tenantA), sessions.CreateProviderRecordInput{
		Kind: sessions.ProviderKindOpenAICompatible, DisplayName: "llama.cpp on this server", BaseURL: "https://llm.example.com/v1", APIKey: "their-own-gateway-key-0001", Actor: p})
	if err != nil {
		t.Fatal(err)
	}
	register := registerLocalModelServer(h.set.sessions, h.st)
	if err := register(ctx, p, model.TenantID(h.tenantA), "llama.cpp on this server", "http://127.0.0.1:8080/v1", "olv-0123456789abcdef-x"); err == nil ||
		!strings.Contains(err.Error(), "already exists at another address") {
		t.Fatalf("register over their provider = %v, want it refused with the step", err)
	}
	records := localServerRecords(t, h)
	if len(records) != 1 || records[0].Ref != theirs.Ref || records[0].BaseURL != "https://llm.example.com/v1" || records[0].SecretRef != theirs.SecretRef {
		t.Fatalf("records = %+v, want their provider untouched", records)
	}
}

// A same-named provider on another machine of the local network is not one this host's server
// could have made: the engine leaves it and its key alone, like one behind a gateway.
func TestLocalModelServerNeverTakesOverAProviderOnTheLocalNetwork(t *testing.T) {
	h := newHarness(t)
	withProviderPlane(t, h)
	ctx := context.Background()
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := h.set.sessions.CreateProviderRecord(ctx, model.TenantID(h.tenantA), sessions.CreateProviderRecordInput{
		Kind: sessions.ProviderKindOpenAICompatible, DisplayName: "llama.cpp on this server", BaseURL: "http://192.168.1.5:1234/v1", APIKey: "their-own-lan-key-000001", Actor: p})
	if err != nil {
		t.Fatal(err)
	}
	register := registerLocalModelServer(h.set.sessions, h.st)
	if err := register(ctx, p, model.TenantID(h.tenantA), "llama.cpp on this server", "http://127.0.0.1:8080/v1", "olv-0123456789abcdef-x"); err == nil ||
		!strings.Contains(err.Error(), "already exists at another address") {
		t.Fatalf("register over their LAN provider = %v, want it refused", err)
	}
	records := localServerRecords(t, h)
	if len(records) != 1 || records[0].Ref != theirs.Ref || records[0].BaseURL != "http://192.168.1.5:1234/v1" || records[0].SecretRef != theirs.SecretRef {
		t.Fatalf("records = %+v, want their provider untouched", records)
	}
}

// A record whose connection test does not pass would only fail the first OpenCode launch later,
// with a message about models. Registration says so now, with the test's own reason.
func TestLocalModelServerRegistrationSaysWhenTheConnectionTestFails(t *testing.T) {
	srv := newKeyedModelServer(t)
	srv.holdKey("olv-0123456789abcdef-another") // not the key being registered
	h := newHarness(t)
	withProviderPlane(t, h)
	ctx := context.Background()
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	register := registerLocalModelServer(h.set.sessions, h.st)
	err = register(ctx, p, model.TenantID(h.tenantA), "llama.cpp on this server", srv.URL+"/v1", "olv-0123456789abcdef-first")
	if err == nil || !strings.Contains(err.Error(), "connection test did not succeed") {
		t.Fatalf("register = %v, want the failed connection test said", err)
	}
}

// Only a tenant this node serves gets the record; the key has no other home.
func TestLocalModelServerIsRegisteredOnlyInAServedTenant(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	register := registerLocalModelServer(h.set.sessions, h.st)
	if err := register(ctx, p, model.SystemTenantID, "llama.cpp on this server", "http://127.0.0.1:8080/v1", "olv-0123456789abcdef-x"); err == nil ||
		!strings.Contains(err.Error(), "not served") {
		t.Fatalf("a start outside a served tenant = %v, want it refused", err)
	}
	if got := len(localServerRecords(t, h)); got != 0 {
		t.Fatalf("a refused registration left %d records", got)
	}
}

// A person's own provider that happens to sit at the address the server was given has another
// name: it is theirs, and its key stays. The server gets a record of its own.
func TestLocalModelServerNeverTakesOverAProviderThatOnlySharesItsAddress(t *testing.T) {
	srv := newKeyedModelServer(t)
	srv.holdKey("olv-0123456789abcdef-engine")
	h := newHarness(t)
	withProviderPlane(t, h)
	ctx := context.Background()
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := h.set.sessions.CreateProviderRecord(ctx, model.TenantID(h.tenantA), sessions.CreateProviderRecordInput{
		Kind: sessions.ProviderKindOpenAICompatible, DisplayName: "my own vllm", BaseURL: srv.URL + "/v1", APIKey: "their-own-local-key-0001", Actor: p})
	if err != nil {
		t.Fatal(err)
	}
	register := registerLocalModelServer(h.set.sessions, h.st)
	if err := register(ctx, p, model.TenantID(h.tenantA), "llama.cpp on this server", srv.URL+"/v1", "olv-0123456789abcdef-engine"); err != nil {
		t.Fatalf("register: %v", err)
	}
	var kept, made int
	for _, rec := range localServerRecords(t, h) {
		switch {
		case rec.Ref == theirs.Ref && rec.SecretRef == theirs.SecretRef && rec.DisplayName == "my own vllm":
			kept++
		case rec.DisplayName == "llama.cpp on this server" && rec.SecretRef != "" && rec.SecretRef != theirs.SecretRef:
			made++
		}
	}
	if kept != 1 || made != 1 {
		t.Fatalf("records = %+v, want their record untouched (%d) and one of the server's own (%d)", localServerRecords(t, h), kept, made)
	}
}

// At engine start the server comes back under the system principal, not an administrator: the
// sealed vault must accept that actor, or a restarted server would have a stale key in Providers.
func TestLocalModelServerRegistersUnderTheSystemPrincipalOfARestart(t *testing.T) {
	srv := newKeyedModelServer(t)
	h := newHarness(t)
	withProviderPlane(t, h)
	system, err := auth.NewSystemOperator("engine start", "restart the llama.cpp a person started before the engine stopped")
	if err != nil {
		t.Fatal(err)
	}
	register := registerLocalModelServer(h.set.sessions, h.st)
	srv.holdKey("olv-0123456789abcdef-boot")
	if err := register(context.Background(), system, model.TenantID(h.tenantA), "llama.cpp on this server", srv.URL+"/v1", "olv-0123456789abcdef-boot"); err != nil {
		t.Fatalf("register as the system principal: %v", err)
	}
	records := localServerRecords(t, h)
	if len(records) != 1 || records[0].SecretRef == "" || records[0].ProbeState != sessions.ProbeOK {
		t.Fatalf("records = %+v, want one sealed record that tests ok", records)
	}
}
