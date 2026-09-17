// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// communicationChannelGrantIndexDefinitions reads the ChannelGrant indexes the
// REAL database created, from the engine's own catalog. It is not a re-reading
// of the descriptor: a declared index that the reconciler never issued would
// pass a descriptor assertion and fail here.
func communicationChannelGrantIndexDefinitions(
	t *testing.T,
	backend communicationSchemaBackend,
) map[string]string {
	t.Helper()
	driver, query := "sqlite", `
SELECT name, COALESCE(sql, '')
FROM sqlite_schema
WHERE type = 'index' AND tbl_name = 'sessions_channel_grant'`
	if backend.engineName == store.EnginePostgres {
		driver, query = "pgx", `
SELECT indexname, indexdef
FROM pg_indexes
WHERE schemaname = current_schema() AND tablename = 'sessions_channel_grant'`
	}
	raw, err := sql.Open(driver, backend.dsn)
	if err != nil {
		t.Fatalf("open %s catalog: %v", backend.name, err)
	}
	defer raw.Close() //nolint:errcheck
	rows, err := raw.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("read %s index catalog: %v", backend.name, err)
	}
	defer rows.Close() //nolint:errcheck
	out := map[string]string{}
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatalf("scan %s index catalog: %v", backend.name, err)
		}
		out[name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s index catalog: %v", backend.name, err)
	}
	return out
}

// TestChannelGrantAdministrationIndexesExistOnBothEngines proves the three
// indexes the administrative read model declares are actually CREATED by the
// product's own schema path on each engine, that their column order is the one
// the queries bind, and that the pre-existing catalog and per-Channel indexes
// survive untouched.
//
// It exercises whichever engines are configured and SAYS SO: an exit status of
// 0 with PostgreSQL unset proves SQLite only, which is why the skip is loud.
func TestChannelGrantAdministrationIndexesExistOnBothEngines(t *testing.T) {
	t.Parallel()
	backends := communicationSchemaBackends(t)
	engines := make([]string, 0, len(backends))
	for _, backend := range backends {
		engines = append(engines, backend.name)
	}
	t.Logf("K3_ADMIN_INDEX_ENGINES|%s", strings.Join(engines, ","))
	for _, backend := range backends {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			if backend.engineName == store.EngineSQLite {
				backend.dsn = filepath.Join(t.TempDir(), "administration-indexes.db")
			}
			communicationOpenAndClose(t, backend.engineName, backend.dsn, New().RegisterSchema)
			definitions := communicationChannelGrantIndexDefinitions(t, backend)

			// Column order matters: the leading equality columns must precede the
			// keyset/projected column, or the statement stops being a range scan.
			want := map[string][]string{
				"sessions_channel_grant_administration": {
					"tenant_id", "workspace_id", "subject_kind", "subject_ref",
					"state", "can_admin", "channel_id", "expires_at",
				},
				"sessions_channel_grant_history": {"tenant_id", "channel_id", "id", "state"},
				"sessions_channel_grant_subject_history": {
					"tenant_id", "channel_id", "subject_kind", "subject_ref", "id", "state",
				},
				// Pre-existing, asserted so this increment cannot quietly reshape them.
				"sessions_channel_grant_catalog": {
					"tenant_id", "workspace_id", "subject_kind", "subject_ref",
					"state", "can_read", "channel_id", "expires_at",
				},
				"sessions_channel_grant_channel": {"tenant_id", "channel_id", "state", "id"},
			}
			for name, columns := range want {
				definition, created := definitions[name]
				if !created {
					t.Fatalf("%s: index %q was declared but the engine never created it (catalog: %v)",
						backend.name, name, definitions)
				}
				got := communicationIndexColumns(t, backend.name, name, definition)
				if !reflect.DeepEqual(got, columns) {
					t.Fatalf("%s: index %q binds %v, want %v (%s)",
						backend.name, name, got, columns, definition)
				}
			}
		})
	}
}

