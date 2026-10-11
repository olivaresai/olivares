// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build !enterprise || !addon_ids

package a2a

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCommunityA2ADelegationUnavailable(t *testing.T) {
	_, err := NewClient(EmitConfig{}).SendMessage(context.Background(), SendSpec{})
	if err == nil || !strings.Contains(err.Error(), "Business") {
		t.Fatalf("Community exposes the A2A sender: %v", err)
	}
}

func TestCommunityA2ADelegationDenyClosedDefaultGate(t *testing.T) {
	var requests atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected delegation", http.StatusInternalServerError)
	}))
	t.Cleanup(peer.Close)
	delegator := NewDelegator(DelegatorConfig{
		Emit: EmitConfig{AllowInsecure: true, Doer: peer.Client()},
		Allowlist: NewAllowlist([]AllowRule{
			{Agent: "peer", Skill: "*", Scopes: []string{"*"}},
		}),
	})
	result, err := delegator.Delegate(t.Context(), DelegateSpec{
		AgentName: "peer", AgentURL: peer.URL, Skill: "summarize", Scope: "reports:read",
	})
	if !errors.Is(err, errBusinessDelegation) {
		t.Fatalf("Community delegation must deny-closed with no gate: %v", err)
	}
	if result.TaskID != "" || result.ResultKind != "" {
		t.Fatalf("refused delegation returned a task: %+v", result)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("deny-closed delegation reached the upstream %d time(s)", got)
	}
}
