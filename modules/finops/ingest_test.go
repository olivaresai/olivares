// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestIngestWritesLedgerAndReadModel(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 1234, baseTime))

	if got := countCosts(t, st, tenant); got != 1 {
		t.Fatalf("cost records = %d, want 1", got)
	}
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		// Canonical CostRecord carries the resolved provider/model ids and figures.
		costs, _, err := sc.Costs().List(context.Background(), model.Query{})
		if err != nil {
			return err
		}
		c := costs[0]
		if c.CostMicroUSD != 1234 || c.InputTokens != 100 || c.OutputTokens != 50 {
			t.Errorf("cost record figures = %+v", c)
		}
		if c.ProviderID.IsZero() || c.ModelID.IsZero() {
			t.Errorf("cost record missing attribution ids: %+v", c)
		}
		if c.Currency != "USD" {
			t.Errorf("currency = %q", c.Currency)
		}
		// Read-model row exists for analytics.
		repo, _ := sc.Ext(costSampleKind)
		rows, _, err := repo.List(context.Background(), model.Query{})
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].String(colModelRef) != "claude-opus-4-8" {
			t.Errorf("read-model rows = %+v", rows)
		}
		// Provider + model were materialized.
		provs, _, _ := sc.Providers().List(context.Background(), model.Query{})
		if len(provs) != 1 || provs[0].Name != "anthropic" {
			t.Errorf("providers = %+v", provs)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIngestIsDeduped(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	c := mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 1234, baseTime)
	m.ingest(t, tenant, c)
	m.ingest(t, tenant, c) // exact re-delivery (at-least-once)
	if got := countCosts(t, st, tenant); got != 1 {
		t.Errorf("cost records after re-delivery = %d, want 1 (deduped)", got)
	}
}

func TestIngestDistinctSamplesBothCounted(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 1234, baseTime))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 1234, baseTime.Add(1)))
	if got := countCosts(t, st, tenant); got != 2 {
		t.Errorf("cost records = %d, want 2 (distinct instants)", got)
	}
}

// TestIngestRepulledBucketUpserts covers the cumulative/open-bucket re-pull: the same
// bucket (same natural key) re-delivered with a GROWN value must REPLACE the row, not
// add a second one — otherwise repeated polling of today's open bucket double-counts.
func TestIngestRepulledBucketUpserts(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	// Same bucket (same model/instant/dims), value grows across polls: 600 → 1000.
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 600, baseTime))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 160, 80, 1000, baseTime))

	// Exactly ONE ledger row and ONE read-model row, holding the LATEST value.
	if got := countCosts(t, st, tenant); got != 1 {
		t.Fatalf("cost records after re-pull = %d, want 1 (upsert, not append)", got)
	}
	var spend spendResponse
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		spend, e = spendByDimension(context.Background(), sc, "model", time.Time{}, false, time.Time{}, false)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if spend.TotalMicroUSD != 1000 {
		t.Errorf("spend after re-pull = %d, want 1000 (latest value, not 600+1000)", spend.TotalMicroUSD)
	}
	// The canonical ledger tracks the read-model (latest value, not first-seen).
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		costs, _, _ := sc.Costs().List(context.Background(), model.Query{})
		if costs[0].CostMicroUSD != 1000 || costs[0].InputTokens != 160 {
			t.Errorf("ledger after re-pull = %+v, want cost 1000 / input 160", costs[0])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIngestAttributesSessionAgentLabels(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()

	// Seed an agent (with team/project labels) and a session that runs as it.
	var sessionExt string
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(ctx, model.Agent{
			Name: "ci-bot", Kind: "claude-code", ExternalID: "agent-ci", Status: model.StatusActive,
			Labels: map[string]any{"team": "platform", "project": "atlas"},
		})
		if err != nil {
			return err
		}
		s, err := sc.Sessions().Create(ctx, model.Session{
			AgentID: a.ID, ExternalID: "sess-99", State: model.SessionRunning,
		})
		sessionExt = s.ExternalID
		return err
	}); err != nil {
		t.Fatal(err)
	}

	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", sessionExt, 10, 5, 500, baseTime))

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		costs, _, _ := sc.Costs().List(ctx, model.Query{})
		c := costs[0]
		if c.SessionID.IsZero() || c.AgentID.IsZero() {
			t.Errorf("cost not attributed to session/agent: %+v", c)
		}
		if c.Metadata["team"] != "platform" || c.Metadata["project"] != "atlas" {
			t.Errorf("cost metadata team/project = %v", c.Metadata)
		}
		repo, _ := sc.Ext(costSampleKind)
		rows, _, _ := repo.List(ctx, model.Query{})
		if rows[0].String(colTeam) != "platform" || rows[0].String(colAgentRef) != "agent-ci" {
			t.Errorf("read-model attribution = %+v", rows[0])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIngestOperatorLabelsSeedTeamProjectAndAgentWins(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()

	// A sample with NO resolvable session: the operator-supplied labels (e.g. OTEL_RESOURCE_ATTRIBUTES) seed Team/Project and the rest of the labels
	// land on the ledger metadata under the label_ prefix.
	c := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 500, baseTime)
	c.Labels = map[string]string{"team": "payments", "project": "atlas", "cost_center": "cc-42"}
	m.ingest(t, tenant, c)

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		costs, _, _ := sc.Costs().List(ctx, model.Query{})
		if costs[0].Metadata["team"] != "payments" || costs[0].Metadata["project"] != "atlas" {
			t.Errorf("ledger team/project from labels = %v", costs[0].Metadata)
		}
		if costs[0].Metadata["label_cost_center"] != "cc-42" {
			t.Errorf("non-promoted label not on ledger metadata: %v", costs[0].Metadata)
		}
		repo, _ := sc.Ext(costSampleKind)
		rows, _, _ := repo.List(ctx, model.Query{})
		if rows[0].String(colTeam) != "payments" || rows[0].String(colProject) != "atlas" {
			t.Errorf("read-model team/project from labels = %+v", rows[0])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// When the session's agent carries CURATED labels, they outrank the sample's
	// operator-supplied ones — but a label the agent does NOT carry keeps the
	// telemetry-seeded value (never blanked).
	var sessionExt string
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(ctx, model.Agent{
			Name: "ci-bot", Kind: "claude-code", ExternalID: "agent-lbl", Status: model.StatusActive,
			Labels: map[string]any{"team": "platform"}, // no project label
		})
		if err != nil {
			return err
		}
		s, err := sc.Sessions().Create(ctx, model.Session{
			AgentID: a.ID, ExternalID: "sess-lbl", State: model.SessionRunning,
		})
		sessionExt = s.ExternalID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	c2 := mkCost("anthropic", "claude-opus-4-8", sessionExt, 10, 5, 500, baseTime)
	c2.Labels = map[string]string{"team": "payments", "project": "atlas"}
	m.ingest(t, tenant, c2)

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, _ := sc.Ext(costSampleKind)
		rows, _, _ := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colSessionRef, sessionExt)}})
		if len(rows) != 1 {
			t.Fatalf("rows for %s = %d, want 1", sessionExt, len(rows))
		}
		if rows[0].String(colTeam) != "platform" {
			t.Errorf("agent label must outrank sample label: team = %q", rows[0].String(colTeam))
		}
		if rows[0].String(colProject) != "atlas" {
			t.Errorf("missing agent label must not blank the sample label: project = %q", rows[0].String(colProject))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
