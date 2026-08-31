// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/audit"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

type mcpLedgerFixture struct {
	store  store.Store
	tenant model.TenantID
	pub    ed25519.PublicKey
}

func newMCPLedgerFixture(t *testing.T) *mcpLedgerFixture {
	t.Helper()
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate MCP audit signing key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("build MCP audit signer: %v", err)
	}
	st, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", SignEvent: signer.SignEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open signed MCP ledger store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{
			Name: "mcp-ledger", Slug: "mcp-ledger", Status: model.StatusActive,
		})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		t.Fatalf("provision MCP ledger tenant: %v", err)
	}
	return &mcpLedgerFixture{store: st, tenant: tenant, pub: pub}
}

func mcpLedgerHead(t *testing.T, st store.Store, tenant model.TenantID) store.HeadRef {
	t.Helper()
	var head store.HeadRef
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var ok bool
		var err error
		head, ok, err = sc.Audit().Head(context.Background())
		if err == nil && !ok {
			return fmt.Errorf("MCP ledger has no tenant genesis event")
		}
		return err
	})
	if err != nil {
		t.Fatalf("read MCP ledger head: %v", err)
	}
	return head
}

func mcpLedgerEventsFrom(t *testing.T, st store.Store, tenant model.TenantID, fromSeq int64) []model.AuditEvent {
	t.Helper()
	var events []model.AuditEvent
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), fromSeq, func(ev model.AuditEvent) error {
			events = append(events, ev)
			return nil
		})
	})
	if err != nil {
		t.Fatalf("walk MCP ledger: %v", err)
	}
	return events
}

func verifyMCPLedger(t *testing.T, f *mcpLedgerFixture) {
	t.Helper()
	err := f.store.View(context.Background(), f.tenant, func(sc store.Scope) error {
		chain, err := sc.Audit().Verify(context.Background(), 0)
		if err != nil {
			return err
		}
		if !chain.OK {
			t.Fatalf("MCP ledger hash chain failed verification at seq %d: %s", chain.BreakAt, chain.Reason)
		}
		sigs, err := audit.VerifyEvents(context.Background(), sc.Audit(), f.pub)
		if err != nil {
			return err
		}
		if !sigs.OK || sigs.Events == 0 || sigs.Events != sigs.Signed {
			t.Fatalf("MCP ledger event signatures failed offline verification: %+v", sigs)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify MCP ledger: %v", err)
	}
}

func assertSingleMCPDecision(t *testing.T, f *mcpLedgerFixture, d mcpc.ToolDecision, wantDecision string) {
	t.Helper()
	before := mcpLedgerHead(t, f.store, f.tenant)
	auditor := mcpGateAuditor{
		log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), store: f.store, tenant: f.tenant,
	}
	auditor.Record(context.Background(), d, sdk.EvidenceBinding{}) // legacy zero-binding surface

	events := mcpLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1)
	if len(events) != 1 {
		t.Fatalf("new MCP ledger events = %d, want exactly 1", len(events))
	}
	ev := events[0]
	if ev.Action != "mcp.tool."+wantDecision {
		t.Fatalf("MCP ledger action = %q, want %q", ev.Action, "mcp.tool."+wantDecision)
	}
	if len(ev.PayloadHash) != sha256.Size {
		t.Fatalf("MCP ledger payload hash length = %d, want %d", len(ev.PayloadHash), sha256.Size)
	}
	if len(ev.Sig) != ed25519.SignatureSize {
		t.Fatalf("MCP ledger signature length = %d, want %d", len(ev.Sig), ed25519.SignatureSize)
	}
	if ev.Seq != before.Seq+1 || !bytes.Equal(ev.PrevHash, before.Hash) {
		t.Fatalf("MCP ledger event is not the next chained entry: seq=%d want=%d", ev.Seq, before.Seq+1)
	}
	wantHash := mcpDecisionHash(
		f.tenant.String(), d.Subject, d.Tool, d.RequiredScope, wantDecision,
		d.ApprovalRef, d.TaskID, d.MCPTag, d.TokenBinding,
	)
	if !bytes.Equal(ev.PayloadHash, wantHash) {
		t.Fatalf("MCP ledger payload hash = %x, want %x", ev.PayloadHash, wantHash)
	}
	if ev.ActorKind != model.ActorAgent {
		t.Fatalf("MCP ledger actor kind = %q, want %q", ev.ActorKind, model.ActorAgent)
	}
	verifyMCPLedger(t, f)
}

