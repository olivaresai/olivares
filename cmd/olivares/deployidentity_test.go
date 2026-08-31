// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// govStub simulates the two governance endpoints the IdentityBinder calls in-process:
// GET /v1/agents (roster lookup) and POST /v1/m/governance/agents/{id}/identity (bind).
type govStub struct {
	shared     bool // the bound identity is shared across agents (=> degraded)
	agentFound bool
	bindCalls  int
}

func (s *govStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" || r.Header.Get("X-Olivares-Tenant") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/agents"):
			if !s.agentFound {
				_, _ = w.Write([]byte(`{"items":[],"has_more":false}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"agent-1","name":"bot","external_id":"bot"}],"has_more":false}`))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/m/governance/agents/") && strings.HasSuffix(r.URL.Path, "/identity"):
			s.bindCalls++
			if !strings.Contains(r.URL.Path, "agent-1") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			body := `{"agent_id":"agent-1","identity_id":"id-1","identity_ref":"nhi:bot","shared":false,"agent_count":1}`
			if s.shared {
				body = `{"agent_id":"agent-1","identity_id":"id-1","identity_ref":"nhi:shared","shared":true,"agent_count":3}`
			}
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func binderFor(t *testing.T, stub *govStub) (*deployIdentityBinder, model.TenantID) {
	t.Helper()
	tid := model.TenantID("t-123")
	b := &deployIdentityBinder{creds: map[model.TenantID]string{tid: "svc-token"}, log: discardLog()}
	b.useHandler(stub.handler())
	return b, tid
}

func TestIdentityBinderFirmWhenUnambiguous(t *testing.T) {
	b, tid := binderFor(t, &govStub{agentFound: true, shared: false})
	got, err := b.EnsureAgentIdentity(context.Background(), tid, "bot", "nhi:bot", false)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Firm || got.IdentityRef != "nhi:bot" {
		t.Fatalf("a single unambiguous binding must be firm, got %+v", got)
	}
}

func TestIdentityBinderDegradedWhenShared(t *testing.T) {
	b, tid := binderFor(t, &govStub{agentFound: true, shared: true})
	got, err := b.EnsureAgentIdentity(context.Background(), tid, "bot", "nhi:shared", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Firm {
		t.Fatalf("a shared identity must NOT be reported firm (honest degradation), got %+v", got)
	}
}

func TestIdentityBinderDegradedWhenAgentUnknown(t *testing.T) {
	stub := &govStub{agentFound: false}
	b, tid := binderFor(t, stub)
	got, err := b.EnsureAgentIdentity(context.Background(), tid, "ghost", "nhi:x", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Firm {
		t.Fatalf("an unknown agent must degrade, got %+v", got)
	}
	if stub.bindCalls != 0 {
		t.Fatalf("a bind must not be attempted for an unresolved agent")
	}
}

func TestIdentityBinderDegradedWhenTenantUnconfigured(t *testing.T) {
	b := &deployIdentityBinder{creds: map[model.TenantID]string{}, log: discardLog()}
	b.useHandler((&govStub{agentFound: true}).handler())
	got, err := b.EnsureAgentIdentity(context.Background(), model.TenantID("other"), "bot", "nhi:bot", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Firm {
		t.Fatalf("a tenant with no binding credential must degrade, never fake firm, got %+v", got)
	}
}

func TestIdentityBinderMintPathFirm(t *testing.T) {
	// mint=true (no identity_ref) with a resolvable, unshared agent must bind firmly.
	b, tid := binderFor(t, &govStub{agentFound: true, shared: false})
	got, err := b.EnsureAgentIdentity(context.Background(), tid, "bot", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Firm {
		t.Fatalf("mint of a single per-agent NHI must be firm, got %+v", got)
	}
}

func TestIdentityBinderNothingToBindDegrades(t *testing.T) {
	// No identity_ref AND mint=false: nothing to bind firmly => degraded, no bind call.
	stub := &govStub{agentFound: true}
	b, tid := binderFor(t, stub)
	got, err := b.EnsureAgentIdentity(context.Background(), tid, "bot", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Firm {
		t.Fatalf("no identity_ref and mint=false must degrade, got %+v", got)
	}
	if stub.bindCalls != 0 {
		t.Fatalf("no bind must be attempted when there is nothing to bind, got %d", stub.bindCalls)
	}
}

func TestNewDeployIdentityBinderNilWhenEmpty(t *testing.T) {
	if b := newDeployIdentityBinder(nil, discardLog()); b != nil {
		t.Fatalf("an unconfigured binder must be nil (keep degraded default), got %v", b)
	}
}
