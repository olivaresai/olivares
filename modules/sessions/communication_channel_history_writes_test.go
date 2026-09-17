// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// channelAdminWriterHistoryGenerations is the history one subject carries in the
// engine measurements below. It is deliberately far past the 256 rows the
// previous writer locked, and past 1000, so a plan that is proportional to the
// history is visible rather than arguable.
const channelAdminWriterHistoryGenerations = 1200

// channelAdminWriterUnrelatedSubjects is how many OTHER subjects hold a current
// admin grant on the same Channel while those measurements run. It exists to
// prove the second half of the claim: the writer's cost is not bounded by the
// Channel's population either.
const channelAdminWriterUnrelatedSubjects = 500

// TestChannelGrantSubjectGenerationIndexPinsTheMeasuredShape pins the ONE index
// this increment declares to the exact column order the writer's statement
// binds, and re-pins the four indexes that were already there. The two sides are
// written out separately on purpose: an index whose order drifts from the
// statement stops being the plan that was measured, and a single shared literal
// would hide that.
func TestChannelGrantSubjectGenerationIndexPinsTheMeasuredShape(t *testing.T) {
	t.Parallel()
	reg := communicationCaptureSchema(t)
	descriptor := communicationDescriptor(t, reg, channelGrantKind)

	want := []string{
		"tenant_id", "workspace_id", "channel_id", "subject_kind", "subject_ref",
		"generation", "id",
	}
	found := channelAdministrationIndex(t, descriptor, "sessions_channel_grant_subject_generation")
	if found.Unique || !reflect.DeepEqual(found.Columns, want) {
		t.Fatalf("subject generation index = %+v, want non-unique %v", found, want)
	}
	// The statement's own columns: the tenant and the workspace lineage the
	// store forces, the Channel and the subject as equalities, then the ordered
	// generation with the id tiebreaker in the same direction.
	bound := []string{
		model.ColTenantID, colWorkWorkspaceID, colCommChannelID,
		colCommSubjectKind, colCommSubjectRef, colCommGeneration, model.ColID,
	}
	if !reflect.DeepEqual(bound, want) {
		t.Fatalf("writer statement columns %v differ from the index %v", bound, want)
	}

	wantState := []string{
		"tenant_id", "workspace_id", "channel_id", "subject_kind", "subject_ref",
		"state", "generation", "id",
	}
	state := channelAdministrationIndex(t, descriptor, "sessions_channel_grant_subject_current")
	if state.Unique || !reflect.DeepEqual(state.Columns, wantState) {
		t.Fatalf("subject current index = %+v, want non-unique %v", state, wantState)
	}
	boundState := []string{
		model.ColTenantID, colWorkWorkspaceID, colCommChannelID,
		colCommSubjectKind, colCommSubjectRef, colCommState, colCommGeneration, model.ColID,
	}
	if !reflect.DeepEqual(boundState, wantState) {
		t.Fatalf("current-row statement columns %v differ from the index %v", boundState, wantState)
	}
	// The two writer indexes share their equality prefix and differ only in what
	// they order by. That is the whole design: one answers "which generation is
	// the subject's last?", the other "which generation is the subject's
	// current one, if any?".
	if !reflect.DeepEqual(want[:5], wantState[:5]) {
		t.Fatalf("the two writer indexes no longer share a prefix: %v / %v", want, wantState)
	}

	// Nothing that the read catalog, the administrative catalog or the sheet
	// measured is widened, reordered or dropped to serve the writer.
	for name, columns := range map[string][]string{
		"sessions_channel_grant_catalog": {
			"tenant_id", "workspace_id", "subject_kind", "subject_ref",
			"state", "can_read", "channel_id", "expires_at",
		},
		"sessions_channel_grant_administration": {
			"tenant_id", "workspace_id", "subject_kind", "subject_ref",
			"state", "can_admin", "channel_id", "expires_at",
		},
		"sessions_channel_grant_history": {"tenant_id", "channel_id", "id", "state"},
		"sessions_channel_grant_subject_history": {
			"tenant_id", "channel_id", "subject_kind", "subject_ref", "id", "state",
		},
		"sessions_channel_grant_subject": {
			"tenant_id", "workspace_id", "subject_kind", "subject_ref", "state", "id",
		},
		"sessions_channel_grant_channel": {"tenant_id", "channel_id", "state", "id"},
		"sessions_channel_grant_uniq": {
			"tenant_id", "channel_id", "subject_kind", "subject_ref", "generation",
		},
	} {
		index := channelAdministrationIndex(t, descriptor, name)
		if !reflect.DeepEqual(index.Columns, columns) {
			t.Fatalf("pre-existing index %q changed: %+v, want %v", name, index, columns)
		}
	}
	if uniq := channelAdministrationIndex(t, descriptor, "sessions_channel_grant_uniq"); !uniq.Unique {
		t.Fatalf("sessions_channel_grant_uniq stopped being unique: %+v", uniq)
	}
}

