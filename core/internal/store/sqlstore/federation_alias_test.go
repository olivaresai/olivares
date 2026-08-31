// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestFederationAliasCodecNormalizesDefault proves the codec half of the U4
// legacy-safety: a stored NULL/empty alias DECODES to "default" (so a pre-U4 row reads as
// the scope's primary), and any alias ENCODES normalized (trimmed/lowercased, empty →
// "default") so case/whitespace cannot fork the same IdP through the unique index.
func TestFederationAliasCodecNormalizesDefault(t *testing.T) {
	base := model.BaseFields{ID: "id1", TenantID: model.SystemTenantID}

	// A legacy record with NO alias key present → decodes to "default".
	got, err := federationConfigCodec.Decode(base, model.Record{"status": "active", "protocol": "oidc"})
	if err != nil {
		t.Fatalf("decode(no alias): %v", err)
	}
	if got.Alias != model.DefaultFederationAlias {
		t.Fatalf("decode(no alias) alias = %q, want %q", got.Alias, model.DefaultFederationAlias)
	}
	// An explicit case/whitespace alias → normalized on decode.
	got, err = federationConfigCodec.Decode(base, model.Record{"alias": "Okta ", "status": "active"})
	if err != nil {
		t.Fatalf("decode(Okta ): %v", err)
	}
	if got.Alias != "okta" {
		t.Fatalf("decode(%q) alias = %q, want okta", "Okta ", got.Alias)
	}
	// Encode folds empty → "default" and case/space → canonical.
	rec, err := federationConfigCodec.Encode(model.FederationConfig{BaseFields: base, Alias: ""})
	if err != nil {
		t.Fatalf("encode(empty): %v", err)
	}
	if rec["alias"] != model.DefaultFederationAlias {
		t.Fatalf("encode(empty) alias = %v, want %q", rec["alias"], model.DefaultFederationAlias)
	}
	rec, err = federationConfigCodec.Encode(model.FederationConfig{BaseFields: base, Alias: "Okta "})
	if err != nil {
		t.Fatalf("encode(Okta ): %v", err)
	}
	if rec["alias"] != "okta" {
		t.Fatalf("encode(%q) alias = %v, want okta", "Okta ", rec["alias"])
	}
}

// TestFederationAliasBackfillConverges proves the U4 migration converges an upgraded
// database onto the shape a fresh one is created with: after the additive reconcile adds
// the NULLABLE alias column (every legacy row NULL) and the new (tenant,target,alias)
// UNIQUE index, reconcileCoreData backfills every NULL alias to "default" so the index
// enforces one default per scope IDENTICALLY to a fresh DB, and the pre-U4 scope-unique
// index is gone. It also proves v4's DROP INDEX IF EXISTS and the backfill are idempotent.
func TestFederationAliasBackfillConverges(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // keep the in-memory schema alive across statements
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no sqlite dialect")
	}
	for _, stmt := range dia.TenancyStmts() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	// The federation_configs table exactly as the CURRENT (U4) descriptor creates it: the
	// alias column + the (tenant_id, target_tenant_id, alias) unique index.
	for _, stmt := range dia.CreateTableStmts(federationConfigDescriptor) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("create federation_configs: %v (%s)", err, stmt)
		}
	}
	if !indexExists(t, db, "federation_configs_idp_uniq") {
		t.Fatal("fresh descriptor must create federation_configs_idp_uniq")
	}
	if indexExists(t, db, "federation_configs_scope_uniq") {
		t.Fatal("the pre-U4 scope-unique index must not be in the current descriptor")
	}

	// Two LEGACY per-scope rows (pre-U4): alias physically NULL. Pin the owning system
	// tenant so the scope guard admits the auth rows.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO "+dialect.ScopeTenantTable+"(tenant_id) VALUES(?)", model.SystemTenantID.String()); err != nil {
		t.Fatal(err)
	}
	ins := func(id, target string) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO federation_configs (id, tenant_id, created_at, updated_at, version, target_tenant_id, alias, protocol, status) "+
				"VALUES (?,?,?,?,1,?,NULL,'oidc','active')",
			id, model.SystemTenantID.String(), "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", target); err != nil {
			t.Fatalf("insert legacy row %s: %v", id, err)
		}
	}
	ins("cfg-global", model.SystemTenantID.String())
	ins("cfg-tenant", "11111111-1111-1111-1111-111111111111")

	// The backfill, from boot's actual starting state: nothing pinned.
	unpinScope(t, ctx, db)
	if err := reconcileCoreData(ctx, db, dia); err != nil {
		t.Fatalf("reconcileCoreData: %v", err)
	}
	assertAuthPartitionPinned(t, ctx, db)

	count := func(where string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM federation_configs WHERE "+where).Scan(&n); err != nil {
			t.Fatalf("count %q: %v", where, err)
		}
		return n
	}
	if n := count("alias = 'default'"); n != 2 {
		t.Fatalf("backfilled 'default' rows = %d, want 2", n)
	}
	if n := count("alias IS NULL OR alias = ''"); n != 0 {
		t.Fatalf("rows with NULL/empty alias after backfill = %d, want 0", n)
	}

	// Idempotent: a second backfill updates nothing.
	if err := reconcileCoreData(ctx, db, dia); err != nil {
		t.Fatalf("reconcileCoreData not idempotent: %v", err)
	}
	if n := count("alias = 'default'"); n != 2 {
		t.Fatalf("after re-run 'default' rows = %d, want 2", n)
	}
	// v4's DROP INDEX IF EXISTS is a harmless no-op on the fresh-shaped table.
	if _, err := db.ExecContext(ctx, "DROP INDEX IF EXISTS federation_configs_scope_uniq"); err != nil {
		t.Fatalf("DROP INDEX IF EXISTS (idempotent): %v", err)
	}
}

