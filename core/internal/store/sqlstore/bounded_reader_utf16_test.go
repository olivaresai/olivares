// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// openUTF16SQLiteTest creates a real UTF-16LE database file before the store
// opens it. The encoding is fixed by the first page written, so a table is
// created and dropped to persist it.
func openUTF16SQLiteTest(t *testing.T) store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "br1-utf16.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	for _, statement := range []string{
		"PRAGMA encoding = 'UTF-16le'",
		"CREATE TABLE br1_encoding_seed (x)",
		"DROP TABLE br1_encoding_seed",
	} {
		if _, err := raw.Exec(statement); err != nil {
			_ = raw.Close()
			t.Fatalf("prepare UTF-16 database: %v", err)
		}
	}
	var encoding string
	if err := raw.QueryRow("PRAGMA encoding").Scan(&encoding); err != nil || encoding != "UTF-16le" {
		_ = raw.Close()
		t.Fatalf("raw database encoding %q: %v", encoding, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: path, Debug: true}, nil)
	if err != nil {
		t.Fatalf("open UTF-16 store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestBoundedReaderUTF16DatabaseUsesRatifiedMultiplier exercises a real
// UTF-16LE database: results equal R0, and the conservative 2x TEXT bound is
// applied to stored octets, so a refusal reports an insufficient admission
// bound even though the returned UTF-8 value would have fit.
func TestBoundedReaderUTF16DatabaseUsesRatifiedMultiplier(t *testing.T) {
	ctx := context.Background()
	st := openUTF16SQLiteTest(t)
	tenant := provisionTenant(t, st, "bounded-utf16")
	var mixed, ascii model.Policy
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		if mixed, err = plantRawPolicy(ctx, sc, "mixed", "{\"k\":\"é€\x00\"}"); err != nil {
			return err
		}
		ascii, err = plantRawPolicy(ctx, sc, "ascii", strings.Repeat("a", 100))
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		reader := newBoundedTestReader(t, sc, boundedTestLimits())
		for _, id := range []model.ID{mixed.ID, ascii.ID} {
			want, err := policySnapshots(t, sc).GetPolicySnapshot(ctx, id)
			if err != nil {
				return err
			}
			got, err := reader.GetPolicySnapshot(ctx, id)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("UTF-16 bounded snapshot differs from R0")
			}
		}
		list, _, err := reader.ListPolicySnapshots(ctx, model.Query{Limit: 10})
		if err != nil || len(list) != 2 {
			t.Fatalf("UTF-16 list: rows=%d err=%v", len(list), err)
		}
		if enc := reader.(*boundedReader).encoding; enc != sqliteEncodingUTF16LE {
			t.Fatalf("observed encoding %d, want UTF-16LE", enc)
		}

		// 100 ASCII characters are 200 stored octets, admitted as 400 bytes.
		limits := boundedTestLimits()
		limits.MaxCellBytes = 300
		tight := newBoundedTestReader(t, sc, limits)
		_, err = tight.GetPolicySnapshot(ctx, ascii.ID)
		if !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), "admission bound 400") {
			t.Fatalf("UTF-16 conservative bound err = %v", err)
		}
		if u := tight.Usage(); u.PayloadRowsReserved != 0 || !u.Terminal {
			t.Errorf("UTF-16 refusal usage: %+v", u)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}