func TestMCPGateAuditorAnchorsSignedDecisions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed bool
		want    string
	}{
		{name: "deny", allowed: false, want: "deny"},
		{name: "allow", allowed: true, want: "allow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMCPLedgerFixture(t)
			assertSingleMCPDecision(t, f, mcpc.ToolDecision{
				Tenant: f.tenant.String(), Subject: "agent:mcp-test", Tool: "deploy",
				RequiredScope: "tools:deploy", Allowed: tc.allowed, Reason: "policy verdict",
				ApprovalRef: "approval:123", TaskID: "task:456", MCPTag: "MCP07",
				TokenBinding: "dpop",
			}, tc.want)
		})
	}
}

func TestMCPGateAuditorUsesConfiguredTenantFallback(t *testing.T) {
	f := newMCPLedgerFixture(t)
	assertSingleMCPDecision(t, f, mcpc.ToolDecision{
		Subject: "agent:mcp-fallback", Tool: "read_repository", RequiredScope: "repo:read",
		Allowed: true, Reason: "scope present", MCPTag: "MCP07", TokenBinding: "mtls",
	}, "allow")
}

func TestMCPGateAuditorSealsDelegationInLedgerMeta(t *testing.T) {
	f := newMCPLedgerFixture(t)
	before := mcpLedgerHead(t, f.store, f.tenant)
	auditor := mcpGateAuditor{
		log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), store: f.store, tenant: f.tenant,
	}
	auditor.Record(context.Background(), mcpc.ToolDecision{
		Tenant: f.tenant.String(), Subject: "agent:delegated", IsDelegated: true,
		ActAs: "user:on-behalf-of", Tool: "deploy", RequiredScope: "tools:deploy",
		Allowed: true, Reason: "policy verdict", TokenBinding: "dpop",
	}, sdk.EvidenceBinding{})

	events := canonicalLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1)
	if len(events) != 1 {
		t.Fatalf("new MCP ledger events = %d, want exactly 1", len(events))
	}
	if got := events[0].meta["is_delegated"]; got != true {
		t.Fatalf("MCP ledger is_delegated = %#v, want true", got)
	}
	if got := events[0].meta["act_as"]; got != "user:on-behalf-of" {
		t.Fatalf("MCP ledger act_as = %#v, want %q", got, "user:on-behalf-of")
	}
	wantHash := mcpDecisionHash(
		f.tenant.String(), "agent:delegated", "deploy", "tools:deploy", "allow",
		"", "", "", "dpop",
	)
	if !bytes.Equal(events[0].event.PayloadHash, wantHash) {
		t.Fatalf("delegation changed the v1 MCP PayloadHash preimage: got %x want %x", events[0].event.PayloadHash, wantHash)
	}
	verifyMCPLedger(t, f)
}

func TestMCPGateAuditorLogsEvidenceGapWithoutPanic(t *testing.T) {
	t.Run("nil store", func(t *testing.T) {
		var logs bytes.Buffer
		auditor := mcpGateAuditor{
			log: slog.New(slog.NewTextHandler(&logs, nil)), tenant: model.NewTenantID(),
		}
		auditor.Record(context.Background(), mcpc.ToolDecision{Subject: "agent:nil-store", Tool: "read"}, sdk.EvidenceBinding{})
		if !strings.Contains(logs.String(), "evidence gap") {
			t.Fatalf("missing evidence-gap log: %s", logs.String())
		}
	})

	t.Run("zero tenant", func(t *testing.T) {
		f := newMCPLedgerFixture(t)
		before := mcpLedgerHead(t, f.store, f.tenant)
		var logs bytes.Buffer
		auditor := mcpGateAuditor{
			log: slog.New(slog.NewTextHandler(&logs, nil)), store: f.store,
		}
		auditor.Record(context.Background(), mcpc.ToolDecision{Subject: "agent:zero-tenant", Tool: "read"}, sdk.EvidenceBinding{})
		if !strings.Contains(logs.String(), "evidence gap") {
			t.Fatalf("missing evidence-gap log: %s", logs.String())
		}
		if events := mcpLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1); len(events) != 0 {
			t.Fatalf("new MCP ledger events = %d, want 0 for zero tenant", len(events))
		}
	})
}
