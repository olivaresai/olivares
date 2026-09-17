// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// distinctProjectionEntity mirrors the shape the K3 ChannelGrant catalog
// projects over: a workspace lineage, a subject pair, a state, a boolean bit, a
// projected UUID column and a NULLABLE expiry stored as canonical timestamp text.
var distinctProjectionEntity = model.EntityDescriptor{
	Kind:  "dpt.grant",
	Table: "dpt_grant",
	WorkspaceLineage: model.WorkspaceLineageSpec{
		Column: "workspace_id", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetHidden,
	},
	Fields: []model.FieldSpec{
		{Name: "workspace_id", Kind: model.KindUUID},
		{Name: "channel_id", Kind: model.KindUUID, Nullable: true},
		{Name: "subject_kind", Kind: model.KindText},
		{Name: "subject_ref", Kind: model.KindText},
		{Name: "state", Kind: model.KindText},
		{Name: "can_read", Kind: model.KindBool},
		{Name: "expires_at", Kind: model.KindTimestamp, Nullable: true},
	},
	Indexes: []model.IndexSpec{{
		Name: "dpt_grant_catalog",
		Columns: []string{
			model.ColTenantID, "workspace_id", "subject_kind", "subject_ref", "state",
			"can_read", "channel_id", "expires_at",
		},
	}},
}

func registerDistinctProjectionEntity(reg store.ExtensionRegistry) error {
	return reg.Register(distinctProjectionEntity)
}

type distinctProjectionRow struct {
	workspace model.ID
	channel   string
	kind, ref string
	state     string
	canRead   bool
	expires   *time.Time
}

func seedDistinctProjectionRows(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
	rows []distinctProjectionRow,
) {
	t.Helper()
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(distinctProjectionEntity.Kind)
		if err != nil {
			return err
		}
		for _, row := range rows {
			record := model.Record{
				"workspace_id": row.workspace.String(), "subject_kind": row.kind,
				"subject_ref": row.ref, "state": row.state, "can_read": row.canRead,
			}
			if row.channel != "" {
				record["channel_id"] = row.channel
			}
			if row.expires != nil {
				record["expires_at"] = model.NewTimestamp(*row.expires).String()
			}
			if _, err := repo.Create(ctx, record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed distinct projection rows: %v", err)
	}
}

func distinctProjectionWorkspaces(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
) (model.ID, model.ID) {
	t.Helper()
	ctx := context.Background()
	var defaultWS, other model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		def, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		defaultWS = def.ID
		created, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "Other", Slug: "other", Status: model.StatusActive,
		})
		other = created.ID
		return err
	}); err != nil {
		t.Fatalf("workspaces: %v", err)
	}
	return defaultWS, other
}

