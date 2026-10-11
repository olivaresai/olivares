// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package mcpgateway

// auditor_tenant_test.go holds the two gateway sites of the per-reader tenant tests
// (cmd/olivares/tenantreaders_test.go has the others): each one REFUSES a present
// but invalid decision tenant, the reserved system tenant included.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ---- site 3: auditor.go enforcedTenant — the decision tenant of an ENFORCED op ----

// TestEnforcedTenantRefusesAnInvalidDecisionTenant pins: evidence is never
// re-attributed to the configured tenant because the decision named something broken.
func TestEnforcedTenantRefusesAnInvalidDecisionTenant(t *testing.T) {
	anchor := model.NewTenantID()
	a := GateAuditor{Log: discardLogger(), Tenant: anchor}

	// CONTROL 1: an EMPTY decision tenant legitimately inherits the anchor.
	if got, ok := a.enforcedTenant(""); !ok || got != anchor {
		t.Fatalf("control: an empty decision tenant must inherit the anchor; got (%q,%v)", got, ok)
	}
	// CONTROL 2: the matching tenant resolves.
	if got, ok := a.enforcedTenant(anchor.String()); !ok || got != anchor {
		t.Fatalf("control: the matching decision tenant must resolve; got (%q,%v)", got, ok)
	}

	for _, bad := range []string{"not-a-uuid", model.SystemTenantID.String(), "00000000-0000-0000-0000-000000000000", model.NewTenantID().String()} {
		if got, ok := a.enforcedTenant(bad); ok {
			t.Errorf("enforcedTenant(%q) resolved to %q; a non-matching or invalid decision tenant must refuse", bad, got)
		}
	}
}

// ---- site 4: auditor.go bestEffortAnchor — the decision tenant of a denial ----

// TestBestEffortAnchorRefusesTheSystemDecisionTenant closes the leg site 4 was missing.
// bestEffortAnchor is reached on a branch that does NOT go through enforcedTenant
// (see Record), so its own check is the only one on that path. It looked
// at the parse error alone, so a decision naming the reserved system tenant would have
// been anchored under it — evidence for a business action filed outside every business
// boundary.
//
// The observable is the anchor's tenant argument, so the test drives a recording store
// double: with a nil store the function returns early and would be green for free.
func TestBestEffortAnchorRefusesTheSystemDecisionTenant(t *testing.T) {
	anchor := model.NewTenantID()

	// CONTROL: a decision naming the anchor itself does reach the store.
	ctrl := &recordingAnchorStore{}
	ac := GateAuditor{Log: discardLogger(), Store: ctrl, Tenant: anchor}
	ac.bestEffortAnchor(context.Background(), mcpc.ToolDecision{Tool: "search", Tenant: anchor.String()})
	if len(ctrl.tenants) == 0 {
		t.Fatal("control: a valid decision tenant must reach the ledger; the assertion below would be vacuous")
	}

	// An UNUSABLE decision tenant must produce NO anchor at all — the loud gap.
	// Asserting only "not under the system tenant" would be too weak: falling back to
	// the configured anchor is precisely the silent re-attribution forbade, and a
	// mutant that dropped the check would survive such an assertion.
	for _, bad := range []string{model.SystemTenantID.String(), "not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		sink := &recordingAnchorStore{}
		as := GateAuditor{Log: discardLogger(), Store: sink, Tenant: anchor}
		as.bestEffortAnchor(context.Background(), mcpc.ToolDecision{Tool: "search", Tenant: bad})
		if len(sink.tenants) != 0 {
			t.Errorf("decision tenant %q: anchored under %v; an unusable decision tenant must be a loud evidence GAP, never re-attributed to the configured anchor", bad, sink.tenants)
		}
	}
}

// recordingAnchorStore records the tenant every Mutate is pinned to and goes no
// further. The tenant argument IS the observable under test (which tenant the decision
// was filed under), so the double never needs to open a Scope; returning an error keeps
// it from pretending to have written anything.
type recordingAnchorStore struct{ tenants []model.TenantID }

var _ store.Store = (*recordingAnchorStore)(nil)

func (s *recordingAnchorStore) Mutate(_ context.Context, tenant model.TenantID, _ func(store.Scope) error) error {
	s.tenants = append(s.tenants, tenant)
	return errAnchorStoreStub
}

func (s *recordingAnchorStore) View(_ context.Context, tenant model.TenantID, _ func(store.Scope) error) error {
	s.tenants = append(s.tenants, tenant)
	return errAnchorStoreStub
}

// Custody records the tenant too. The anchor path uses Mutate today, but a double
// that observed only the doors it currently expects would go blind the moment the
// work moved — and evidence anchoring is exactly the kind of work that belongs on
// the custody door (core/audit checkpointing already moved there).
func (s *recordingAnchorStore) Custody(_ context.Context, tenant model.TenantID, _ func(store.CustodyScope) error) error {
	s.tenants = append(s.tenants, tenant)
	return errAnchorStoreStub
}

// Export records the tenant too, for the same reason Custody does.
func (s *recordingAnchorStore) Export(_ context.Context, tenant model.TenantID, _ func(store.ExportScope) error) error {
	s.tenants = append(s.tenants, tenant)
	return errAnchorStoreStub
}

func (s *recordingAnchorStore) System(context.Context, func(store.SystemScope) error) error {
	return errAnchorStoreStub
}
func (s *recordingAnchorStore) AuthView(context.Context, func(store.AuthScope) error) error {
	return errAnchorStoreStub
}
func (s *recordingAnchorStore) AuthMutate(context.Context, func(store.AuthScope) error) error {
	return errAnchorStoreStub
}
func (s *recordingAnchorStore) Engine() store.Engine       { return store.Engine("") }
func (s *recordingAnchorStore) Ping(context.Context) error { return nil }
func (s *recordingAnchorStore) Leader() store.LeaderElector {
	return nil
}
func (s *recordingAnchorStore) Close() error { return nil }

var errAnchorStoreStub = errors.New("recordingAnchorStore: observation only")
