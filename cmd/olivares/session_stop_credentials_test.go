// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSessionCredentialRemembersStopBeforeNextUse(t *testing.T) {
	for _, scenario := range []string{"estate", "agent", "unrelated-agent"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			tenant := model.TenantID(h.tenantA)
			p, err := h.authr.Authenticate(ctx, h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, p, tenant, "stop-history-"+scenario)
			intent.AgentRef = "session-agent"
			intent.LauncherPrincipal = p
			// A queued launch has no API request principal in its context.
			token, err := c.mint(ctx, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			kind, ref := scenario, intent.AgentRef
			if scenario == "estate" {
				ref = ""
			}
			if scenario == "unrelated-agent" {
				kind, ref = "agent", "another-agent"
			}
			code, raw := h.req("POST", "/v1/m/governance/killswitch", h.adminToken, h.tenantA, map[string]any{"scope_kind": kind, "scope_ref": ref, "reason": "credential lifetime proof"})
			if code != 201 {
				t.Fatalf("engage: %d %s", code, raw)
			}
			var stop struct {
				ID model.ID `json:"id"`
			}
			if err := json.Unmarshal(raw, &stop); err != nil {
				t.Fatal(err)
			}
			// Simulate a completed re-enable using the real persisted row. Never
			// authenticate the bearer while the stop is active: that is the bug.
			if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext("governance.killswitch")
				if err != nil {
					return err
				}
				row, err := repo.Get(ctx, stop.ID)
				if err != nil {
					return err
				}
				row["status"], row["active_guard"] = "reenabled", nil
				_, err = repo.Update(ctx, row)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			_, err = c.Authenticate(ctx, token)
			if scenario == "unrelated-agent" {
				if err != nil {
					t.Fatalf("unrelated stop revoked this agent: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("stopped generation recovered after re-enable")
			}
			if _, _, err := c.ResolveRun(ctx, tenant, intent.RunRef); err == nil {
				t.Fatal("run resolver recovered stopped generation")
			}
			fresh, err := c.mint(ctx, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Authenticate(ctx, fresh); err != nil {
				t.Fatalf("fresh launch after re-enable: %v", err)
			}
			intent.LauncherPrincipal = auth.Principal{}
			if _, err := c.mint(ctx, tenant, intent); err == nil {
				t.Fatal("launch without its pinned principal minted a credential")
			}
		})
	}
}
