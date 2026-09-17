// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// administrationForeignEstateSize is the size of each foreign population the
// cost witness builds. It is deliberately the size the INDEPENDENT REVIEW used
// to disprove the first correction's bound, so a regression is measured against
// the same estate that exposed it.
const administrationForeignEstateSize = 2000

// administrationAuthorityExaminedBound is the number of rows either engine may
// examine to answer the authority read for ONE closure subject on ONE Channel.
//
// A legal estate holds ONE active generation per subject per Channel, so the
// honest answer is 1. The bound is 10 rather than 1 because an index range scan
// legitimately touches a boundary row or two and because PostgreSQL's per-node
// accounting includes the row a LIMIT stops on. What it must never accommodate
// is a number that grows with the foreign estate: 2,001 and 4,003 are the two
// measured failures this bound exists to catch, and both are two orders of
// magnitude above it.
const administrationAuthorityExaminedBound = 10

// TestAdministrativeAuthorityReadCostIsBoundedAmidForeignEstate is the
// PHYSICAL half of K3-IR-02, and the half the first correction did not deliver.
//
// ⛔ WHAT WAS STILL WRONG AFTER THE SEMANTIC FIX. Restricting the authority read
// to the reader's own closure subjects removed the availability ceiling — the
// causal witness proves that — but it said nothing about what an engine DOES with
// the resulting predicate. Measured by the independent review on a populated
// split-owner PostgreSQL 16, the reader examined 4,003 rows to return ONE: the
// query supplied no ordering, the store therefore rendered `ORDER BY id ASC`, and
// the planner satisfied that ordering from an ordered PRIMARY-KEY scan with every
// equality column demoted to a row filter. On SQLite the chosen index bound the
// subject but not the Channel, so the range spanned the same subject's grants on
// every OTHER Channel.
//
// NEITHER FAILURE IS AN INDEX FAILURE, which is why "the columns are a prefix of
// a declared index" was never the proof it was written as. One is a planner
// preferring a free ordering; the other is an index that binds four of the five
// columns that matter.
//
// THE WITNESS. Two foreign populations at once, because each defeats a different
// wrong plan:
//
//   - 2,000 OTHER subjects holding ACTIVE grants on the TARGET Channel. A plan
//     that binds the Channel but not the subject reads all of them.
//   - the SAME actor subject holding ACTIVE grants on 2,000 OTHER Channels. A
//     plan that binds the subject but not the Channel reads all of THOSE. This is
//     the population the first correction's own SQLite plan walked.
//
// Both administrative operations answer the request first — a cheap plan that
// returns the wrong page is not a fix — and then the cost of the authority read
// is measured on the real engine with rows examined, present target AND absent
// control, on a tenant-pinned application connection in the split-owner topology.
func TestAdministrativeAuthorityReadCostIsBoundedAmidForeignEstate(t *testing.T) {
	backends := administrationReaderBackends(t)
	engines := make([]string, 0, len(backends))
	for _, backend := range backends {
		engines = append(engines, backend.name)
	}
	t.Logf("K3_AUTHORITY_COST_ENGINES|%s", strings.Join(engines, ","))
	for _, backend := range backends {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			if backend.engineName == store.EngineSQLite {
				backend.dsn = filepath.Join(t.TempDir(), "authority-cost.db")
			}
			fx := newChannelCatalogFixtureOn(t, backend)
			group := CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: model.NewID().String()}
			fx.closure.subjectsByUser[fx.readerID] = []CommunicationSubjectRef{
				fx.readerSubject(), group,
			}
			channel := fx.createChannel(t, "authority-cost-target", ChannelActive)
			readerGrant := fx.grant(t, channel.ID, fx.readerSubject(), false, false, true, nil)

			// Population one: strangers crowding the TARGET Channel.
			strangers := seedUnrelatedActiveChannelGrants(
				t, fx, channel.ID, administrationForeignEstateSize)
			// Population two: the reader's OWN subject, everywhere else.
			foreign := seedSameSubjectActiveGrantsOnOtherChannels(
				t, fx, fx.readerSubject(), administrationForeignEstateSize)
			fx.reconcile(t)
			t.Logf("K3_AUTHORITY_COST_ESTATE|engine=%s|target_strangers=%d|foreign_channels=%d|"+
				"pertinent_rows=1", backend.name, strangers, foreign)

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()

			// ── The two operations answer correctly on the populated estate ──
			sheet, err := fx.m.listChannelGrantAdministrationWithAuthority(
				ctx, fx.scope, fx.readerRef, ChannelGrantAdministrationRequest{
					ChannelID: channel.ID, State: ChannelGrantAdministrationStateAll,
					Subject: fx.readerSubject(), HasSubject: true, Limit: 1,
				},
			)
			if err != nil {
				t.Fatalf("populated sheet: %v", err)
			}
			if len(sheet.Items) != 1 || sheet.Items[0].Grant.ID != readerGrant {
				t.Fatalf("populated sheet = %+v, want the reader's own generation", sheet.Items)
			}
			catalog, err := fx.m.listAdministrableChannelsWithAuthority(
				ctx, fx.scope, fx.readerRef, ChannelAdministrationRequest{Limit: 1},
			)
			if err != nil {
				t.Fatalf("populated catalog: %v", err)
			}
			if len(catalog.Items) == 0 || catalog.Items[0].Channel.ID != channel.ID {
				t.Fatalf("populated catalog = %+v, want the target Channel first", catalog.Items)
			}

			// ── The absent control still conceals, on the same estate ──
			absentChannel := fx.createChannel(t, "authority-cost-absent", ChannelActive)
			if _, err := fx.m.listChannelGrantAdministrationWithAuthority(
				ctx, fx.scope, fx.readerRef, ChannelGrantAdministrationRequest{
					ChannelID: absentChannel.ID, State: ChannelGrantAdministrationStateAll, Limit: 1,
				},
			); !errors.Is(err, ErrCommunicationNotFound) {
				t.Fatalf("absent-authority sheet = %v, want a concealed not-found", err)
			}

			// ── The cost of the authority read itself ──
			measureAuthorityReadCost(t, backend, fx, channel.ID, fx.readerSubject())
		})
	}
}

