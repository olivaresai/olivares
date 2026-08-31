// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
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
	dec    *claudeHookDecider
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

func newHookLedgerFixture(t *testing.T, policy hookPolicyDoc) *hookLedgerFixture {
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
	dec := &claudeHookDecider{
		tenants: map[model.TenantID]resolvedTenant{
			tenant: {tenant: tenant, policy: policy},
		},
		authr: hookLedgerAuthenticator{principal: principal},
		store: st,
		log:   discardLog(),
	}
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

func verifyHookLedger(t *testing.T, f *hookLedgerFixture) {
	t.Helper()
	err := f.store.View(context.Background(), f.tenant, func(sc store.Scope) error {
		chain, err := sc.Audit().Verify(context.Background(), 0)
		if err != nil {
			return err
		}
		if !chain.OK {
			t.Fatalf("hook ledger hash chain failed verification at seq %d: %s", chain.BreakAt, chain.Reason)
		}
		sigs, err := audit.VerifyEvents(context.Background(), sc.Audit(), f.pub)
		if err != nil {
			return err
		}
		if !sigs.OK || sigs.Events == 0 || sigs.Events != sigs.Signed {
			t.Fatalf("hook ledger event signatures failed offline verification: %+v", sigs)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify hook ledger: %v", err)
	}
}

func assertSingleHookLedgerDecision(t *testing.T, f *hookLedgerFixture, in claude.HookDecisionInput, wantDecision string) claude.HookDecisionResult {
	t.Helper()
	before := hookLedgerHead(t, f.store, f.tenant)
	res, err := f.dec.Decide(context.Background(), in, "test-bearer")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Permission != wantDecision {
		t.Fatalf("permission = %q (%s), want %q", res.Permission, res.Reason, wantDecision)
	}
	events := hookLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1)
	if len(events) != 1 {
		t.Fatalf("new hook ledger events = %d, want exactly 1", len(events))
	}
	ev := events[0]
	if ev.Action != "hook.tool."+wantDecision {
		t.Fatalf("hook ledger action = %q, want %q", ev.Action, "hook.tool."+wantDecision)
	}
	if len(ev.PayloadHash) != sha256.Size {
		t.Fatalf("hook ledger payload hash length = %d, want %d", len(ev.PayloadHash), sha256.Size)
	}
	if len(ev.Sig) != ed25519.SignatureSize {
		t.Fatalf("hook ledger signature length = %d, want %d", len(ev.Sig), ed25519.SignatureSize)
	}
	if ev.Seq != before.Seq+1 || !bytes.Equal(ev.PrevHash, before.Hash) {
		t.Fatalf("hook ledger event is not the next chained entry: seq=%d want=%d", ev.Seq, before.Seq+1)
	}
	wantHash := hookDecisionHash(
		in.Event, in.Tool, in.ResourceKind, in.ResourceRef, in.Mode, in.PlanHash,
		wantDecision, res.PrincipalActor, res.PolicyVersion,
	)
	if !bytes.Equal(ev.PayloadHash, wantHash) {
		t.Fatalf("hook ledger payload hash = %x, want %x", ev.PayloadHash, wantHash)
	}
	if ev.ActorKind != model.ActorAgent {
		t.Fatalf("hook ledger actor kind = %q, want %q", ev.ActorKind, model.ActorAgent)
	}
	verifyHookLedger(t, f)
	return res
}

func TestHookDecisionPolicyDenyIsSignedAndChained(t *testing.T) {
	policy := hookPolicyDoc{
		Version: "hook-policy/deny-v1",
		Default: claude.DecisionAllow,
		Rules: []hookPolicyRule{{
			Tool: "Bash", Decision: claude.DecisionDeny, Reason: "shell denied by policy",
		}},
	}
	f := newHookLedgerFixture(t, policy)
	in := hookLedgerInput(f.tenant, "Bash", hookResourceKindShell, "bash", "write")
	assertSingleHookLedgerDecision(t, f, in, claude.DecisionDeny)
}

func TestHookDecisionAllowIsSignedAndChained(t *testing.T) {
	f := newHookLedgerFixture(t, hookPolicyDoc{
		Version: "hook-policy/allow-v1",
		Default: claude.DecisionAllow,
	})
	in := hookLedgerInput(f.tenant, "Read", hookResourceKindFile, "/srv/acme/README.md", "read")
	assertSingleHookLedgerDecision(t, f, in, claude.DecisionAllow)
}

func TestHookDecisionDelegationIsSealedInLedgerMeta(t *testing.T) {
	f := newHookLedgerFixture(t, hookPolicyDoc{
		Version: "hook-policy/delegated-v1",
		Default: claude.DecisionAllow,
	})
	actAs := model.ID("user-on-behalf-of")
	principal := auth.ScopedPrincipal(model.NewID(), "delegated-hook-agent", f.tenant, auth.RoleEditor).WithActAs(actAs)
	f.dec.authr = hookLedgerAuthenticator{principal: principal}

	before := hookLedgerHead(t, f.store, f.tenant)
	in := hookLedgerInput(f.tenant, "Read", hookResourceKindFile, "/srv/acme/README.md", "read")
	res, err := f.dec.Decide(context.Background(), in, "delegated-bearer")
	if err != nil || res.Permission != claude.DecisionAllow {
		t.Fatalf("delegated Decide = (%q, %v), want allow", res.Permission, err)
	}

	events := canonicalLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1)
	if len(events) != 1 {
		t.Fatalf("new hook ledger events = %d, want exactly 1", len(events))
	}
	if got := events[0].meta["is_delegated"]; got != true {
		t.Fatalf("hook ledger is_delegated = %#v, want true", got)
	}
	if got := events[0].meta["act_as"]; got != actAs.String() {
		t.Fatalf("hook ledger act_as = %#v, want %q", got, actAs)
	}
	wantHash := hookDecisionHash(
		in.Event, in.Tool, in.ResourceKind, in.ResourceRef, in.Mode, in.PlanHash,
		claude.DecisionAllow, res.PrincipalActor, res.PolicyVersion,
	)
	if !bytes.Equal(events[0].event.PayloadHash, wantHash) {
		t.Fatalf("delegation changed the v1 hook PayloadHash preimage: got %x want %x", events[0].event.PayloadHash, wantHash)
	}
	verifyHookLedger(t, f)
}

func TestHookDecisionLedgerFailureIsDenyClosed(t *testing.T) {
	t.Run("allow is downgraded to deny", func(t *testing.T) {
		f := newHookLedgerFixture(t, hookPolicyDoc{Default: claude.DecisionAllow})
		f.dec.store = nil
		in := hookLedgerInput(f.tenant, "Read", hookResourceKindFile, "/srv/acme/README.md", "read")
		res, err := f.dec.Decide(context.Background(), in, "test-bearer")
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if res.Permission != claude.DecisionDeny || !strings.Contains(res.Reason, "evidence unavailable") {
			t.Fatalf("ALLOW without ledger = %q (%s), want evidence-unavailable DENY", res.Permission, res.Reason)
		}
	})

	t.Run("deny remains deny", func(t *testing.T) {
		f := newHookLedgerFixture(t, hookPolicyDoc{Default: claude.DecisionDeny})
		f.dec.store = nil
		in := hookLedgerInput(f.tenant, "Bash", hookResourceKindShell, "bash", "write")
		res, err := f.dec.Decide(context.Background(), in, "test-bearer")
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if res.Permission != claude.DecisionDeny {
			t.Fatalf("DENY without ledger = %q (%s), want DENY", res.Permission, res.Reason)
		}
		if strings.Contains(res.Reason, "evidence unavailable") {
			t.Fatalf("failed DENY anchor replaced the policy reason: %q", res.Reason)
		}
	})
}

func TestHookDecisionAskDoesNotAddHookLedgerEntry(t *testing.T) {
	h := newHarness(t)
	token := h.firmAgentToken(t, "agent-hook-ledger-ask@e2e.test")
	f := newHookPEPFixture(t, h, hookPolicyDoc{
		Version: "hook-policy/ask-v1",
		Default: claude.DecisionAsk,
	}, false, fixedEval{allow: true}, true)
	in := hookLedgerInput(f.tenant, "Bash", hookResourceKindShell, "bash", "write")
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