// TestChannelAdminWriterAddsNoDurableRelation proves the writer correction adds
// no entity kind: it declares ONE index on the existing ChannelGrant relation
// and nothing else.
func TestChannelAdminWriterAddsNoDurableRelation(t *testing.T) {
	t.Parallel()
	reg := communicationCaptureSchema(t)
	kinds := make(map[model.Kind]bool, len(reg.descriptors))
	for _, descriptor := range reg.descriptors {
		kinds[descriptor.Kind] = true
	}
	for _, forbidden := range []model.Kind{
		"sessions.channel_grant_generation", "sessions.channel_grant_head",
		"sessions.channel_admin_selection",
	} {
		if kinds[forbidden] {
			t.Fatalf("the writer correction declared a durable relation %q", forbidden)
		}
	}
	for _, kind := range CommunicationSchemaKinds() {
		if !kinds[kind] {
			t.Fatalf("declared K3 kind %s disappeared from the registered schema", kind)
		}
	}
}

// channelAdminWriterFixture is a real estate carrying ONE Channel with a very
// long single-subject history and many unrelated current admin grants, plus a
// second Channel, so a statement that leaves its Channel or its subject is
// visible in the plan and in the rows the engine actually touched.
type channelAdminWriterFixture struct {
	fixture communicationSchemaFixture
	channel model.ID
	other   model.ID
	subject model.ID
}