// runDistinctProjectionContract is the engine-neutral behaviour every engine
// must reproduce: distinct ascending values, keyset anchor, HasMore lookahead,
// ORed subject alternatives, the unset-or-after expiry predicate against the
// observed database time, NULL exclusion, tenant isolation and workspace
// confinement. Its assertions are the same on SQLite and PostgreSQL.
func runDistinctProjectionContract(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	tenant := provisionTenant(t, st, "dpt-a")
	otherTenant := provisionTenant(t, st, "dpt-b")
	defaultWS, otherWS := distinctProjectionWorkspaces(t, st, tenant)
	_, otherTenantWS := distinctProjectionWorkspaces(t, st, otherTenant)

	// Canonical lowercase UUID text orders identically as TEXT on both engines.
	channelA := "00000000-0000-7000-8000-00000000000a"
	channelB := "00000000-0000-7000-8000-00000000000b"
	channelC := "00000000-0000-7000-8000-00000000000c"
	channelD := "00000000-0000-7000-8000-00000000000d"
	channelE := "00000000-0000-7000-8000-00000000000e"
	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	seedDistinctProjectionRows(t, st, tenant, []distinctProjectionRow{
		// user subject: A twice (duplicate through two grant rows), B expired,
		// C future expiry, D write-only, E revoked.
		{workspace: defaultWS, channel: channelA, kind: "user", ref: "u1", state: "active", canRead: true},
		{workspace: defaultWS, channel: channelA, kind: "user", ref: "u1", state: "active", canRead: true, expires: &future},
		{workspace: defaultWS, channel: channelB, kind: "user", ref: "u1", state: "active", canRead: true, expires: &past},
		{workspace: defaultWS, channel: channelC, kind: "user", ref: "u1", state: "active", canRead: true, expires: &future},
		{workspace: defaultWS, channel: channelD, kind: "user", ref: "u1", state: "active", canRead: false},
		{workspace: defaultWS, channel: channelE, kind: "user", ref: "u1", state: "revoked", canRead: true},
		// group subject: B and E through the group (E duplicates a revoked direct).
		{workspace: defaultWS, channel: channelB, kind: "user_group", ref: "g1", state: "active", canRead: true},
		{workspace: defaultWS, channel: channelE, kind: "user_group", ref: "g1", state: "active", canRead: true},
		// unrelated subject and a NULL projected column.
		{workspace: defaultWS, channel: channelD, kind: "user", ref: "u9", state: "active", canRead: true},
		{workspace: defaultWS, channel: "", kind: "user", ref: "u1", state: "active", canRead: true},
		// other workspace of the same tenant.
		{workspace: otherWS, channel: channelC, kind: "user", ref: "u1", state: "active", canRead: true},
	})
	seedDistinctProjectionRows(t, st, otherTenant, []distinctProjectionRow{
		{workspace: otherTenantWS, channel: channelA, kind: "user", ref: "u1", state: "active", canRead: true},
	})

	project := func(workspace model.ID, after string, limit int, subjects ...[2]string) store.DistinctPage {
		t.Helper()
		var page store.DistinctPage
		err := st.View(ctx, tenant, func(raw store.Scope) error {
			sc, err := store.ConfineWorkspace(ctx, raw, workspace)
			if err != nil {
				return err
			}
			clock, ok := sc.(store.TransactionClock)
			if !ok {
				return errors.New("confined scope lost the transaction clock")
			}
			now, err := clock.TransactionNow(ctx)
			if err != nil {
				return err
			}
			repo, err := sc.Ext(distinctProjectionEntity.Kind)
			if err != nil {
				return err
			}
			projector, ok := repo.(store.DistinctProjector)
			if !ok {
				return errors.New("confined repository lost the distinct projector")
			}
			projection := store.DistinctProjection{
				Column: "channel_id", After: after, Limit: limit,
				Filters: []model.Filter{
					{Column: "state", Op: model.OpEq, Value: "active"},
					{Column: "can_read", Op: model.OpEq, Value: true},
					{Column: "expires_at", Op: model.OpUnsetOrGt, Value: now.String()},
				},
			}
			for _, subject := range subjects {
				projection.AnyOf = append(projection.AnyOf, []model.Filter{
					{Column: "subject_kind", Op: model.OpEq, Value: subject[0]},
					{Column: "subject_ref", Op: model.OpEq, Value: subject[1]},
				})
			}
			page, err = projector.ProjectDistinct(ctx, projection)
			return err
		})
		if err != nil {
			t.Fatalf("project: %v", err)
		}
		return page
	}

	user := [2]string{"user", "u1"}
	group := [2]string{"user_group", "g1"}
	if got := project(defaultWS, "", 10, user); !reflect.DeepEqual(got.Values, []string{channelA, channelC}) || got.HasMore {
		t.Fatalf("user projection = %+v, want [A C] (expired B, write-only D and revoked E excluded)", got)
	}
	if got := project(defaultWS, "", 10, user, group); !reflect.DeepEqual(got.Values, []string{channelA, channelB, channelC, channelE}) || got.HasMore {
		t.Fatalf("user+group projection = %+v, want [A B C E]", got)
	}
	if got := project(defaultWS, "", 2, user, group); !reflect.DeepEqual(got.Values, []string{channelA, channelB}) || !got.HasMore {
		t.Fatalf("limit lookahead = %+v, want [A B] has_more", got)
	}
	if got := project(defaultWS, channelB, 2, user, group); !reflect.DeepEqual(got.Values, []string{channelC, channelE}) || got.HasMore {
		t.Fatalf("keyset anchor after B = %+v, want [C E]", got)
	}
	if got := project(defaultWS, channelE, 2, user, group); len(got.Values) != 0 || got.HasMore {
		t.Fatalf("exhausted anchor = %+v, want empty", got)
	}
	if got := project(otherWS, "", 10, user, group); !reflect.DeepEqual(got.Values, []string{channelC}) || got.HasMore {
		t.Fatalf("other workspace projection = %+v, want [C]", got)
	}
	if got := project(defaultWS, "", 10); !reflect.DeepEqual(got.Values, []string{channelA, channelB, channelC, channelD, channelE}) {
		t.Fatalf("no-alternative projection = %+v, want every current read grant of the workspace", got)
	}

	// Tenant isolation: the other tenant's rows are invisible even under the
	// same subject and channel.
	var foreign store.DistinctPage
	if err := st.View(ctx, otherTenant, func(raw store.Scope) error {
		sc, err := store.ConfineWorkspace(ctx, raw, otherTenantWS)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(distinctProjectionEntity.Kind)
		if err != nil {
			return err
		}
		foreign, err = repo.(store.DistinctProjector).ProjectDistinct(ctx, store.DistinctProjection{
			Column: "channel_id", Limit: 10,
			AnyOf: [][]model.Filter{{
				{Column: "subject_kind", Op: model.OpEq, Value: "user"},
				{Column: "subject_ref", Op: model.OpEq, Value: "u1"},
			}},
		})
		return err
	}); err != nil {
		t.Fatalf("foreign projection: %v", err)
	}
	if !reflect.DeepEqual(foreign.Values, []string{channelA}) {
		t.Fatalf("foreign tenant projection = %+v, want only its own A", foreign)
	}

	// Invalid shapes are refused before any SQL is rendered.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(distinctProjectionEntity.Kind)
		if err != nil {
			return err
		}
		projector := repo.(store.DistinctProjector)
		for _, bad := range []store.DistinctProjection{
			{Column: "channel_id", Limit: 0},
			{Column: "channel_id", Limit: store.DistinctProjectionMaxLimit + 1},
			{Column: "", Limit: 1},
			{Column: "channel_id", Limit: 1, AnyOf: [][]model.Filter{{}}},
			{Column: "channel_id", Limit: 1, Filters: []model.Filter{{Column: "state", Op: "bogus"}}},
		} {
			if _, err := projector.ProjectDistinct(ctx, bad); !errors.Is(err, store.ErrInvalidProjection) {
				return errors.New("invalid projection was not refused: " + bad.Column)
			}
		}
		if _, err := projector.ProjectDistinct(ctx, store.DistinctProjection{Column: "nope", Limit: 1}); !errors.Is(err, store.ErrUnknownEntity) {
			return errors.New("unknown column was not refused")
		}
		if _, err := projector.ProjectDistinct(ctx, store.DistinctProjection{
			Column: "channel_id", Limit: 1,
			Filters: []model.Filter{{Column: "nope", Op: model.OpEq, Value: "x"}},
		}); !errors.Is(err, store.ErrUnknownEntity) {
			return errors.New("unknown filter column was not refused")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// runDistinctProjectionUnionContract is the cross-engine behaviour of the
// per-alternative union shape: overlapping subjects deduplicate, disjoint
// subjects interleave in column order, an absent subject contributes nothing,
// the page cut can fall inside one arm or between arms, and the lookahead and
// keyset anchor are those of the global union, not of any single arm.
func runDistinctProjectionUnionContract(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	tenant := provisionTenant(t, st, "dpt-union")
	defaultWS, _ := distinctProjectionWorkspaces(t, st, tenant)
	id := func(n int) string {
		return "00000000-0000-7000-8000-0000000000" + string(rune('0'+n/10)) + string(rune('0'+n%10))
	}
	one := [2]string{"user", "s1"}
	two := [2]string{"user_group", "s2"}
	three := [2]string{"user_group", "s3"}
	absent := [2]string{"user", "nobody"}
	seed := func(subject [2]string, channels ...int) {
		rows := make([]distinctProjectionRow, 0, len(channels))
		for _, n := range channels {
			rows = append(rows, distinctProjectionRow{
				workspace: defaultWS, channel: id(n), kind: subject[0], ref: subject[1], state: "active", canRead: true,
			})
		}
		seedDistinctProjectionRows(t, st, tenant, rows)
	}
	// s1 holds 1 2 3 4 6, s2 holds 2 5 6 (2 and 6 overlap s1), s3 holds 7 alone.
	seed(one, 1, 2, 3, 4, 6)
	seed(two, 2, 5, 6)
	seed(three, 7)
	project := func(after string, limit int, subjects ...[2]string) store.DistinctPage {
		t.Helper()
		var page store.DistinctPage
		err := st.View(ctx, tenant, func(raw store.Scope) error {
			sc, err := store.ConfineWorkspace(ctx, raw, defaultWS)
			if err != nil {
				return err
			}
			repo, err := sc.Ext(distinctProjectionEntity.Kind)
			if err != nil {
				return err
			}
			projection := store.DistinctProjection{
				Column: "channel_id", After: after, Limit: limit,
				Filters: []model.Filter{
					{Column: "state", Op: model.OpEq, Value: "active"},
					{Column: "can_read", Op: model.OpEq, Value: true},
				},
			}
			for _, subject := range subjects {
				projection.AnyOf = append(projection.AnyOf, []model.Filter{
					{Column: "subject_kind", Op: model.OpEq, Value: subject[0]},
					{Column: "subject_ref", Op: model.OpEq, Value: subject[1]},
				})
			}
			page, err = repo.(store.DistinctProjector).ProjectDistinct(ctx, projection)
			return err
		})
		if err != nil {
			t.Fatalf("project: %v", err)
		}
		return page
	}
	want := func(name string, got store.DistinctPage, hasMore bool, channels ...int) {
		t.Helper()
		expected := make([]string, 0, len(channels))
		for _, n := range channels {
			expected = append(expected, id(n))
		}
		if len(expected) == 0 {
			expected = nil
		}
		if !reflect.DeepEqual(got.Values, expected) || got.HasMore != hasMore {
			t.Fatalf("%s = %+v, want %v has_more=%t", name, got, expected, hasMore)
		}
	}
	all := [][2]string{one, two, three, absent}
	want("global union", project("", 10, all...), false, 1, 2, 3, 4, 5, 6, 7)
	want("exact-fit limit", project("", 7, all...), false, 1, 2, 3, 4, 5, 6, 7)
	want("lookahead one short", project("", 6, all...), true, 1, 2, 3, 4, 5, 6)
	// The cut falls on an overlapping value: 2 is carried by s1 and s2 and is
	// returned once; the lookahead is 3 from s1 alone.
	want("cut on overlap", project("", 2, all...), true, 1, 2)
	// Continue strictly after 2: 3 4 from s1; the lookahead 5 belongs to s2 only.
	want("anchor after overlap", project(id(2), 2, all...), true, 3, 4)
	// After 4 the next two are 5 (s2 only) and 6 (both); 7 remains in s3.
	want("cut between arms", project(id(4), 2, all...), true, 5, 6)
	want("last arm alone", project(id(6), 2, all...), false, 7)
	want("exhausted", project(id(7), 2, all...), false)
	// A limit of one sees the smallest value of any arm and the true lookahead.
	want("limit one", project("", 1, three, one), true, 1)
	want("limit one after s1", project(id(6), 1, three, one), false, 7)
	// Disjoint arms interleave; an absent subject changes nothing.
	want("disjoint", project("", 10, two, three), false, 2, 5, 6, 7)
	want("absent only", project("", 10, absent), false)
	want("absent plus one", project("", 10, absent, three), false, 7)
	// A single alternative is the same bounded arm with the union wrapper.
	want("single arm", project(id(1), 2, one), true, 2, 3)
}

// TestDistinctProjectionRendersOneBoundedArmPerAlternative pins the statement
// shape the engine renders, without executing it: a common-only projection is
// one arm; every alternative becomes its own arm that repeats every common
// predicate and the anchor, orders and caps itself at limit+1, and the arms
// are unioned, ordered and capped again. The bound-value count of the rendered
// text equals the store contract's arithmetic, and the worst admissible shape
// renders under the ceiling.
func TestDistinctProjectionRendersOneBoundedArmPerAlternative(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerDistinctProjectionEntity)
	tenant := provisionTenant(t, st, "dpt-render")
	render := func(p store.DistinctProjection) distinctProjectionStatement {
		t.Helper()
		var statement distinctProjectionStatement
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(distinctProjectionEntity.Kind)
			if err != nil {
				return err
			}
			generic, ok := repo.(*genericRepo)
			if !ok {
				return errors.New("tenant scope did not yield the generic repository")
			}
			statement, err = generic.renderDistinctProjection(p)
			return err
		}); err != nil {
			t.Fatalf("render: %v", err)
		}
		return statement
	}
	common := []model.Filter{
		{Column: "workspace_id", Op: model.OpEq, Value: "ws"},
		{Column: "state", Op: model.OpEq, Value: "active"},
		{Column: "can_read", Op: model.OpEq, Value: true},
		{Column: "expires_at", Op: model.OpUnsetOrGt, Value: "now"},
	}
	plain := render(store.DistinctProjection{Column: "channel_id", Limit: 50, Filters: common, After: "anchor"})
	if plain.arms != 0 || strings.Contains(plain.text, "UNION") || strings.Count(plain.text, "SELECT") != 1 {
		t.Fatalf("common-only projection rendered %d arms: %s", plain.arms, plain.text)
	}
	if want := "SELECT DISTINCT channel_id FROM dpt_grant WHERE tenant_id = ? AND channel_id IS NOT NULL AND workspace_id = ? AND state = ? AND can_read = ? AND (expires_at IS NULL OR expires_at > ?) AND channel_id > ? ORDER BY channel_id ASC LIMIT 51"; plain.text != want {
		t.Fatalf("common-only text = %s\nwant %s", plain.text, want)
	}
	if len(plain.args) != 6 || strings.Count(plain.text, "?") != 6 {
		t.Fatalf("common-only bound values = %d (%d placeholders), want 6", len(plain.args), strings.Count(plain.text, "?"))
	}

	subjects := [][]model.Filter{
		{{Column: "subject_kind", Op: model.OpEq, Value: "user"}, {Column: "subject_ref", Op: model.OpEq, Value: "u1"}},
		{{Column: "subject_kind", Op: model.OpEq, Value: "user_group"}, {Column: "subject_ref", Op: model.OpEq, Value: "g1"}},
		{{Column: "subject_kind", Op: model.OpEq, Value: "user_group"}, {Column: "subject_ref", Op: model.OpEq, Value: "g2"}},
	}
	p := store.DistinctProjection{Column: "channel_id", Limit: 50, Filters: common, After: "anchor", AnyOf: subjects}
	union := render(p)
	if union.arms != 3 || strings.Count(union.text, " UNION ") != 2 {
		t.Fatalf("three alternatives rendered %d arms / %d unions: %s", union.arms, strings.Count(union.text, " UNION "), union.text)
	}
	armText := func(index int, kind, ref string) string {
		return "SELECT channel_id FROM (SELECT DISTINCT channel_id FROM dpt_grant WHERE tenant_id = ? AND channel_id IS NOT NULL AND workspace_id = ? AND state = ? AND can_read = ? AND (expires_at IS NULL OR expires_at > ?) AND channel_id > ? AND subject_kind = ? AND subject_ref = ? ORDER BY channel_id ASC LIMIT 51) AS a" + string(rune('0'+index))
	}
	want := armText(0, "user", "u1") + " UNION " + armText(1, "user_group", "g1") + " UNION " + armText(2, "user_group", "g2") + " ORDER BY channel_id ASC LIMIT 51"
	if union.text != want {
		t.Fatalf("union text = %s\nwant %s", union.text, want)
	}
	if got, contract := len(union.args), p.BoundValues(); got != contract || strings.Count(union.text, "?") != got || got != 3*(6+2) {
		t.Fatalf("union bound values = %d, contract %d, placeholders %d, want 24", got, contract, strings.Count(union.text, "?"))
	}
	// Every arm carries the common values then its own, in placeholder order.
	for arm := 0; arm < 3; arm++ {
		base := arm * 8
		if union.args[base] != tenant.String() || union.args[base+1] != "ws" || union.args[base+2] != "active" ||
			union.args[base+3] != true || union.args[base+4] != "now" || union.args[base+5] != "anchor" ||
			union.args[base+6] != subjects[arm][0].Value || union.args[base+7] != subjects[arm][1].Value {
			t.Fatalf("arm %d bound values = %v", arm, union.args[base:base+8])
		}
	}
	// The SQLite dialect leaves the "?" placeholders as rendered; the PostgreSQL
	// dialect numbers every one of them once, in order, so each arm's bound
	// values land on that arm's own placeholders.
	if rebound := st.(*sqlStore).dia.Rebind(union.text); rebound != union.text {
		t.Fatalf("sqlite dialect rewrote the statement: %s", rebound)
	}
	postgres, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("postgres dialect unavailable")
	}
	numbered := postgres.Rebind(union.text)
	if strings.Count(numbered, "?") != 0 || strings.Count(numbered, "$") != len(union.args) ||
		!strings.Contains(numbered, "tenant_id = $1 ") || !strings.Contains(numbered, "subject_ref = $24 ") {
		t.Fatalf("postgres rebind did not number %d placeholders in order: %s", len(union.args), numbered)
	}

	// The worst admissible shape renders, and its bound values match the
	// contract's constant, under the ceiling.
	worst := store.DistinctProjection{Column: "channel_id", Limit: store.DistinctProjectionMaxLimit, After: "anchor"}
	for i := 0; i < store.DistinctProjectionMaxFilters; i++ {
		worst.Filters = append(worst.Filters, model.Filter{Column: "state", Op: model.OpEq, Value: i})
	}
	for i := 0; i < store.DistinctProjectionMaxAlternatives; i++ {
		var alternative []model.Filter
		for j := 0; j < store.DistinctProjectionMaxAlternativeFilters; j++ {
			alternative = append(alternative, model.Filter{Column: "subject_ref", Op: model.OpEq, Value: j})
		}
		worst.AnyOf = append(worst.AnyOf, alternative)
	}
	rendered := render(worst)
	if len(rendered.args) != store.DistinctProjectionWorstCaseBoundValues || rendered.arms != store.DistinctProjectionMaxAlternatives ||
		len(rendered.args) > store.DistinctProjectionMaxBoundValues || strings.Count(rendered.text, "?") != len(rendered.args) {
		t.Fatalf("worst shape: %d bound values, %d arms; want %d under %d",
			len(rendered.args), rendered.arms, store.DistinctProjectionWorstCaseBoundValues, store.DistinctProjectionMaxBoundValues)
	}
	// And it executes on SQLite, which has the lower ceiling.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(distinctProjectionEntity.Kind)
		if err != nil {
			return err
		}
		_, err = repo.(store.DistinctProjector).ProjectDistinct(ctx, worst)
		return err
	}); err != nil {
		t.Fatalf("worst admissible shape did not execute on sqlite: %v", err)
	}

	// Nil alternatives are refused before rendering, and an alternative on an
	// unknown column is refused by the same validator as the common filters.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(distinctProjectionEntity.Kind)
		if err != nil {
			return err
		}
		generic := repo.(*genericRepo)
		if _, err := generic.renderDistinctProjection(store.DistinctProjection{Column: "channel_id", Limit: 1, AnyOf: [][]model.Filter{nil}}); !errors.Is(err, store.ErrInvalidProjection) {
			return errors.New("nil alternative rendered")
		}
		if _, err := generic.renderDistinctProjection(store.DistinctProjection{
			Column: "channel_id", Limit: 1,
			AnyOf: [][]model.Filter{{{Column: "nope", Op: model.OpEq, Value: "x"}}},
		}); !errors.Is(err, store.ErrUnknownEntity) {
			return errors.New("unknown alternative column rendered")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDistinctProjectionSQLite(t *testing.T) {
	st := openSQLiteTest(t, registerDistinctProjectionEntity)
	runDistinctProjectionContract(t, st)
	runDistinctProjectionUnionContract(t, st)
}

// TestDistinctProjectionPostgres runs the same contract on an isolated
// PostgreSQL database in the split-owner topology. It skips only when no server
// is configured and fails when OLIVARES_TEST_POSTGRES_REQUIRED demands the leg.
func TestDistinctProjectionPostgres(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 4,
	}, registerDistinctProjectionEntity)
	if err != nil {
		t.Fatalf("open split-owner projection store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runDistinctProjectionContract(t, st)
	runDistinctProjectionUnionContract(t, st)
}

// TestUnsetOrGtFilterOnList proves the composite operator through the ordinary
// List path too, so the projection and the row list agree on what "current" means.
func TestUnsetOrGtFilterOnList(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerDistinctProjectionEntity)
	tenant := provisionTenant(t, st, "dpt-list")
	defaultWS, _ := distinctProjectionWorkspaces(t, st, tenant)
	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	seedDistinctProjectionRows(t, st, tenant, []distinctProjectionRow{
		{workspace: defaultWS, channel: "x", kind: "user", ref: "never", state: "active", canRead: true},
		{workspace: defaultWS, channel: "x", kind: "user", ref: "past", state: "active", canRead: true, expires: &past},
		{workspace: defaultWS, channel: "x", kind: "user", ref: "future", state: "active", canRead: true, expires: &future},
	})
	var refs []string
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		now, err := sc.(store.TransactionClock).TransactionNow(ctx)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(distinctProjectionEntity.Kind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{
			{Column: "expires_at", Op: model.OpUnsetOrGt, Value: now.String()},
		}, Sort: []model.Sort{{Column: "subject_ref"}}})
		for _, row := range rows {
			refs = append(refs, row.String("subject_ref"))
		}
		return err
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !reflect.DeepEqual(refs, []string{"future", "never"}) {
		t.Fatalf("unset-or-after list = %v, want [future never]", refs)
	}
}
