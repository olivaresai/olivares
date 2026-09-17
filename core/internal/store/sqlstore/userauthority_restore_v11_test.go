// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// TestPostgresRestoreUserAuthorityCurrentPrefix proves the logical-restore closure
// on a database whose history is the exact current plan (core v11), and that a
// future version, a hole, a misnamed record and a reverted record each refuse
// with the H function ACLs left exactly as the refusal found them.
func TestPostgresRestoreUserAuthorityCurrentPrefix(t *testing.T) {
	ctx := context.Background()
	pg := isolatedPGSplit(t)
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, AdminDSN: pg.Admin, OwnerDSN: pg.Owner, MaxConns: 1}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	super, err := openPGPinnedToEngineSchema(pg.Superuser, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close()

	var tracked, maxVersion int
	if err := super.QueryRow("SELECT count(*), max(version) FROM public.schema_migrations_core").Scan(&tracked, &maxVersion); err != nil {
		t.Fatal(err)
	}
	// The compiled plan is 1..11 then 13 (v12 reserved), so count the compiled plan.
	if want := len(compiledCoreMigrationVersions(loginCapabilitySQLiteDialect(t))); tracked != want || maxVersion != coreSupportedMigrationVersion {
		t.Fatalf("current Open tracked %d versions up to v%d, want the %d compiled versions up to v%d", tracked, maxVersion, want, coreSupportedMigrationVersion)
	}

	snapshot := func() string { return restoreAuthoritySnapshot(t, super) }
	original := snapshot()
	mustExec(t, super, `UPDATE pg_catalog.pg_proc p SET proacl=NULL FROM pg_catalog.pg_namespace n WHERE n.oid=p.pronamespace AND `+restoreAuthorityFunctions)
	stripped := snapshot()
	if stripped == original {
		t.Fatal("fixture: stripping the H function ACLs did not change the catalog")
	}
	closureCfg := cfg
	closureCfg.AdminDSN = "not-a-connection-string"

	for _, tc := range []struct {
		name, mutate, undo, want string
	}{
		{
			name:   "future version",
			mutate: "INSERT INTO public.schema_migrations_core(version,name,applied_at,phase) VALUES (12,'future_core_change','2026-09-13T00:00:00Z','expand')",
			undo:   "DELETE FROM public.schema_migrations_core WHERE version=12",
			want:   "empty or incompatible",
		},
		{
			name:   "hole",
			mutate: "UPDATE public.schema_migrations_core SET version=105 WHERE version=5",
			undo:   "UPDATE public.schema_migrations_core SET version=5 WHERE version=105",
			want:   "not a recognized prefix",
		},
		{
			name:   "misnamed v11",
			mutate: "UPDATE public.schema_migrations_core SET name='evidence_operation_refused_state_renamed' WHERE version=11",
			undo:   "UPDATE public.schema_migrations_core SET name='" + coreEvidenceRefusedMigrationName + "' WHERE version=11",
			want:   "not an exact active record",
		},
		{
			name:   "reverted v11",
			mutate: "UPDATE public.schema_migrations_core SET reverted_at='2026-09-13T00:00:00Z' WHERE version=11",
			undo:   "UPDATE public.schema_migrations_core SET reverted_at=NULL WHERE version=11",
			want:   "not an exact active record",
		},
	} {
		mustExec(t, super, tc.mutate)
		err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: closure = %v, want refusal containing %q", tc.name, err, tc.want)
		}
		if snapshot() != stripped {
			t.Fatalf("%s: refused closure changed the H function owner/definition/ACL", tc.name)
		}
		mustExec(t, super, tc.undo)
		t.Logf("V11_RESTORE_REFUSAL|case=%s|refused=true|acl_unchanged=true", tc.name)
	}

	if err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg); err != nil {
		t.Fatalf("current v11 prefix closure: %v", err)
	}
	if snapshot() != original {
		t.Fatal("current v11 closure did not restore the exact source owner/definition/ACL")
	}
	if err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg); err != nil {
		t.Fatalf("idempotent current v11 closure: %v", err)
	}
	if snapshot() != original {
		t.Fatal("idempotent current v11 closure changed the functions")
	}
	st, err = Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("reopen after current v11 closure: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("V11_RESTORE_CURRENT|tracked=11|closure=restored|idempotent=true|reopen=ok")
}