func seedChannelAdminWriterHistory(
	t *testing.T,
	fixture communicationSchemaFixture,
) channelAdminWriterFixture {
	t.Helper()
	ctx := context.Background()
	out := channelAdminWriterFixture{
		fixture: fixture, channel: model.NewID(), other: model.NewID(), subject: model.NewID(),
	}
	if err := fixture.st.Mutate(ctx, fixture.tenant, func(sc store.Scope) error {
		channels, err := sc.Ext(channelKind)
		if err != nil {
			return err
		}
		for index, id := range []model.ID{out.channel, out.other} {
			record, encodeErr := channelToRecord(Channel{
				MutableCommunicationEntity: MutableCommunicationEntity{
					CommunicationEntity: CommunicationEntity{
						ID: id, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
						Version: 1, CreatedAt: communicationSchemaNow(),
					}, UpdatedAt: communicationSchemaNow(),
				},
				Slug: fmt.Sprintf("writer-plan-%d", index), Name: "writer plan",
				Kind: ChannelCoordination, State: ChannelActive, Sensitivity: ChannelInternal,
				ContentProtection: ContentProtectionStorage, ProtectionGeneration: 1,
				DefaultAckPolicy: AckPolicyNone, DefaultWake: WakeNone,
				MaxFanout: 10, MaxAutomationDepth: 1,
				ACLRevision: 1, RouteRevision: int64(index + 1), SubscriptionRevision: 1,
			})
			if encodeErr != nil {
				return encodeErr
			}
			if _, err := channels.CreateWithID(ctx, id, record); err != nil {
				return err
			}
		}
		grants, err := sc.Ext(channelGrantKind)
		if err != nil {
			return err
		}
		var previous model.ID
		for generation := 1; generation <= channelAdminWriterHistoryGenerations; generation++ {
			id := model.NewID()
			record := model.Record{
				"workspace_id": fixture.workspace.String(), "channel_id": out.channel.String(),
				"subject_kind": "user", "subject_ref": out.subject.String(),
				"generation": int64(generation),
				"can_read":   true, "can_write": false, "can_admin": true,
				"state":           "revoked",
				"granted_by_kind": "user", "granted_by_ref": out.subject.String(),
				"revoked_by_kind": "user", "revoked_by_ref": out.subject.String(),
			}
			if generation == channelAdminWriterHistoryGenerations {
				record["state"] = "active"
				delete(record, "revoked_by_kind")
				delete(record, "revoked_by_ref")
			}
			if !previous.IsZero() {
				record["supersedes_id"] = previous.String()
			}
			if _, err := grants.CreateWithID(ctx, id, record); err != nil {
				return fmt.Errorf("seed generation %d: %w", generation, err)
			}
			previous = id
		}
		for index := 0; index < channelAdminWriterUnrelatedSubjects; index++ {
			for _, channel := range []model.ID{out.channel, out.other} {
				if _, err := grants.CreateWithID(ctx, model.NewID(), model.Record{
					"workspace_id": fixture.workspace.String(), "channel_id": channel.String(),
					"subject_kind": "user", "subject_ref": model.NewID().String(),
					"generation": int64(1),
					"can_read":   true, "can_write": false, "can_admin": true,
					"state":           "active",
					"granted_by_kind": "user", "granted_by_ref": out.subject.String(),
				}); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed writer history: %v", err)
	}
	return out
}

// channelAdminWriterStatements are the FOUR statements the administrative
// writer issues against sessions_channel_grant, rendered exactly as
// core/internal/store/sqlstore renders a model.Query for this entity: the
// tenant predicate, the caller's ANDed filters in order, then the workspace
// lineage predicate the confined repository forces last, then the ORDER BY with
// the implicit id tiebreaker and LIMIT bound+1.
//
// They are written out rather than captured because the store exposes no
// statement recorder. The shape is pinned on the other side by
// TestChannelGrantSubjectGenerationIndexPinsTheMeasuredShape and by the product
// journeys, which run the real code path on both engines.
func channelAdminWriterStatements(seeded channelAdminWriterFixture) []struct {
	name  string
	text  string
	args  []any
	index string
	rows  int
} {
	f := seeded.fixture
	base := "SELECT * FROM " + channelGrantTable + " WHERE tenant_id = ? AND channel_id = ?"
	subjectEquality := " AND subject_kind = ? AND subject_ref = ?"
	return []struct {
		name  string
		text  string
		args  []any
		index string
		rows  int
	}{
		{
			// GrantChannel: the subject's exact highest generation.
			name: "subject newest generation",
			text: base + subjectEquality + " AND workspace_id = ?" +
				" ORDER BY " + colCommGeneration + " DESC, id DESC LIMIT 3",
			args: []any{
				f.tenant.String(), seeded.channel.String(), "user", seeded.subject.String(),
				f.workspace.String(),
			},
			index: "sessions_channel_grant_subject_generation", rows: 2,
		},
		{
			// Every mutation asks this once per closure subject, and GrantChannel
			// asks it once more about the subject it serves: the subject's
			// persisted-active generation on this Channel, if it has one.
			name: "subject current generation",
			text: base + subjectEquality + " AND state = ? AND workspace_id = ?" +
				" ORDER BY " + colCommGeneration + " DESC, id DESC LIMIT 3",
			args: []any{
				f.tenant.String(), seeded.channel.String(), "user", seeded.subject.String(),
				string(ChannelGrantActive), f.workspace.String(),
			},
			index: "sessions_channel_grant_subject_current", rows: 1,
		},
		{
			// The same question about a subject that has NO current row: proving
			// the absence must cost an empty range, not a scan for a row that is
			// not there.
			name: "subject current generation, absent",
			text: base + subjectEquality + " AND state = ? AND workspace_id = ?" +
				" ORDER BY " + colCommGeneration + " DESC, id DESC LIMIT 3",
			args: []any{
				f.tenant.String(), seeded.channel.String(), "user", model.NewID().String(),
				string(ChannelGrantActive), f.workspace.String(),
			},
			index: "sessions_channel_grant_subject_current", rows: 0,
		},
		{
			// RevokeChannelGrant: the addressed row, confined to this Channel.
			name: "addressed grant row",
			text: base + " AND id = ? AND workspace_id = ? ORDER BY id ASC LIMIT 3",
			args: []any{
				f.tenant.String(), seeded.channel.String(), model.NewID().String(),
				f.workspace.String(),
			},
			index: "", rows: 0,
		},
	}
}

// TestChannelAdminWriterSelectionsAreBoundedOnBothEngines measures, on each
// configured engine, that the four statements the administrative writer issues
// read a bounded number of rows on a Channel carrying 1200 generations of one
// subject and 500 current admin grants of others.
//
// SQLite is judged by its plan: the intended index and NO temporary B-tree,
// because a temporary B-tree here means the engine sorted the whole history to
// answer a two-row question. PostgreSQL is judged by the rows it ACTUALLY
// touched under EXPLAIN ANALYZE, which is a measurement rather than a reading
// of intent.
func TestChannelAdminWriterSelectionsAreBoundedOnBothEngines(t *testing.T) {
	t.Parallel()
	backends := communicationSchemaBackends(t)
	engines := make([]string, 0, len(backends))
	for _, backend := range backends {
		engines = append(engines, backend.name)
	}
	t.Logf("K3_WRITER_PLAN_ENGINES|%s", strings.Join(engines, ","))
	for _, backend := range backends {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			if backend.engineName == store.EngineSQLite {
				backend.dsn = filepath.Join(t.TempDir(), "writer-history.db")
			}
			fixture := communicationOpenFixture(t, backend)
			seeded := seedChannelAdminWriterHistory(t, fixture)

			definitions := communicationChannelGrantIndexDefinitions(t, backend)
			for name, columns := range map[string][]string{
				"sessions_channel_grant_subject_generation": {
					"tenant_id", "workspace_id", "channel_id", "subject_kind", "subject_ref",
					"generation", "id",
				},
				"sessions_channel_grant_subject_current": {
					"tenant_id", "workspace_id", "channel_id", "subject_kind", "subject_ref",
					"state", "generation", "id",
				},
			} {
				definition, created := definitions[name]
				if !created {
					t.Fatalf("%s: writer index %q was declared but never created (catalog: %v)",
						backend.name, name, definitions)
				}
				if got := communicationIndexColumns(
					t, backend.name, name, definition,
				); !reflect.DeepEqual(got, columns) {
					t.Fatalf("%s: writer index %q binds %v (%s)", backend.name, name, got, definition)
				}
			}

			for _, statement := range channelAdminWriterStatements(seeded) {
				if backend.engineName == store.EngineSQLite {
					plan := explainSQLiteQueryPlan(t, backend.dsn, statement.text, statement.args...)
					if statement.index != "" && !strings.Contains(plan, "INDEX "+statement.index+" ") {
						t.Errorf("%s plan does not use %s:\n%s", statement.name, statement.index, plan)
					}
					if strings.Contains(plan, "TEMP B-TREE") || strings.Contains(plan, "SCAN ") {
						t.Errorf("%s plan is not bounded (temp b-tree or full scan):\n%s",
							statement.name, plan)
					}
					t.Logf("K3_WRITER_PLAN|sqlite|%s|%s", statement.name,
						strings.ReplaceAll(plan, "\n", " / "))
					continue
				}
				touched, plan := explainPostgresTouchedRows(
					t, backend.dsn, fixture.tenant, statement.text, statement.args...)
				// The page reads limit+1 = 3 rows; the ceiling allows that plus a
				// little slack. Anything near the history means the engine walked it.
				if ceiling := statement.rows + 3; touched > ceiling {
					t.Errorf("%s touched %d rows at its busiest node, want at most %d:\n%s",
						statement.name, touched, ceiling, plan)
				}
				t.Logf("K3_WRITER_PLAN|postgres|%s|busiest_node_rows=%d|%s",
					statement.name, touched, strings.ReplaceAll(plan, "\n", " / "))
			}
		})
	}
}

func explainSQLiteQueryPlan(t *testing.T, dsn, text string, args ...any) string {
	t.Helper()
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite for EXPLAIN: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	rows, err := raw.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+text, args...)
	if err != nil {
		t.Fatalf("explain %q: %v", text, err)
	}
	defer rows.Close() //nolint:errcheck
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan explain: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate explain: %v", err)
	}
	return strings.Join(details, "\n")
}