// federationConfigsTestDB builds a raw SQLite DB with the tenancy prerequisites and the
// federation_configs table in its current (U4) shape, pinning the system tenant so auth
// rows can be inserted.
func federationConfigsTestDB(t *testing.T) (*sql.DB, dialect.Dialect, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no sqlite dialect")
	}
	for _, stmt := range dia.TenancyStmts() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range dia.CreateTableStmts(federationConfigDescriptor) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("create federation_configs: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO "+dialect.ScopeTenantTable+"(tenant_id) VALUES(?)", model.SystemTenantID.String()); err != nil {
		t.Fatal(err)
	}
	return db, dia, ctx
}

// scopePin reports the tenant ids currently pinned in the SQLite scope-pin table.
// Boot runs with NOTHING pinned, and reconcileCoreData must leave the AUTH-PARTITION
// scope pinned when it is done — deliberately, not incidentally. An EMPTY pin table
// is the PERMISSIVE state: the tripwire triggers only fire WHERE EXISTS(pin)
// (dialect/sqlite.go), which is exactly how the System path opts out of isolation.
// Clearing the pin at the end of boot would therefore hand whatever runs next the
// privileged System behavior by default; leaving it means a stray unbound write to
// another tenant's row aborts loudly instead. (On Postgres there is nothing to leave:
// the bind is a transaction-local GUC that evaporates on commit.)
func scopePin(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT tenant_id FROM "+dialect.ScopeTenantTable)
	if err != nil {
		t.Fatalf("read scope pin: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan scope pin: %v", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("scope pin rows: %v", err)
	}
	return out
}

// assertAuthPartitionPinned is the post-backfill contract: exactly the auth partition,
// pinned.
func assertAuthPartitionPinned(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	got := scopePin(t, ctx, db)
	if len(got) != 1 || got[0] != model.SystemTenantID.String() {
		t.Fatalf("scope pin after the backfill = %v, want exactly [%s] — an empty pin is the PERMISSIVE System state, so boot must not end there",
			got, model.SystemTenantID)
	}
}

// unpinScope drops the ambient scope pin so the call under test starts exactly where
// boot does: unpinned. Without this the tests would prove only that the backfill works
// when someone else has already pinned the right scope — which is precisely the gap
// that let the Postgres boot failure ship, since production pinned nothing.
func unpinScope(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, "DELETE FROM "+dialect.ScopeTenantTable); err != nil {
		t.Fatalf("clear scope pin: %v", err)
	}
}

func insertLegacyFederationRow(t *testing.T, ctx context.Context, db *sql.DB, id, target string) {
	t.Helper()
	if _, err := db.ExecContext(ctx,
		"INSERT INTO federation_configs (id, tenant_id, created_at, updated_at, version, target_tenant_id, alias, protocol, status) "+
			"VALUES (?,?,?,?,1,?,NULL,'oidc','active')",
		id, model.SystemTenantID.String(), "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", target); err != nil {
		t.Fatalf("insert legacy row %s: %v", id, err)
	}
}

