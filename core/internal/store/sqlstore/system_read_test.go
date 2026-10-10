// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func systemReadFixture(t *testing.T) (*sqlStore, string, model.TenantID) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "system-read.db")
	st, err := Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
	}, nil)
	if err != nil {
		t.Fatalf("open file-backed System fixture: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tenant := provisionTenant(t, st, "system-read")
	ss := st.(*sqlStore)
	directoryEpochTestWantSQLiteBaseline(t, ss.db)
	return ss, dsn, tenant
}

func systemReadWAL(t *testing.T, dsn string) []byte {
	t.Helper()
	b, err := os.ReadFile(dsn + "-wal")
	if err != nil {
		t.Fatalf("read System fixture WAL: %v", err)
	}
	return b
}

func TestReadOnlySystemDoesNotWriteSQLite(t *testing.T) {
	ctx := context.Background()
	ss, dsn, tenant := systemReadFixture(t)
	reads := map[string]func(*testing.T, store.SystemScope) error{
		"list_visible": func(t *testing.T, sys store.SystemScope) error {
			orgs, authoritative, err := sys.ListOrgsVisible(ctx)
			if err == nil && (!authoritative || len(orgs) != 2) {
				t.Fatalf("visible inventory: orgs=%d authoritative=%t", len(orgs), authoritative)
			}
			return err
		},
		"list": func(t *testing.T, sys store.SystemScope) error {
			orgs, err := sys.ListOrgs(ctx)
			if err == nil && len(orgs) != 2 {
				t.Fatalf("inventory: orgs=%d", len(orgs))
			}
			return err
		},
		"get": func(t *testing.T, sys store.SystemScope) error {
			org, err := sys.GetOrg(ctx, tenant)
			if err == nil && org.TenantID != tenant {
				t.Fatalf("GetOrg tenant=%s, want %s", org.TenantID, tenant)
			}
			return err
		},
		"verify": func(t *testing.T, sys store.SystemScope) error {
			report, err := sys.Verify(ctx, tenant, 1)
			if err == nil && (!report.OK || report.Checked == 0) {
				t.Fatalf("audit verification: %+v", report)
			}
			return err
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			before := systemReadWAL(t, dsn)
			for i := 0; i < 3; i++ {
				if err := ss.System(ctx, func(sys store.SystemScope) error { return read(t, sys) }); err != nil {
					t.Fatalf("read-only System: %v", err)
				}
			}
			directoryEpochTestWantSQLiteBaseline(t, ss.db)
			if !bytes.Equal(before, systemReadWAL(t, dsn)) {
				t.Fatal("canonical read-only System changed the file-backed SQLite WAL")
			}
		})
	}
}

func TestSystemReadOptimizationPreservesWritesSQLite(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"mixed_read_write", "idempotent_mutator"} {
		t.Run(name, func(t *testing.T) {
			ss, dsn, tenant := systemReadFixture(t)
			before := systemReadWAL(t, dsn)
			err := ss.System(ctx, func(sys store.SystemScope) error {
				if _, _, err := sys.ListOrgsVisible(ctx); err != nil {
					return err
				}
				if name == "idempotent_mutator" {
					_, err := sys.EnsureSystemTenant(ctx)
					return err
				}
				org, err := sys.SetOrgRegion(ctx, tenant, "eu-west-1")
				if err == nil && org.DataRegion != "eu-west-1" {
					t.Fatalf("mutation result: region=%q", org.DataRegion)
				}
				return err
			})
			if err != nil {
				t.Fatalf("System mutation: %v", err)
			}
			if bytes.Equal(before, systemReadWAL(t, dsn)) {
				t.Fatal("a System mutator was rolled back by the read shortcut")
			}
			if name == "mixed_read_write" {
				if err := ss.System(ctx, func(sys store.SystemScope) error {
					org, err := sys.GetOrg(ctx, tenant)
					if err == nil && org.DataRegion != "eu-west-1" {
						t.Fatalf("persisted region=%q", org.DataRegion)
					}
					return err
				}); err != nil {
					t.Fatalf("read committed mutation: %v", err)
				}
			}
			systemReadWantCanonical(t, ss)
		})
	}
}