// explainPostgresTouchedRows returns the rows the BUSIEST node of the plan
// actually touched — the rows it emitted plus the rows it read and discarded —
// together with the plan text, from EXPLAIN (ANALYZE, FORMAT JSON).
//
// The busiest node rather than the root, and discarded rows as well as emitted
// ones, because both of the plans this measurement is meant to catch hide their
// cost below the root: a Sort or a Limit over an index scan of the whole history
// reports three rows at the top and the whole history underneath, and a scan
// that filters the Channel out afterwards reports the rows it kept, not the rows
// it read.
func explainPostgresTouchedRows(
	t *testing.T,
	dsn string,
	tenant model.TenantID,
	text string,
	args ...any,
) (int, string) {
	t.Helper()
	ctx := context.Background()
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres for EXPLAIN: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	// One PINNED connection carrying the same transaction scope the product's own
	// pool sets: the row-level policies read app.tenant_id, so without it the
	// statement is refused and with a different value the plan would be another
	// statement's. ANALYZE first, so the planner chooses on statistics rather
	// than on the defaults of a freshly loaded table.
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatalf("pin a postgres connection: %v", err)
	}
	defer conn.Close() //nolint:errcheck
	if _, err := conn.ExecContext(ctx, "ANALYZE "+channelGrantTable); err != nil {
		t.Fatalf("analyze %s: %v", channelGrantTable, err)
	}
	if _, err := conn.ExecContext(
		ctx, "SELECT set_config('app.tenant_id', $1, false)", tenant.String(),
	); err != nil {
		t.Fatalf("set the transaction tenant scope: %v", err)
	}
	rebound := text
	for index := 1; strings.Contains(rebound, "?"); index++ {
		rebound = strings.Replace(rebound, "?", fmt.Sprintf("$%d", index), 1)
	}
	var payload string
	if err := conn.QueryRowContext(
		ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+rebound, args...,
	).Scan(&payload); err != nil {
		t.Fatalf("explain analyze %q: %v", rebound, err)
	}
	var plans []struct {
		Plan map[string]any `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(payload), &plans); err != nil || len(plans) != 1 {
		t.Fatalf("decode EXPLAIN JSON: %v (%s)", err, payload)
	}
	return busiestPostgresPlanNode(plans[0].Plan), payload
}

// busiestPostgresPlanNode returns the largest per-node row count in the plan,
// counting each node's emitted rows plus the rows it read and discarded.
func busiestPostgresPlanNode(node map[string]any) int {
	touched := 0
	for _, field := range []string{
		"Actual Rows", "Rows Removed by Filter", "Rows Removed by Index Recheck",
	} {
		if value, ok := node[field].(float64); ok {
			touched += int(value)
		}
	}
	children, ok := node["Plans"].([]any)
	if !ok {
		return touched
	}
	for _, child := range children {
		next, ok := child.(map[string]any)
		if !ok {
			continue
		}
		if deeper := busiestPostgresPlanNode(next); deeper > touched {
			touched = deeper
		}
	}
	return touched
}