// TestFederationAliasBackfillDedupsDuplicates proves the U4 backfill can NEVER brick
// boot: in the pathological mixed-version window two NULL-alias rows can exist for ONE scope
// (v4 dropped the old scope-unique index and NULL is index-distinct); the backfill promotes
// exactly one to "default" and renames the other to a unique, non-colliding "dup-<id>" —
// no unique-index violation, no row lost.
func TestFederationAliasBackfillDedupsDuplicates(t *testing.T) {
	db, dia, ctx := federationConfigsTestDB(t)
	scope := "11111111-1111-1111-1111-111111111111"
	insertLegacyFederationRow(t, ctx, db, "cfg-a", scope)
	insertLegacyFederationRow(t, ctx, db, "cfg-b", scope) // SAME scope — the pathological duplicate

	unpinScope(t, ctx, db)
	if err := reconcileCoreData(ctx, db, dia); err != nil {
		t.Fatalf("reconcileCoreData must not brick on duplicate NULL rows: %v", err)
	}
	assertAuthPartitionPinned(t, ctx, db)
	count := func(where string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM federation_configs WHERE "+where).Scan(&n); err != nil {
			t.Fatalf("count %q: %v", where, err)
		}
		return n
	}
	if n := count("alias = 'default'"); n != 1 {
		t.Fatalf("'default' rows = %d, want exactly 1", n)
	}
	if n := count("alias LIKE 'dup-%'"); n != 1 {
		t.Fatalf("'dup-' rows = %d, want exactly 1", n)
	}
	if n := count("alias IS NULL OR alias = ''"); n != 0 {
		t.Fatalf("NULL/empty rows = %d, want 0", n)
	}
	// The promoted "default" is the lowest-id row (deterministic).
	var defID string
	if err := db.QueryRowContext(ctx, "SELECT id FROM federation_configs WHERE alias='default'").Scan(&defID); err != nil {
		t.Fatal(err)
	}
	if defID != "cfg-a" {
		t.Fatalf("default id = %q, want cfg-a (lowest id)", defID)
	}
	// Idempotent + collision-proof on a second boot.
	if err := reconcileCoreData(ctx, db, dia); err != nil {
		t.Fatalf("reconcileCoreData not idempotent after dedup: %v", err)
	}
	if n := count("alias = 'default'"); n != 1 {
		t.Fatalf("after re-run 'default' rows = %d, want 1", n)
	}
}

// TestFederationAliasBackfillLeavesWritesFailClosed pins the SECURITY consequence of the
// scope the backfill leaves bound, which is the whole reason it does not clear it.
//
// On SQLite the tripwire triggers fire only WHERE EXISTS(pin) (dialect/sqlite.go), so an
// EMPTY pin table is the PERMISSIVE state — the deliberate opt-out the System path uses.
// If boot ended with the pin cleared, the next raw write that forgot to bind a scope would
// be silently accepted for ANY tenant. With the auth partition left pinned it aborts.
// This test fails if someone "tidies up" by clearing the pin at the end of the backfill.
func TestFederationAliasBackfillLeavesWritesFailClosed(t *testing.T) {
	db, dia, ctx := federationConfigsTestDB(t)
	insertLegacyFederationRow(t, ctx, db, "cfg-a", model.SystemTenantID.String())

	// Boot's actual starting state, then the backfill.
	unpinScope(t, ctx, db)
	if err := reconcileCoreData(ctx, db, dia); err != nil {
		t.Fatalf("reconcileCoreData: %v", err)
	}
	assertAuthPartitionPinned(t, ctx, db)

	// A raw write that forgets to bind, aimed at ANOTHER tenant's row, is refused.
	foreign := "22222222-2222-2222-2222-222222222222"
	_, err := db.ExecContext(ctx,
		"INSERT INTO federation_configs (id, tenant_id, created_at, updated_at, version, target_tenant_id, alias, protocol, status) "+
			"VALUES (?,?,?,?,1,?,'default','oidc','active')",
		"cfg-foreign", foreign, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", foreign)
	if err == nil {
		t.Fatal("an unbound raw write for a FOREIGN tenant was accepted after boot: the scope pin was cleared, so the tripwire is inert and cross-tenant writes pass silently")
	}
	if !strings.Contains(err.Error(), "tenant scope violation") {
		t.Fatalf("write was refused, but not by the scope tripwire: %v", err)
	}

	// The auth partition itself still writes — the pin IS that partition, so boot's own
	// remaining work is unaffected.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO federation_configs (id, tenant_id, created_at, updated_at, version, target_tenant_id, alias, protocol, status) "+
			"VALUES (?,?,?,?,1,?,'second','oidc','active')",
		"cfg-b", model.SystemTenantID.String(), "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z",
		model.SystemTenantID.String()); err != nil {
		t.Fatalf("a write to the pinned auth partition must still succeed: %v", err)
	}
}
