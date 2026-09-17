// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// TestEvidenceV11PostgresRefusesDifferentCollation replaces one generated object
// with a same-name object whose collation identity demonstrably differs from the
// dialect's generated default, and requires refusal before mutation with the
// catalog and rows unchanged. The tracked-v11 verifier must refuse it as well.
func TestEvidenceV11PostgresRefusesDifferentCollation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		words    []model.EvidenceOperationState
		extra    []string
		precheck string
		verifier bool
	}{
		{
			name:  "state index tenant key with explicit C collation",
			words: evidenceOpStateWords6,
			extra: []string{
				"DROP INDEX evidence_operations_state_idx",
				`CREATE INDEX evidence_operations_state_idx ON evidence_operations (tenant_id COLLATE pg_catalog."C", state)`,
			},
			precheck: `SELECT (SELECT x.indcollation[0] FROM pg_catalog.pg_index x WHERE x.indexrelid = 'evidence_operations_state_idx'::regclass)
			              <> (SELECT a.attcollation FROM pg_catalog.pg_attribute a WHERE a.attrelid = 'evidence_operations'::regclass AND a.attname = 'tenant_id')`,
		},
		{
			name:  "surface column with explicit C collation",
			words: evidenceOpStateWords6,
			extra: []string{`ALTER TABLE evidence_operations ALTER COLUMN surface TYPE text COLLATE pg_catalog."C"`},
			precheck: `SELECT (SELECT a.attcollation FROM pg_catalog.pg_attribute a WHERE a.attrelid = 'evidence_operations'::regclass AND a.attname = 'surface')
			              <> (SELECT a.attcollation FROM pg_catalog.pg_attribute a WHERE a.attrelid = 'evidence_operations'::regclass AND a.attname = 'action')`,
		},
		{
			name:  "tracked v11 journal with C-collated state index key",
			words: evidenceOpStateWords7,
			extra: []string{
				"DROP INDEX evidence_operations_state_idx",
				`CREATE INDEX evidence_operations_state_idx ON evidence_operations (tenant_id COLLATE pg_catalog."C", state)`,
			},
			precheck: `SELECT (SELECT x.indcollation[0] FROM pg_catalog.pg_index x WHERE x.indexrelid = 'evidence_operations_state_idx'::regclass)
			              <> (SELECT a.attcollation FROM pg_catalog.pg_attribute a WHERE a.attrelid = 'evidence_operations'::regclass AND a.attname = 'tenant_id')`,
			verifier: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			p := newV11Postgres(t, false)
			p.build(t, v11Check(tc.words), tc.extra...)
			var differs bool
			if err := p.super.QueryRowContext(ctx, tc.precheck).Scan(&differs); err != nil || !differs {
				t.Fatalf("fixture did not establish a different collation identity: differs=%t err=%v", differs, err)
			}
			if err := p.insert(t, v11Row{id: "r1", tenant: "tenant-a", state: "blocked", claim: "c", outcome: "o"}); err != nil {
				t.Fatalf("insert: %v", err)
			}
			before := p.snapshot(t)
			n0, sum0 := v11Witness(t, p.super, p.dia)
			if tc.verifier {
				if err := verifyEvidenceRefusedPerBoot(ctx, p.owner, p.dia, p.cal); !errors.Is(err, ErrEvidenceRefusedTrackedStale) {
					t.Fatalf("tracked-v11 verifier = %v, want refusal", err)
				}
			}
			_, err := p.apply(t, p.owner, nil)
			if !errors.Is(err, ErrEvidenceRefusedTransitionInventory) {
				t.Fatalf("transition error = %v, want inventory refusal", err)
			}
			n1, sum1 := v11Witness(t, p.super, p.dia)
			if p.snapshot(t) != before || n1 != n0 || sum1 != sum0 {
				t.Fatal("a refused transition changed the relation or rows")
			}
			t.Logf("V11_PG_COLLATION|case=%s|identity_differs=true|refused=true|unchanged=true|verifier_checked=%t", tc.name, tc.verifier)
		})
	}
}