// communicationIndexColumns extracts the ordered column list from an engine's
// own CREATE INDEX text. Both engines render exactly one parenthesised column
// group, and comparing the parsed list — rather than searching for substrings —
// is what stops "id" from matching inside "tenant_id".
func communicationIndexColumns(t *testing.T, engine, name, definition string) []string {
	t.Helper()
	open := strings.Index(definition, "(")
	closeAt := strings.LastIndex(definition, ")")
	if open < 0 || closeAt <= open {
		t.Fatalf("%s: index %q has no column group: %s", engine, name, definition)
	}
	parts := strings.Split(definition[open+1:closeAt], ",")
	columns := make([]string, 0, len(parts))
	for _, part := range parts {
		column := strings.TrimSpace(part)
		if column == "" {
			t.Fatalf("%s: index %q has an empty column: %s", engine, name, definition)
		}
		columns = append(columns, column)
	}
	return columns
}

// TestChannelAdministrationAuthorityReadIsIndexedOnBothEngines asks the REAL
// engine how it would answer the predicate set the scoped authority read binds,
// with a Channel deliberately crowded by other subjects' active grants.
//
// It is the engine-level half of the K3-IR-02 correction. The causal witness in
// the sessions package proves the reader is no longer refused; the pinned query
// shape proves WHICH columns are bound; this proves the engine turns those bound
// columns into an INDEX lookup rather than a scan of the Channel's whole grant
// set — which is what "bounded by the relevant subjects" has to mean once it
// reaches a database.
//
// SQLite is ASSERTED: its planner is deterministic here and the declared indexes
// are an exact equality prefix. PostgreSQL is REPORTED, not asserted: a cost-based
// planner legitimately prefers a different shape depending on statistics, and a
// plan assertion that depends on whether ANALYZE has run is a flaky gate dressed
// as a guarantee. The PostgreSQL guarantee this increment actually makes is the
// causal one — the 4,096-unrelated-grant witness passes on real PG16 — and the
// plan is logged beside it so a reviewer can see what the engine chose.
func TestChannelAdministrationAuthorityReadIsIndexedOnBothEngines(t *testing.T) {
	t.Parallel()
	backends := administrationReaderBackends(t)
	engines := make([]string, 0, len(backends))
	for _, backend := range backends {
		engines = append(engines, backend.name)
	}
	t.Logf("K3_ADMIN_AUTHORITY_PLAN_ENGINES|%s", strings.Join(engines, ","))
	for _, backend := range backends {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			// The split-owner topology runs DDL as the OWNER, so the schema is
			// created through the owner DSN when the backend declares one; the plan
			// is then asked over the APPLICATION DSN, which is the role the product
			// actually reads with.
			communicationOpenAndCloseOwned(t, backend, New().RegisterSchema)

			driver, explain := "sqlite", "EXPLAIN QUERY PLAN "
			if backend.engineName == store.EnginePostgres {
				driver, explain = "pgx", "EXPLAIN "
			}
			raw, err := sql.Open(driver, backend.dsn)
			if err != nil {
				t.Fatalf("open %s: %v", backend.name, err)
			}
			defer raw.Close() //nolint:errcheck

			// ⛔ THE STATEMENT IS DERIVED FROM THE QUERY VALUE, NOT TYPED OUT AGAIN.
			// It used to be a hand-written literal ending in `ORDER BY id ASC`, which
			// was faithful when this test was written and became a description of a
			// query the product no longer sends the moment the ordering was
			// corrected — a probe that keeps passing while measuring something else.
			// authorityCostStatement renders it from
			// channelAdministrationAuthorityGrantQuery, so a future change to either
			// the predicate or the ordering arrives here automatically.
			query := channelAdministrationAuthorityGrantQuery(
				model.NewID(),
				CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: model.NewID().String()},
			)
			text, args := authorityCostStatement(t, query, model.TenantID(model.NewID()),
				model.NewID(), backend.engineName == store.EnginePostgres)
			statement := explain + text
			rows, err := raw.QueryContext(context.Background(), statement, args...)
			if err != nil {
				t.Fatalf("%s plan: %v", backend.name, err)
			}
			defer rows.Close() //nolint:errcheck
			columns, err := rows.Columns()
			if err != nil {
				t.Fatalf("%s plan columns: %v", backend.name, err)
			}
			var plan []string
			for rows.Next() {
				cells := make([]any, len(columns))
				for index := range cells {
					cells[index] = new(sql.NullString)
				}
				if err := rows.Scan(cells...); err != nil {
					t.Fatalf("%s plan row: %v", backend.name, err)
				}
				for _, cell := range cells {
					if value := cell.(*sql.NullString); value.Valid && value.String != "" {
						plan = append(plan, value.String)
					}
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("%s plan iteration: %v", backend.name, err)
			}
			joined := strings.Join(plan, " | ")
			// ⚠ WHAT THIS PROBE DOES AND DOES NOT SHOW, PER ENGINE.
			//
			// SQLite's planner is rule-based, so it names the index it would use even
			// on an empty relation: its arm is ASSERTED, and the assertion is the
			// PROPERTY (the chosen index binds the subject) rather than an index name.
			//
			// PostgreSQL's is cost-based, and this raw connection sets no
			// `app.tenant_id`, so row-level security turns the whole statement into a
			// One-Time Filter before any index is considered. The line is therefore
			// DIAGNOSTIC only — it is logged, not asserted, and it must not be read as
			// a plan for a populated table. Reproducing a meaningful PostgreSQL plan
			// needs seeded rows, an ANALYZE run as the owner and a tenant-pinned
			// connection; that is a measurement, not a gate, and the guarantee this
			// increment actually makes on PostgreSQL is the CAUSAL one:
			// TestAdministrativeReadsHaveNoUnrelatedActiveGrantCeiling passing on a
			// split-owner PG16 database carrying 4,096 unrelated active grants.
			t.Logf("K3_ADMIN_AUTHORITY_PLAN|engine=%s|relation=empty|rls=unpinned|plan=%s",
				backend.name, joined)
			if backend.engineName != store.EngineSQLite {
				return
			}
			// The assertion is the PROPERTY, not an index name — but the property is
			// WIDER than it used to be, and the widening is the K3-IR-02 correction.
			// Binding the subject alone was the original claim, and an independent
			// populated measurement disproved it: `sessions_channel_grant_subject`
			// binds tenant/workspace/subject/state WITHOUT the Channel, so the range
			// it selects spans that subject's grants on every OTHER Channel — 2,001
			// rows on the review's estate. So the Channel is required here too.
			// `sessions_channel_grant_channel` still fails for the original reason:
			// Channel and state without the subject is the whole-Channel read this
			// correction removed. Naming an index would make a legitimate planner
			// choice a red test; naming the columns does not.
			if !strings.Contains(joined, "SEARCH sessions_channel_grant USING INDEX") &&
				!strings.Contains(joined, "SEARCH sessions_channel_grant USING COVERING INDEX") {
				t.Fatalf("SQLite does not answer the scoped authority read with an index search: %s", joined)
			}
			if strings.Contains(joined, "SCAN sessions_channel_grant") {
				t.Fatalf("SQLite plan contains a full table scan: %s", joined)
			}
			if strings.Contains(strings.ToUpper(joined), "TEMP B-TREE") {
				t.Fatalf("SQLite sorts the authority read into a temporary B-tree, so the "+
					"ordering is not served by the index: %s", joined)
			}
			for _, bound := range []string{"channel_id=?", "subject_kind=?", "subject_ref=?"} {
				if !strings.Contains(joined, bound) {
					t.Fatalf("the chosen index does not bind %s, so the rows examined would "+
						"scale with an estate this request never asked about: %s", bound, joined)
				}
			}
			// ⚠ AND AN EMPTY RELATION IS NOT A COST MEASUREMENT. This probe proves
			// which access path the planner NAMES with no rows and no statistics; what
			// it costs on a populated estate is
			// TestAdministrativeAuthorityReadCostIsBoundedAmidForeignEstate, which
			// counts rows examined on both engines with a present target and an
			// absent control.
		})
	}
}

// communicationOpenAndCloseOwned opens and closes a backend honouring its owner
// DSN. communicationOpenAndClose predates the split-owner backends and opens with
// the application DSN only, which cannot create the schema when DDL belongs to the
// owner role (measured: "permission denied for schema public", SQLSTATE 42501).
func communicationOpenAndCloseOwned(
	t *testing.T,
	backend communicationSchemaBackend,
	register func(store.ExtensionRegistry) error,
) {
	t.Helper()
	st, err := engine.Open(context.Background(), store.Config{
		Engine: backend.engineName, DSN: backend.dsn, OwnerDSN: backend.ownerDSN, Debug: true,
	}, register)
	if err != nil {
		t.Fatalf("open %s: %v", backend.name, err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close %s: %v", backend.name, err)
	}
}