// seedSameSubjectActiveGrantsOnOtherChannels creates `count` other Channels in
// the same workspace and gives the SAME subject an active admin grant on each.
//
// These rows are legal, unrelated and invisible to the request under measurement:
// the reader asked about ONE Channel. They exist to make a plan that binds the
// subject without the Channel expensive, which is exactly the SQLite plan the
// independent review measured on the first correction.
func seedSameSubjectActiveGrantsOnOtherChannels(
	t *testing.T,
	fx channelCatalogFixture,
	subject CommunicationSubjectRef,
	count int,
) int {
	t.Helper()
	ctx := context.Background()
	if err := fx.m.data.Mutate(ctx, fx.tenant, func(sc store.Scope) error {
		channels, err := sc.Ext(channelKind)
		if err != nil {
			return err
		}
		grants, err := sc.Ext(channelGrantKind)
		if err != nil {
			return err
		}
		for index := 0; index < count; index++ {
			other := model.NewID()
			if _, err := channels.CreateWithID(ctx, other,
				communicationChannelRecord(fx.workspace, fmt.Sprintf("authority-cost-foreign-%d", index)),
			); err != nil {
				return fmt.Errorf("seed foreign Channel %d: %w", index, err)
			}
			record := model.Record{
				colWorkWorkspaceID:   fx.workspace.String(),
				colCommChannelID:     other.String(),
				colCommSubjectKind:   string(subject.Kind),
				colCommSubjectRef:    subject.Ref,
				colCommGeneration:    int64(1),
				colCommCanRead:       false,
				colCommCanWrite:      false,
				colCommCanAdmin:      true,
				colCommState:         string(ChannelGrantActive),
				colCommGrantedByKind: string(ActorUser),
				colCommGrantedByRef:  fx.sender.String(),
			}
			if _, err := grants.CreateWithID(ctx, model.NewID(), record); err != nil {
				return fmt.Errorf("seed foreign grant %d: %w", index, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed %d same-subject grants on other Channels: %v", count, err)
	}
	return count
}

// authorityCostStatement renders the SQL the store issues for the EXACT query
// value the product binds, so the measurement below cannot drift from the source
// by being written out twice.
//
// The rendering rules are the generic repository's own and are named here so a
// reader can check them: the tenant predicate is prepended by the repository, the
// workspace predicate is APPENDED by the confined scope (core/store.forceQuery),
// the ORDER BY is the query's Sort with an id tiebreaker only when the caller
// supplied none, and the LIMIT is the caller's plus the repository's one-row
// lookahead. ChannelGrant declares no soft delete, so no `deleted_at` term
// exists. The AND order is irrelevant to a planner; the SET and the ORDER BY are
// not, and both come from the query value.
func authorityCostStatement(
	t *testing.T,
	query model.Query,
	tenant model.TenantID,
	workspace model.ID,
	postgres bool,
) (string, []any) {
	t.Helper()
	// The PROJECTION is the descriptor's, not a convenient `SELECT id`, because a
	// narrower projection measures a plan the product never gets: on PostgreSQL it
	// can be answered index-only, while the real full-record read is an ordinary
	// Index Scan with a heap fetch.
	//
	// ⚠ THAT DIFFERENCE IS WHY AN EARLIER DESCRIPTION OF THIS MEASUREMENT WAS
	// WRONG, and it is recorded here rather than quietly dropped. The R3 report
	// described the corrected plan as an "Index Only Scan" on PostgreSQL and a
	// "COVERING" index on SQLite. Those labels came from the first, `SELECT id`
	// version of this probe. Once the projection was widened to the descriptor —
	// which is this function's whole point — the real plans are `Index Scan` and
	// `SEARCH … USING INDEX`, confirmed by an independent reviewer and by the
	// author's own retained final log. The measured cost (1 row present, 0 absent)
	// is unchanged; what is NOT established is heap-free access, and this comment
	// exists so nobody re-derives that claim from the stronger words.
	projection := communicationDescriptor(t, communicationCaptureSchema(t), channelGrantKind).
		AllColumns()
	terms := []string{"tenant_id = ?"}
	args := []any{tenant.String()}
	for _, filter := range query.Filters {
		if filter.Op != model.OpEq {
			t.Fatalf("authority query carries a non-equality filter %+v; the cost derivation "+
				"below assumes equality ranges and must be revisited", filter)
		}
		terms = append(terms, filter.Column+" = ?")
		args = append(args, filter.Value)
	}
	terms = append(terms, "workspace_id = ?")
	args = append(args, workspace.String())

	order := make([]string, 0, len(query.Sort)+1)
	explicitID := false
	for _, term := range query.Sort {
		direction := "ASC"
		if term.Desc {
			direction = "DESC"
		}
		order = append(order, term.Column+" "+direction)
		if term.Column == model.ColID {
			explicitID = true
		}
	}
	if !explicitID {
		order = append(order, "id ASC")
	}
	statement := fmt.Sprintf("SELECT %s FROM sessions_channel_grant WHERE %s ORDER BY %s LIMIT %d",
		strings.Join(projection, ", "), strings.Join(terms, " AND "),
		strings.Join(order, ", "), query.Limit+1)
	if postgres {
		var b strings.Builder
		position := 0
		for _, r := range statement {
			if r == '?' {
				position++
				fmt.Fprintf(&b, "$%d", position)
				continue
			}
			b.WriteRune(r)
		}
		statement = b.String()
	}
	return statement, args
}

// measureAuthorityReadCost asks the REAL engine how many rows it examines to
// answer the authority read, with the target present and with it absent.
func measureAuthorityReadCost(
	t *testing.T,
	backend communicationSchemaBackend,
	fx channelCatalogFixture,
	channelID model.ID,
	subject CommunicationSubjectRef,
) {
	t.Helper()
	// The reader refuses an identity without a finite deadline, so the probes
	// below carry one. The first attempt at this measurement did not, and the
	// interception probe died with `communication request identity requires a
	// finite deadline` — the same fixture fault the independent review recorded
	// against its own populated witness. It is a harness requirement, not a
	// product one, and it is named here so the next reader does not rediscover it.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	query := channelAdministrationAuthorityGrantQuery(channelID, subject)
	postgres := backend.engineName == store.EnginePostgres
	statement, args := authorityCostStatement(t, query, fx.tenant, fx.workspace, postgres)
	t.Logf("K3_AUTHORITY_COST_STATEMENT|engine=%s|limit=%d|sql=%s",
		backend.name, query.Limit+1, statement)
	if postgres {
		measurePostgresAuthorityCost(t, ctx, backend, fx, statement, args)
		return
	}
	measureSQLiteAuthorityCost(t, ctx, backend, statement, args, query)
}

// measureSQLiteAuthorityCost derives rows examined from SQLite's own plan.
//
// SQLite exposes no per-statement row counter, so the metric is the CARDINALITY
// OF THE INDEX RANGE THE PLAN BINDS, derived mechanically from the plan text
// rather than asserted: the chosen index and its bound equality columns are
// parsed out of EXPLAIN QUERY PLAN, and the rows that range spans are counted
// with those columns and their actual values. A range scan cannot examine fewer
// rows than a residual filter later discards, so this is the engine-appropriate
// answer to "how much does this read touch" — and it is the number that was 2,001
// before the ordering correction, because the index SQLite then chose did not
// bind the Channel.
func measureSQLiteAuthorityCost(
	t *testing.T,
	ctx context.Context,
	backend communicationSchemaBackend,
	statement string,
	args []any,
	query model.Query,
) {
	t.Helper()
	db, err := sql.Open("sqlite", backend.dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close() //nolint:errcheck

	bound := map[string]any{"tenant_id": args[0]}
	for index, filter := range query.Filters {
		bound[filter.Column] = args[index+1]
	}
	bound["workspace_id"] = args[len(args)-1]

	for _, mode := range []string{"present", "absent"} {
		probe := append([]any(nil), args...)
		if mode == "absent" {
			probe[1] = model.NewID().String()
		}
		values := make(map[string]any, len(bound))
		for column, value := range bound {
			values[column] = value
		}
		if mode == "absent" {
			values[query.Filters[0].Column] = probe[1]
		}
		plan := sqlitePlanLines(t, ctx, db, statement, probe)
		joined := strings.Join(plan, " | ")
		if strings.Contains(strings.ToUpper(joined), "TEMP B-TREE") {
			t.Fatalf("SQLite %s plan sorts into a temporary B-tree, so the ordering is not "+
				"served by an index: %s", mode, joined)
		}
		index, columns := sqliteIndexAndBoundColumns(t, joined)
		// ⛔ MEASURE FIRST, THEN JUDGE. The cardinality is logged before either
		// assertion so a FAILING run publishes the number too: a counterfactual
		// that only says "the wrong index" is an opinion, and the number is what
		// makes the correction's 1 comparable with the defect's 2,001.
		examined := sqliteIndexRangeCardinality(t, ctx, db, columns, values)
		t.Logf("K3_AUTHORITY_COST|engine=sqlite|mode=%s|index=%s|bound=%v|"+
			"index_range_rows=%d|plan=%s", mode, index, authorityBoundColumnNames(columns), examined, joined)
		for _, required := range []string{"channel_id", "subject_kind", "subject_ref"} {
			if !columns[required] {
				t.Errorf("SQLite %s plan uses index %q binding %v, which does not bind %q: "+
					"the range then spans every row that shares the other columns — %s",
					mode, index, authorityBoundColumnNames(columns), required, joined)
			}
		}
		if examined > administrationAuthorityExaminedBound {
			t.Errorf("SQLite %s: the index range the plan binds spans %d rows for at most one "+
				"pertinent grant; the foreign estate determines this read's cost", mode, examined)
		}
	}
}

func sqlitePlanLines(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	statement string,
	args []any,
) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+statement, args...)
	if err != nil {
		t.Fatalf("sqlite plan: %v", err)
	}
	defer rows.Close() //nolint:errcheck
	var plan []string
	for rows.Next() {
		var a, b, c int
		var detail string
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatalf("sqlite plan row: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("sqlite plan iteration: %v", err)
	}
	if len(plan) == 0 {
		t.Fatal("sqlite produced no plan")
	}
	return plan
}

// sqliteIndexAndBoundColumns extracts the chosen index and the columns it binds
// as EQUALITY terms from a plan line. A non-equality term makes the derived
// cardinality an underestimate, so it is refused rather than counted.
func sqliteIndexAndBoundColumns(t *testing.T, plan string) (string, map[string]bool) {
	t.Helper()
	// SQLite says "USING INDEX x" or "USING COVERING INDEX x". Both are index
	// searches and both are accepted; the covering form would additionally mean the
	// table is never touched. ⚠ The full-record query this suite measures gets the
	// PLAIN form — `USING INDEX sessions_channel_grant_subject_current` — so the
	// covering branch is here for robustness, not because it describes the measured
	// plan. An earlier report said otherwise; see authorityCostStatement.
	marker, at := "", -1
	for _, candidate := range []string{"USING COVERING INDEX ", "USING INDEX "} {
		if position := strings.Index(plan, candidate); position >= 0 {
			marker, at = candidate, position
			break
		}
	}
	if at < 0 || !strings.Contains(plan, "SEARCH sessions_channel_grant") {
		t.Fatalf("SQLite does not answer the authority read with an index search: %s", plan)
	}
	rest := plan[at+len(marker):]
	open := strings.Index(rest, "(")
	closeAt := strings.Index(rest, ")")
	if open < 0 || closeAt <= open {
		t.Fatalf("SQLite plan names no bound column group: %s", plan)
	}
	name := strings.TrimSpace(rest[:open])
	columns := map[string]bool{}
	for _, term := range strings.Split(rest[open+1:closeAt], " AND ") {
		term = strings.TrimSpace(term)
		column, found := strings.CutSuffix(term, "=?")
		if !found {
			t.Fatalf("SQLite plan binds a non-equality term %q; the derived range "+
				"cardinality would understate the cost: %s", term, plan)
		}
		columns[strings.TrimSpace(column)] = true
	}
	return name, columns
}

// sqliteIndexRangeCardinality counts the rows the bound equality columns span.
func sqliteIndexRangeCardinality(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	columns map[string]bool,
	values map[string]any,
) int {
	t.Helper()
	terms := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, column := range authorityBoundColumnNames(columns) {
		value, ok := values[column]
		if !ok {
			t.Fatalf("plan binds %q, which the measured statement does not supply", column)
		}
		terms = append(terms, column+" = ?")
		args = append(args, value)
	}
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM sessions_channel_grant WHERE "+strings.Join(terms, " AND "),
		args...,
	).Scan(&count); err != nil {
		t.Fatalf("count index range: %v", err)
	}
	return count
}

func authorityBoundColumnNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// measurePostgresAuthorityCost measures rows examined with EXPLAIN ANALYZE on a
// tenant-pinned APPLICATION connection, after the SCHEMA OWNER has collected
// statistics — the application role cannot ANALYZE a relation it does not own and
// the server skips the request silently, which is how a plan gets measured
// against no statistics at all.
func measurePostgresAuthorityCost(
	t *testing.T,
	ctx context.Context,
	backend communicationSchemaBackend,
	fx channelCatalogFixture,
	statement string,
	args []any,
) {
	t.Helper()
	if backend.ownerDSN == "" {
		t.Fatal("the PostgreSQL cost measurement requires the split-owner topology")
	}
	owner, err := sql.Open("pgx", backend.ownerDSN)
	if err != nil {
		t.Fatalf("open owner: %v", err)
	}
	if _, err := owner.ExecContext(ctx, "ANALYZE sessions_channel_grant"); err != nil {
		owner.Close() //nolint:errcheck
		t.Fatalf("owner ANALYZE: %v", err)
	}
	owner.Close() //nolint:errcheck

	app, err := sql.Open("pgx", backend.dsn)
	if err != nil {
		t.Fatalf("open app: %v", err)
	}
	defer app.Close() //nolint:errcheck
	conn, err := app.Conn(ctx)
	if err != nil {
		t.Fatalf("app connection: %v", err)
	}
	defer conn.Close() //nolint:errcheck
	if _, err := conn.ExecContext(ctx,
		"SELECT set_config('app.tenant_id', $1, false)", fx.tenant.String()); err != nil {
		t.Fatalf("pin app.tenant_id: %v", err)
	}
	var role string
	var superuser, bypassRLS bool
	if err := conn.QueryRowContext(ctx,
		"SELECT current_user, r.rolsuper, r.rolbypassrls FROM pg_catalog.pg_roles r "+
			"WHERE r.rolname = current_user").Scan(&role, &superuser, &bypassRLS); err != nil {
		t.Fatalf("read role posture: %v", err)
	}
	if superuser || bypassRLS {
		t.Fatalf("measuring role %s is superuser=%t bypassrls=%t; that is not the posture "+
			"the product reads with", role, superuser, bypassRLS)
	}
	t.Logf("K3_AUTHORITY_COST_POSTURE|role=%s|superuser=%t|bypassrls=%t|tenant_pinned=true|"+
		"analyzed_by=owner", role, superuser, bypassRLS)

	for _, mode := range []string{"present", "absent"} {
		probe := append([]any(nil), args...)
		if mode == "absent" {
			probe[1] = model.NewID().String()
		}
		var raw string
		if err := conn.QueryRowContext(ctx,
			"EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+statement, probe...).Scan(&raw); err != nil {
			t.Fatalf("explain %s: %v", mode, err)
		}
		var plans []struct {
			Plan map[string]any `json:"Plan"`
		}
		if err := json.Unmarshal([]byte(raw), &plans); err != nil || len(plans) != 1 {
			t.Fatalf("explain JSON %s: %v", mode, err)
		}
		examined, node := postgresBusiestNode(plans[0].Plan)
		t.Logf("K3_AUTHORITY_COST|engine=postgres-split-owner|mode=%s|busiest_node=%s|"+
			"rows_examined=%d|plan=%s", mode, node, examined, compactAuthorityPlanJSON(raw))
		if examined > administrationAuthorityExaminedBound {
			t.Errorf("PostgreSQL %s: the busiest node (%s) examined %d rows for at most one "+
				"pertinent grant; the foreign estate determines this read's cost",
				mode, node, examined)
		}
	}
	assertPostgresAuthorityStatementIsTheProductStatement(t, ctx, backend, fx, statement)
}

// postgresBusiestNode reports the largest rows-examined figure anywhere in the
// plan tree, and the node type that produced it.
//
// Rows examined at a node is its actual output PLUS what it discarded, times its
// loop count. Reading only the root would hide the substitution this test exists
// to catch: an ordered primary-key scan feeding a Limit reports one row at the
// top and four thousand underneath.
func postgresBusiestNode(node map[string]any) (int, string) {
	sum := float64(0)
	for _, key := range []string{
		"Actual Rows", "Rows Removed by Filter", "Rows Removed by Index Recheck",
	} {
		if value, ok := node[key].(float64); ok {
			sum += value
		}
	}
	if loops, ok := node["Actual Loops"].(float64); ok {
		sum *= loops
	}
	best, name := int(sum), fmt.Sprint(node["Node Type"])
	if children, ok := node["Plans"].([]any); ok {
		for _, raw := range children {
			child, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if value, childName := postgresBusiestNode(child); value > best {
				best, name = value, childName
			}
		}
	}
	return best, name
}

func compactAuthorityPlanJSON(raw string) string {
	return strings.Join(strings.Fields(raw), " ")
}

// assertPostgresAuthorityStatementIsTheProductStatement closes the gap the
// independent review named: the plans above are measured on a statement this test
// RENDERS, and a rendering that drifted from the store's would measure the wrong
// query. So the server is asked to log what the PRODUCT actually sent, the real
// reader is driven once, and the logged text is compared with the rendering.
//
// It is reported and asserted only when the instance log is reachable and logging
// can be enabled — that is a property of the disposable runtime, not of the
// product — and the outcome is named either way, so an unavailable interception
// can never read as a passing one.
func assertPostgresAuthorityStatementIsTheProductStatement(
	t *testing.T,
	ctx context.Context,
	backend communicationSchemaBackend,
	fx channelCatalogFixture,
	statement string,
) {
	t.Helper()
	logPath := strings.TrimSpace(os.Getenv("OLIVARES_PG_LOG"))
	superuserDSN := strings.TrimSpace(os.Getenv(enginetest.EnvSuperuserDSN))
	if logPath == "" || superuserDSN == "" {
		t.Logf("K3_AUTHORITY_SQL_INTERCEPTION|state=unavailable|reason=%s",
			"no instance log or superuser DSN in the environment")
		return
	}
	before, err := os.Stat(logPath)
	if err != nil {
		t.Logf("K3_AUTHORITY_SQL_INTERCEPTION|state=unavailable|reason=stat %v", err)
		return
	}
	super, err := sql.Open("pgx", superuserDSN)
	if err != nil {
		t.Logf("K3_AUTHORITY_SQL_INTERCEPTION|state=unavailable|reason=open %v", err)
		return
	}
	defer super.Close() //nolint:errcheck
	for _, command := range []string{
		"ALTER SYSTEM SET log_statement = 'all'",
		"SELECT pg_reload_conf()",
	} {
		if _, err := super.ExecContext(ctx, command); err != nil {
			t.Logf("K3_AUTHORITY_SQL_INTERCEPTION|state=unavailable|reason=%q %v", command, err)
			return
		}
	}
	defer func() {
		for _, command := range []string{
			"ALTER SYSTEM RESET log_statement", "SELECT pg_reload_conf()",
		} {
			if _, err := super.ExecContext(context.Background(), command); err != nil {
				t.Errorf("restore %q: %v", command, err)
			}
		}
	}()

	// Reload is asynchronous. Wait until a sentinel of our own is visible in the
	// log before driving the reader, so an empty capture cannot be mistaken for a
	// product that issued nothing.
	sentinel := "k3_authority_sql_sentinel_" + model.NewID().String()[:8]
	deadline := time.Now().Add(30 * time.Second)
	armed := false
	for time.Now().Before(deadline) {
		if _, err := super.ExecContext(ctx, "SELECT '"+sentinel+"'"); err != nil {
			t.Fatalf("sentinel: %v", err)
		}
		if strings.Contains(readLogTail(t, logPath, before.Size()), sentinel) {
			armed = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !armed {
		t.Logf("K3_AUTHORITY_SQL_INTERCEPTION|state=unavailable|reason=logging never took effect")
		return
	}

	if _, err := fx.m.listChannelGrantAdministrationWithAuthority(
		ctx, fx.scope, fx.readerRef, ChannelGrantAdministrationRequest{
			ChannelID: model.NewID(), State: ChannelGrantAdministrationStateAll, Limit: 1,
		},
	); err == nil {
		t.Fatal("the interception probe expected a concealed refusal for an unknown Channel")
	}
	// The refusal above still runs the authority read, which is the statement
	// under measurement. Drive the ANSWERING path too so the capture cannot be an
	// artefact of the refusal.
	if _, err := fx.m.listAdministrableChannelsWithAuthority(
		ctx, fx.scope, fx.readerRef, ChannelAdministrationRequest{Limit: 1},
	); err != nil {
		t.Fatalf("interception probe catalog: %v", err)
	}

	// ⛔ THE SELECTOR MUST NOT BE THE ASSERTION. Picking the logged line by the
	// ordering under test would make this vacuous, and picking it by
	// "mentions sessions_channel_grant" catches the catalog's DISTINCT projection
	// instead — measured, on the first run of this probe. The authority read is
	// identified by what does NOT depend on the correction: it selects from the
	// relation, binds subject_ref as an equality, and carries the per-subject
	// bound as a LITERAL limit, because the generic repository interpolates the
	// limit rather than binding it.
	want := strings.Join(strings.Fields(statement), " ")
	limitMarker := want[strings.LastIndex(want, "LIMIT "):]
	var captured []string
	for _, line := range strings.Split(readLogTail(t, logPath, before.Size()), "\n") {
		if !strings.Contains(line, "FROM sessions_channel_grant") ||
			!strings.Contains(line, "subject_ref = $") ||
			!strings.Contains(line, limitMarker) {
			continue
		}
		captured = append(captured, strings.Join(strings.Fields(line), " "))
	}
	if len(captured) == 0 {
		t.Fatalf("K3_AUTHORITY_SQL_INTERCEPTION|state=empty: logging was armed and the reader "+
			"ran, so no statement matching %q means the capture is wrong, not that none "+
			"was sent", limitMarker)
	}
	orderBy := want[strings.Index(want, "ORDER BY"):]
	t.Logf("K3_AUTHORITY_SQL_INTERCEPTION|state=captured|statements=%d|order_by=%q",
		len(captured), orderBy)
	// EVERY captured statement, not the first: a second unordered variant of the
	// same read is exactly the regression this interception exists to see.
	for index, line := range captured {
		t.Logf("K3_AUTHORITY_SQL_INTERCEPTION|statement=%d|%s", index, line)
		if !strings.Contains(line, orderBy) {
			t.Errorf("the product's own statement does not carry the ordering this "+
				"measurement rendered (%q); the measured plan describes a different "+
				"query:\n%s", orderBy, line)
		}
		for _, column := range []string{
			"channel_id = $", "subject_kind = $", "subject_ref = $", "state = $",
			"workspace_id = $", "tenant_id = $",
		} {
			if !strings.Contains(line, column) {
				t.Errorf("the product's own statement does not bind %q: %s", column, line)
			}
		}
	}
}

func readLogTail(t *testing.T, path string, from int64) string {
	t.Helper()
	file, err := os.Open(path) //nolint:gosec // an instance log path from the disposable runtime
	if err != nil {
		t.Fatalf("open instance log: %v", err)
	}
	defer file.Close() //nolint:errcheck
	if _, err := file.Seek(from, 0); err != nil {
		t.Fatalf("seek instance log: %v", err)
	}
	data, err := readAllLimited(file)
	if err != nil {
		t.Fatalf("read instance log: %v", err)
	}
	return data
}

func readAllLimited(file *os.File) (string, error) {
	var builder strings.Builder
	buffer := make([]byte, 64*1024)
	for {
		n, err := file.Read(buffer)
		if n > 0 {
			builder.Write(buffer[:n])
		}
		if err != nil {
			if errors.Is(err, os.ErrClosed) {
				return builder.String(), err
			}
			break
		}
		if builder.Len() > 64*1024*1024 {
			break
		}
	}
	return builder.String(), nil
}
