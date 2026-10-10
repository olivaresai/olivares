// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// buildClaudeHookPEPServer is the only place the engine's kill-switch plane and its deny
// recorder reach the hooks PEP: a plane left unset there would turn the stop off for every
// Claude Code tool-call with no failing test, so this drives the server the builder returns.
func TestClaudeHookPEPServerWiresTheEngineKillSwitchAndItsEvidence(t *testing.T) {
	guard := &toggleStopGuard{}
	fixture := newHookSecretTestRun(t, hookSecretCanary, nil, func(eng *engine) {
		eng.killSwitch = guard
		// The fixture substitutes a different stop plane after minting its bearer.
		// Its original history must not stand in for this replacement reader.
		eng.sessionHooks.stopEpoch = nil
		eng.stopDeny = newStopDenyRecorder(eng.store, discardLog())
	})
	call := func() (decision, reason string) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "session_id": "vendor-session", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+fixture.token)
		rec := httptest.NewRecorder()
		fixture.server.Handler.ServeHTTP(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("response not JSON: %q", rec.Body.String())
		}
		if hso, ok := out["hookSpecificOutput"].(map[string]any); ok {
			out = hso
		}
		decision, _ = out["permissionDecision"].(string)
		reason, _ = out["permissionDecisionReason"].(string)
		return decision, reason
	}

	// A session credential's call would otherwise wait for human review; the stop is checked
	// before the disposition and the review, so it answers at once.
	guard.stopped.Store(true)
	if decision, reason := call(); decision != "deny" || !strings.Contains(reason, "emergency stop active") {
		t.Fatalf("under the stop the builder's server must deny; got %q (%s)", decision, reason)
	}

	var denyEvents int
	if err := fixture.h.st.View(context.Background(), fixture.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(e model.AuditEvent) error {
			if e.Action == "security.killswitch.deny" {
				denyEvents++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if denyEvents == 0 {
		t.Fatal("the builder must hand the engine's stop-deny recorder to the PEP: no kill-switch deny reached the ledger")
	}
}
