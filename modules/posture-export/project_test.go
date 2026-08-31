// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package postureexport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/inventory"
)

// awsKey is a recognized AWS access-key id the redact scrubber catches; we plant it
// in free-form finding/inventory fields to prove the defensive redact pass runs.
const awsKey = "AKIAIOSFODNN7EXAMPLE"

type projHarness struct {
	t      *testing.T
	st     store.Store
	tenant model.TenantID
}

func newProjHarness(t *testing.T) *projHarness {
	t.Helper()
	ctx := context.Background()
	// Register the inventory catalog table (a module table readInventory reads);
	// Findings and AccessEdges are core capabilities, no module schema needed.
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, inventory.New().RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &projHarness{t: t, st: st, tenant: tenant}
}

func (h *projHarness) seedFinding(kind string, sev model.Severity, subjectKind, title string, meta map[string]any) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.st.Mutate(ctx, h.tenant, func(sc store.Scope) error {
		_, e := sc.Findings().Create(ctx, model.Finding{
			Kind: kind, Severity: sev, Status: model.FindingOpen, Source: "module:security",
			SubjectKind: subjectKind, Title: title, DetailHash: []byte{0xaa, 0xbb},
			OccurredAt: model.NewTimestamp(time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)),
			Metadata:   meta,
		})
		return e
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *projHarness) view(fn func(sc store.Scope) error) {
	h.t.Helper()
	if err := h.st.View(context.Background(), h.tenant, fn); err != nil {
		h.t.Fatal(err)
	}
}

// The severity floor is applied IN GO (the column is text, not lexically ordered):
// a floor of "high" keeps only high + critical.
func TestFindingsSeverityFloorInGo(t *testing.T) {
	h := newProjHarness(t)
	h.seedFinding("guardrail", model.SeverityLow, "agent", "low one", nil)
	h.seedFinding("guardrail", model.SeverityHigh, "agent", "high one", nil)
	h.seedFinding("redteam", model.SeverityCritical, "agent", "critical one", nil)

	h.view(func(sc store.Scope) error {
		got, _, err := readFindings(context.Background(), sc, model.SeverityHigh, "")
		if err != nil {
			return err
		}
		if len(got) != 2 {
			t.Fatalf("severity floor high: got %d findings, want 2 (high+critical)", len(got))
		}
		for _, f := range got {
			if severityRank(model.Severity(f.Severity)) < severityRank(model.SeverityHigh) {
				t.Fatalf("floor leaked a %q finding", f.Severity)
			}
		}
		return nil
	})
}

// The category filter matches a finding kind OR subject_kind (there is no category
// column).
func TestFindingsCategoryFilter(t *testing.T) {
	h := newProjHarness(t)
	h.seedFinding("guardrail", model.SeverityHigh, "agent", "g", nil)
	h.seedFinding("redteam", model.SeverityHigh, "mcp_server", "r", nil)

	h.view(func(sc store.Scope) error {
		// by kind
		byKind, _, err := readFindings(context.Background(), sc, "", "guardrail")
		if err != nil {
			return err
		}
		if len(byKind) != 1 || byKind[0].Kind != "guardrail" {
			t.Fatalf("category=guardrail: %+v", byKind)
		}
		// by subject_kind
		bySubject, _, err := readFindings(context.Background(), sc, "", "mcp_server")
		if err != nil {
			return err
		}
		if len(bySubject) != 1 || bySubject[0].SubjectKind != "mcp_server" {
			t.Fatalf("category=mcp_server: %+v", bySubject)
		}
		return nil
	})
}

// A secret that slipped into a free-form finding title or metadata is scrubbed by the
// defensive redact pass before it can leave the box (docs/SECURITY-HARDENING.md).
func TestFindingsRedactDefensivePass(t *testing.T) {
	h := newProjHarness(t)
	h.seedFinding("guardrail", model.SeverityHigh, "agent",
		"leaked key "+awsKey,
		map[string]any{"note": "creds " + awsKey, "count": 3})

	h.view(func(sc store.Scope) error {
		got, _, err := readFindings(context.Background(), sc, "", "")
		if err != nil {
			return err
		}
		if len(got) != 1 {
			t.Fatalf("got %d findings", len(got))
		}
		f := got[0]
		if strings.Contains(f.Title, awsKey) {
			t.Fatalf("title must be redacted, got %q", f.Title)
		}
		if note, _ := f.Metadata["note"].(string); strings.Contains(note, awsKey) {
			t.Fatalf("metadata must be redacted, got %q", note)
		}
		if c, _ := f.Metadata["count"].(float64); c != 3 {
			// non-string metadata passes through unchanged
			t.Fatalf("non-string metadata altered: %v", f.Metadata["count"])
		}
		return nil
	})
}

// Inventory ref/host free-form fields also get the defensive redact pass.
func TestInventoryRedactAndKindFilter(t *testing.T) {
	h := newProjHarness(t)
	ctx := context.Background()
	if err := h.st.Mutate(ctx, h.tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(model.Kind("inventory.catalog_entry"))
		if e != nil {
			return e
		}
		now := model.NewTimestamp(time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)).String()
		_, e = repo.Create(ctx, model.Record{
			"entity_kind": "resource", "entity_id": model.NewID().String(),
			"name": "db", "ref": "dsn " + awsKey, "status": "active",
			"signal_sources": "[]", "hosts": "[]", "occurrence_count": int64(1),
			"first_seen": now, "last_seen": now,
		})
		return e
	}); err != nil {
		t.Fatal(err)
	}

	h.view(func(sc store.Scope) error {
		items, _, err := readInventory(ctx, sc, "resource")
		if err != nil {
			return err
		}
		if len(items) != 1 {
			t.Fatalf("inventory kind filter: got %d, want 1", len(items))
		}
		if strings.Contains(items[0].Ref, awsKey) {
			t.Fatalf("inventory ref must be redacted, got %q", items[0].Ref)
		}
		// A non-matching kind filter returns nothing.
		none, _, err := readInventory(ctx, sc, "agent")
		if err != nil {
			return err
		}
		if len(none) != 0 {
			t.Fatalf("kind=agent should match nothing, got %d", len(none))
		}
		return nil
	})
}

// The drift projection runs against the core access-edge store (no edges → empty,
// never an error).
func TestDriftProjectionEmpty(t *testing.T) {
	h := newProjHarness(t)
	h.view(func(sc store.Scope) error {
		d, err := readDrift(context.Background(), sc)
		if err != nil {
			return err
		}
		if d.UnexpectedCount != 0 || len(d.UnexpectedAccesses) != 0 {
			t.Fatalf("expected empty drift, got %+v", d)
		}
		return nil
	})
}
