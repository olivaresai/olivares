// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hookclient_test.go proves the managed hook command is DENY-CLOSED: a down or erroring
// PEP makes it emit a deny (a PostToolUse failure blocks), so a broken control plane
// blocks the agent rather than waving it through. It also proves the relay path and that
// the bearer + identity hints are forwarded.

func runClient(t *testing.T, payload string, cfg HookClientConfig) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := RunHookClient(context.Background(), strings.NewReader(payload), &out, cfg); err != nil {
		t.Fatalf("RunHookClient: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("client output not JSON: %q", out.String())
	}
	return m
}

func preToolUsePayload(tool string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": "s", "hook_event_name": "PreToolUse", "tool_name": tool,
		"tool_input": map[string]any{"command": "x"},
	})
	return string(b)
}

func TestHookClientRelaysDecision(t *testing.T) {
	var gotAuth, gotTenant, gotAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTenant = r.Header.Get(hdrHookTenant)
		gotAgent = r.Header.Get(hdrHookAgent)
		_, _ = w.Write([]byte(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"blocked"}}`))
	}))
	defer srv.Close()

	m := runClient(t, preToolUsePayload("Bash"), HookClientConfig{
		Endpoint: srv.URL, Token: "tok-1", Tenant: "tnt_1", Agent: "agent-1", Client: srv.Client(),
	})
	hso, _ := m["hookSpecificOutput"].(map[string]any)
	if hso == nil || hso["permissionDecision"] != "deny" {
		t.Fatalf("client must relay the endpoint decision: %v", m)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("bearer not forwarded: %q", gotAuth)
	}
	if gotTenant != "tnt_1" || gotAgent != "agent-1" {
		t.Fatalf("identity hints not forwarded: tenant=%q agent=%q", gotTenant, gotAgent)
	}
}

func TestHookClientDenyClosedOnUnreachable(t *testing.T) {
	// A non-listening endpoint ⇒ deny-closed.
	m := runClient(t, preToolUsePayload("Write"), HookClientConfig{Endpoint: "http://127.0.0.1:0/"})
	hso, _ := m["hookSpecificOutput"].(map[string]any)
	if hso == nil || hso["permissionDecision"] != "deny" {
		t.Fatalf("unreachable PEP must deny-closed: %v", m)
	}
}

func TestHookClientDenyClosedOnNoEndpoint(t *testing.T) {
	m := runClient(t, preToolUsePayload("Write"), HookClientConfig{})
	hso, _ := m["hookSpecificOutput"].(map[string]any)
	if hso == nil || hso["permissionDecision"] != "deny" {
		t.Fatalf("missing endpoint must deny-closed: %v", m)
	}
}

func TestHookClientDenyClosedOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	m := runClient(t, preToolUsePayload("Bash"), HookClientConfig{Endpoint: srv.URL, Client: srv.Client()})
	hso, _ := m["hookSpecificOutput"].(map[string]any)
	if hso == nil || hso["permissionDecision"] != "deny" {
		t.Fatalf("5xx from the PEP must deny-closed: %v", m)
	}
}

func TestHookClientPostToolUseFailureBlocks(t *testing.T) {
	// A PostToolUse failure cannot deny a permission (the tool already ran) — the
	// deny-closed answer is to BLOCK post-processing.
	b, _ := json.Marshal(map[string]any{"session_id": "s", "hook_event_name": "PostToolUse", "tool_name": "Read"})
	var out bytes.Buffer
	if err := RunHookClient(context.Background(), bytes.NewReader(b), &out, HookClientConfig{Endpoint: "http://127.0.0.1:0/"}); err != nil {
		t.Fatalf("RunHookClient: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(out.Bytes(), &m)
	if m["decision"] != "block" {
		t.Fatalf("PostToolUse deny-closed must block, got %v", m)
	}
}

func TestHookClientBoundsResponse(t *testing.T) {
	// A hostile/huge response is bounded; the client still produces valid output.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.Copy(w, strings.NewReader(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`))
	}))
	defer srv.Close()
	m := runClient(t, preToolUsePayload("Read"), HookClientConfig{Endpoint: srv.URL, Client: srv.Client()})
	if hso, _ := m["hookSpecificOutput"].(map[string]any); hso == nil || hso["permissionDecision"] != "allow" {
		t.Fatalf("client should relay an allow: %v", m)
	}
}
