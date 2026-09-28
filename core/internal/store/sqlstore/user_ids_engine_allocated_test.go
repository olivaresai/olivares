// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestUserIDsAreEngineAllocatedAndUsersAreNeverDeleted is a tripwire for the
// assumption that lets a writer store an id that names no account: account ids
// are allocated by the engine and accounts are never deleted, so an id that names
// no account now never names one later. It exits 1 only when account ids become
// caller-chosen or accounts deletable.
func TestUserIDsAreEngineAllocatedAndUsersAreNeverDeleted(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			cfg := store.Config{Engine: engine, DSN: ":memory:", Debug: true}
			if engine == store.EnginePostgres {
				dsns := pgtest.Isolate(t, ProvisionPostgres, pgtest.SplitOwner)
				cfg = store.Config{Engine: engine, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin, Debug: true, MaxConns: 8}
			}
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })

			chosen := model.NewID()
			var created model.User
			if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
				var err error
				created, err = as.Users().Create(ctx, model.User{
					BaseFields: model.BaseFields{ID: chosen}, Email: "chosen@ids.example", Status: model.StatusActive,
				})
				return err
			}); err != nil {
				t.Fatalf("create: %v", err)
			}
			if created.ID == chosen || created.ID.IsZero() {
				t.Errorf("Users().Create stored the caller's id %s", chosen)
			}
			var users store.MutableRepository[model.User]
			_ = st.AuthView(ctx, func(as store.AuthScope) error {
				users = as.Users()
				return nil
			})
			for _, method := range []string{"Delete", "CreateWithID"} {
				if _, ok := reflect.TypeOf(users).MethodByName(method); ok {
					t.Errorf("the users repository exposes %s", method)
				}
			}
		})
	}

	t.Run("no production path writes a user row under a chosen id", func(t *testing.T) {
		_, self, _, _ := runtime.Caller(0)
		dir := filepath.Dir(self)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				usesUsers := false
				withID := false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.Ident:
						if x.Name == "userDescriptor" {
							usesUsers = true
						}
					case *ast.SelectorExpr:
						if x.Sel.Name == "CreateWithID" || x.Sel.Name == "CreateWithIDAtTransactionTime" {
							withID = true
						}
					}
					return true
				})
				// The one engine-owned allocation: Users().Create picks the id itself
				// and writes the account's authority row under the same id.
				if usesUsers && withID && !(name == "userauthority_writer.go" && fn.Name.Name == "Create") {
					t.Errorf("%s: %s writes a user row with a chosen id", name, fn.Name.Name)
				}
			}
		}
	})
}