func TestSystemReadOptimizationRestoresNoncanonicalSQLite(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"real_tenant_write", "absent", "duplicate", "system_blob", "invalid_text"} {
		t.Run(name, func(t *testing.T) {
			ss, dsn, tenant := systemReadFixture(t)
			var err error
			switch name {
			case "real_tenant_write":
				err = ss.Mutate(ctx, tenant, func(sc store.Scope) error {
					_, err := sc.Identities().Create(ctx, model.Identity{
						Name: "before System", Kind: "service", ExternalID: "system-read:identity",
					})
					return err
				})
			case "absent":
				_, err = ss.db.ExecContext(ctx, "DELETE FROM main."+dialect.ScopeTenantTable)
			case "duplicate":
				_, err = ss.db.ExecContext(ctx, "INSERT INTO main."+dialect.ScopeTenantTable+"(tenant_id) VALUES (?)", model.SystemTenantID.String())
			case "system_blob":
				_, err = ss.db.ExecContext(ctx, "UPDATE main."+dialect.ScopeTenantTable+" SET tenant_id = CAST(? AS BLOB)", model.SystemTenantID.String())
			case "invalid_text":
				_, err = ss.db.ExecContext(ctx, "UPDATE main."+dialect.ScopeTenantTable+" SET tenant_id = ?", "not-a-tenant")
			}
			if err != nil {
				t.Fatalf("establish noncanonical entry: %v", err)
			}
			if name == "real_tenant_write" {
				var pin string
				if err := ss.db.QueryRowContext(ctx, "SELECT tenant_id FROM main."+dialect.ScopeTenantTable).Scan(&pin); err != nil || pin != tenant.String() {
					t.Fatalf("real tenant write entry: pin=%q err=%v", pin, err)
				}
			}
			before := systemReadWAL(t, dsn)
			if err := ss.System(ctx, func(sys store.SystemScope) error {
				_, err := sys.GetOrg(ctx, tenant)
				return err
			}); err != nil {
				t.Fatalf("read from noncanonical entry: %v", err)
			}
			systemReadWantCanonical(t, ss)
			if bytes.Equal(before, systemReadWAL(t, dsn)) {
				t.Fatal("noncanonical entry took the read rollback instead of restoring SYSTEM")
			}
		})
	}
}

func systemReadWantCanonical(t *testing.T, ss *sqlStore) {
	t.Helper()
	rows, err := ss.db.QueryContext(context.Background(), "SELECT tenant_id, typeof(tenant_id) FROM main."+dialect.ScopeTenantTable)
	if err != nil {
		t.Fatalf("read restored scope: %v", err)
	}
	defer rows.Close() //nolint:errcheck
	count := 0
	for rows.Next() {
		count++
		var pin, storageClass string
		if err := rows.Scan(&pin, &storageClass); err != nil || pin != model.SystemTenantID.String() || storageClass != "text" {
			t.Fatalf("restored scope: pin=%q storage=%q err=%v", pin, storageClass, err)
		}
	}
	if err := rows.Err(); err != nil || count != 1 {
		t.Fatalf("restored scope singleton: rows=%d err=%v", count, err)
	}
	// Drain rows before borrowing the store's single connection again.
	if err := rows.Close(); err != nil {
		t.Fatalf("close scope rows: %v", err)
	}
	directoryEpochTestWantSQLiteBaseline(t, ss.db)
}

