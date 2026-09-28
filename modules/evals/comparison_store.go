// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	comparisonKind        model.Kind = "evals.comparison"
	colComparisonKey                 = "comparison_key"
	colEligible                      = "eligible"
	colComparisonEvidence            = "evidence"
)

func comparisonDeclaration() *model.ColumnDecl {
	identity := model.None("comparison identity hashed for equality, never principal resolution: comparison_identity.go:166")
	projection := model.None("immutable receipt provenance projected as JSON, never principal resolution: comparison_store.go:78")
	decision := model.None("typed comparison diagnostics consumed by gate verdict selection: gate.go:389, gate.go:394")
	mode := model.None("experiment mode selects candidate equality rules: comparison_identity.go:169")
	selection := model.None("selection label controls explicit or pinned baseline resolution: comparison.go:117")
	baseline := model.None("evaluation run reference resolved only in the scoped run repository: comparison.go:131")
	population := model.None("scored population digest compared to the selected fixture population: comparison.go:274")
	observed := model.None("observed provider model compared only to the declared scoring model: comparison.go:282")
	seed := model.None("sampling seed hashes fixture keys to select cases: gate.go:445")
	leaves := []model.LeafDecl{
		model.Leaf("status", decision),
		model.Leaf("reason", decision),
		model.Leaf("mode", mode),
		model.Leaf("selection", selection),
		model.Leaf("baseline_ref", baseline),
		model.Leaf("baseline_evidence_ref", projection),
		model.Leaf("identity.tenant", identity),
		model.Leaf("identity.suite", identity),
		model.Leaf("identity.subject_kind", identity),
		model.Leaf("identity.candidate.subject", identity),
		model.Leaf("identity.candidate.model", identity),
		model.Leaf("identity.candidate.model_source", projection),
		model.Leaf("identity.candidate.variant", identity),
		model.Leaf("identity.protocol.implementation", identity),
		model.Leaf("identity.protocol.version", identity),
		model.Leaf("identity.protocol.provider", identity),
		model.Leaf("identity.protocol.model", identity),
		model.Leaf("identity.protocol.config_digest", identity),
		model.Leaf("identity.rubric_digest", identity),
		model.Leaf("identity.full.digest", identity),
		model.Leaf("identity.selected.digest", identity),
		model.Leaf("identity.seed", seed),
		model.Leaf("identity.sampling", projection),
		model.Leaf("scored.digest", population),
		model.Leaf("changed[]", projection),
		model.Leaf("observed_models[]", observed),
		model.Leaf("cache_basis", projection),
	}
	return model.Nested(ComparisonEvidence{}, model.ClassEvidence, leaves...)
}
func registerComparisonSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{Kind: comparisonKind, Table: "evals_comparison", AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colRunRef, Kind: model.KindUUID, Principal: pdeclNoneRunRef},
			{Name: colComparisonKey, Kind: model.KindText, Principal: model.None("an identity digest used only as a bounded lookup key: comparison.go:154")},
			{Name: colEligible, Kind: model.KindBool},
			{Name: colFinishedAt, Kind: model.KindTimestamp},
			{Name: colComparisonEvidence, Kind: model.KindJSON, Principal: comparisonDeclaration()},
		}, Indexes: []model.IndexSpec{
			{Name: "evals_comparison_run", Columns: []string{model.ColTenantID, colRunRef}, Unique: true},
			{Name: "evals_comparison_select", Columns: []string{model.ColTenantID, colComparisonKey, colEligible, colFinishedAt, model.ColID}},
		}})
}
func persistComparison(ctx context.Context, sc store.Scope, runRef, finished, key string, c ComparisonEvidence) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	repo, err := sc.Ext(comparisonKind)
	if err != nil {
		return err
	}
	_, err = repo.Create(ctx, model.Record{colRunRef: runRef, colFinishedAt: finished, colComparisonKey: key, colEligible: c.Known && c.Complete, colComparisonEvidence: string(b)})
	return err
}
func decodeComparison(rec model.Record) (ComparisonEvidence, error) {
	var c ComparisonEvidence
	if err := json.Unmarshal([]byte(rec.String(colComparisonEvidence)), &c); err != nil {
		return c, fmt.Errorf("evals comparison evidence: %w", err)
	}
	if c.Version != 1 {
		return c, fmt.Errorf("evals comparison evidence: unsupported version %d", c.Version)
	}
	return c, nil
}
func loadComparison(ctx context.Context, sc store.Scope, runRef string) (*ComparisonEvidence, string, error) {
	if runRef == "" {
		return nil, "", nil
	}
	repo, err := sc.Ext(comparisonKind)
	if err != nil {
		return nil, "", err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colRunRef, runRef)}, Limit: 1})
	if err != nil {
		return nil, "", err
	}
	if len(rows) == 0 {
		return nil, "", nil
	}
	c, err := decodeComparison(rows[0])
	if err != nil {
		return nil, "", err
	}
	return &c, rows[0].String(model.ColID), nil
}
func readComparison(ctx context.Context, sc store.Scope, runRef string) (ComparisonEvidence, error) {
	c, _, err := loadComparison(ctx, sc, runRef)
	if err != nil {
		return ComparisonEvidence{}, err
	}
	if c == nil {
		return ComparisonEvidence{Version: 1, Status: ComparisonUnknown, Reason: "legacy_evidence_unknown"}, nil
	}
	return *c, nil
}

// A budget-stopped gate has no run and retains its captured refusal here instead.
func readGateComparison(ctx context.Context, sc store.Scope, rec model.Record) (ComparisonEvidence, error) {
	if ref := rec.String(colRunRef); ref != "" {
		return readComparison(ctx, sc, ref)
	}
	if raw := rec.String(colStoppedComparison); raw != "" {
		return decodeComparison(model.Record{colComparisonEvidence: raw})
	}
	return ComparisonEvidence{Version: 1, Status: ComparisonUnknown, Reason: ComparisonLegacyUnknown}, nil
}
