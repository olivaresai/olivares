// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

type hookLedgerAuthenticator struct {
	principal auth.Principal
}

func (a hookLedgerAuthenticator) Authenticate(context.Context, string) (auth.Principal, error) {
	return a.principal, nil
}

type hookLedgerFixture struct {
	store  store.Store
	tenant model.TenantID
	pub    ed25519.PublicKey
	dec    *hookpep.Decider
}

type canonicalLedgerEvent struct {
	event model.AuditEvent
	meta  map[string]any
}

func canonicalLedgerEventsFrom(t *testing.T, st store.Store, tenant model.TenantID, fromSeq int64) []canonicalLedgerEvent {
	t.Helper()
	var events []canonicalLedgerEvent
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("ledger does not expose canonical metadata")
		}
		return walker.WalkCanonical(context.Background(), fromSeq, func(ev model.AuditEvent, canonical string, _ []byte) error {
			var meta map[string]any
			if err := json.Unmarshal([]byte(canonical), &meta); err != nil {
				return err
			}
			events = append(events, canonicalLedgerEvent{event: ev, meta: meta})
			return nil
		})
	})
	if err != nil {
		t.Fatalf("walk canonical ledger events: %v", err)
	}
	return events
}

func newHookLedgerFixture(t *testing.T, policy hookpep.PolicyDoc) *hookLedgerFixture {
	t.Helper()
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate audit signing key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("build audit signer: %v", err)
	}
	st, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", SignEvent: signer.SignEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open signed hook ledger store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{
			Name: "hook-ledger", Slug: "hook-ledger", Status: model.StatusActive,
		})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		t.Fatalf("provision hook ledger tenant: %v", err)
	}

	principal := auth.ScopedPrincipal(model.NewID(), "hook-ledger-agent", tenant, auth.RoleEditor)
	dec := newClaudeHookDecider(&hookpep.Decider{
		Tenants: map[model.TenantID]hookpep.ResolvedTenant{
			tenant: hookTenant(t, tenant, false, policy),
		},
		Authr: hookLedgerAuthenticator{principal: principal},
		Store: st,
		Log:   discardLog(),
	})
	return &hookLedgerFixture{store: st, tenant: tenant, pub: pub, dec: dec}
}

func hookLedgerInput(tenant model.TenantID, tool, resourceKind, resourceRef, mode string) claude.HookDecisionInput {
	return claude.HookDecisionInput{
		Event:        "PreToolUse",
		SessionID:    "sess-hook-ledger",
		Tool:         tool,
		ToolUseID:    model.NewID().String(),
		ResourceKind: resourceKind,
		ResourceRef:  resourceRef,
		Mode:         mode,
		PlanHash:     "sha256:hook-ledger-plan",
		Identity:     claude.HookIdentity{Tenant: tenant.String()},
	}
}

func hookLedgerHead(t *testing.T, st store.Store, tenant model.TenantID) store.HeadRef {
	t.Helper()
	var head store.HeadRef
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var ok bool
		var err error
		head, ok, err = sc.Audit().Head(context.Background())
		if err == nil && !ok {
			t.Fatal("hook ledger has no tenant genesis event")
		}
		return err
	})
	if err != nil {
		t.Fatalf("read hook ledger head: %v", err)
	}
	return head
}

func hookLedgerEventsFrom(t *testing.T, st store.Store, tenant model.TenantID, fromSeq int64) []model.AuditEvent {
	t.Helper()
	var events []model.AuditEvent
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), fromSeq, func(ev model.AuditEvent) error {
			events = append(events, ev)
			return nil
		})
	})
	if err != nil {
		t.Fatalf("walk hook ledger: %v", err)
	}
	return events
}

func TestHookDecisionAskDoesNotAddHookLedgerEntry(t *testing.T) {
	h := newHarness(t)
	token := h.firmAgentToken(t, "agent-hook-ledger-ask@e2e.test")
	f := newHookPEPFixture(t, h, hookpep.PolicyDoc{
		Version: "hook-policy/ask-v1",
		Default: claude.DecisionAsk,
	}, false, fixedEval{allow: true}, true)
	in := hookLedgerInput(f.tenant, "Bash", hookpep.ResourceKindShell, "bash", "write")
	before := hookLedgerHead(t, h.st, f.tenant)

	res, err := f.dec.Decide(context.Background(), in, token)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Permission != claude.DecisionAsk {
		t.Fatalf("permission = %q (%s), want ask", res.Permission, res.Reason)
	}

	events := hookLedgerEventsFrom(t, h.st, f.tenant, before.Seq+1)
	foundApproval := false
	for _, ev := range events {
		if strings.HasPrefix(ev.Action, "hook.tool.") {
			t.Fatalf("ASK duplicated its HITL evidence with hook decision entry %q", ev.Action)
		}
		if ev.Action == "governance.approval.create" {
			foundApproval = true
		}
	}
	if !foundApproval {
		t.Fatalf("ASK did not leave the expected HITL approval ledger entry; new events=%v", events)
	}
}