func TestSystemReadOptimizationPreservesErrorsSQLite(t *testing.T) {
	for _, name := range []string{"callback", "canceled", "rollback_failure", "already_done", "marker_contamination"} {
		t.Run(name, func(t *testing.T) {
			ss, dsn, tenant := systemReadFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "marker_contamination" {
				if _, err := ss.db.ExecContext(ctx, "INSERT INTO main.directory_writer_marker(control_key,generation,coverage_protocol) VALUES (?,1,'membership-union-v1')", dialect.DirectoryWriterControlKey); err != nil {
					t.Fatalf("contaminate marker: %v", err)
				}
			}
			before := systemReadWAL(t, dsn)
			called := false
			boom := errors.New("read callback failed")
			err := ss.System(ctx, func(sys store.SystemScope) error {
				called = true
				if _, err := sys.GetOrg(ctx, tenant); err != nil {
					return err
				}
				switch name {
				case "callback":
					return boom
				case "canceled":
					cancel()
				case "rollback_failure":
					// End the real SQLite transaction without notifying database/sql.
					// Its next driver rollback must fail and System must surface it.
					_, err := sys.(*systemScope).tx.ExecContext(ctx, "ROLLBACK")
					return err
				case "already_done":
					return sys.(*systemScope).tx.Rollback()
				}
				return nil
			})
			switch name {
			case "callback":
				if !errors.Is(err, boom) {
					t.Fatalf("callback error lost: %v", err)
				}
			case "canceled":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case "rollback_failure":
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "rollback") {
					t.Fatalf("rollback failure lost: %v", err)
				}
			case "already_done":
				if !errors.Is(err, sql.ErrTxDone) {
					t.Fatalf("uncanceled closed transaction error lost: %v", err)
				}
			case "marker_contamination":
				if called || !errors.Is(err, errDirectoryWriterControlInvalid) {
					t.Fatalf("contaminated baseline: callback=%t err=%v", called, err)
				}
				var count int
				if err := ss.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM main.directory_writer_marker").Scan(&count); err != nil || count != 1 {
					t.Fatalf("contamination was healed: rows=%d err=%v", count, err)
				}
				if !bytes.Equal(before, systemReadWAL(t, dsn)) {
					t.Fatal("refusing contamination wrote the WAL")
				}
				return
			}
			systemReadWantCanonical(t, ss)
		})
	}
}

// The read shortcut depends on enrollment being complete. A new public entry
// requires a census decision; every current mutation must enroll before effects.
func TestSystemMutationEnrollmentCensus(t *testing.T) {
	mutators := map[string]bool{
		"CreateOrg": true, "EnsureDefaultWorkspaces": true, "EnsureSystemTenant": true,
		"SetOrgRegion": true, "SetOrgStatus": true, "DropTenant": true,
		"retireUser": true, "retireDirectoryPrincipal": true,
	}
	reads := map[string]bool{"GetOrg": true, "ListOrgs": true, "ListOrgsVisible": true, "Verify": true}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			ptr, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			typ, ok := ptr.X.(*ast.Ident)
			if !ok || typ.Name != "systemScope" {
				continue
			}
			name := fn.Name.Name
			if ast.IsExported(name) && !mutators[name] && !reads[name] {
				t.Errorf("System entry %s has no mutation/read census decision", name)
			}
			if !mutators[name] && !reads[name] {
				continue
			}
			seen[name] = true
			if !mutators[name] {
				continue
			}
			if fn.Body == nil || len(fn.Body.List) == 0 {
				t.Errorf("System mutation %s has no enrollment body", name)
				continue
			}
			first, ok := fn.Body.List[0].(*ast.IfStmt)
			if !ok {
				t.Errorf("System mutation %s does not enroll first", name)
				continue
			}
			assign, ok := first.Init.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 {
				t.Errorf("System mutation %s does not check enrollment", name)
				continue
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok {
				t.Errorf("System mutation %s does not call enrollment", name)
				continue
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || method.Sel.Name != "beginLineageMutation" {
				t.Errorf("System mutation %s does not begin lineage enrollment", name)
				continue
			}
			receiver, ok := method.X.(*ast.Ident)
			if !ok || len(fn.Recv.List[0].Names) != 1 || receiver.Name != fn.Recv.List[0].Names[0].Name {
				t.Errorf("System mutation %s enrolls a different receiver", name)
			}
		}
	}
	for name := range mutators {
		if !seen[name] {
			t.Errorf("System mutation %s disappeared without a census decision", name)
		}
	}
	for name := range reads {
		if !seen[name] {
			t.Errorf("System read %s disappeared without a census decision", name)
		}
	}
}
