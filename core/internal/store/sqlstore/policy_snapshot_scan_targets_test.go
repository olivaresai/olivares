// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestPolicySnapshotScanTargetsWrapOnlyScalarMetadata(t *testing.T) {
	cols := policyDescriptor.AllColumns()
	st, err := newScanState(policyDescriptor, cols)
	if err != nil {
		t.Fatal(err)
	}
	original := make([]any, len(st.dests))
	copy(original, st.dests)
	targets := policySnapshotScanTargets(st)
	if len(targets) != len(st.dests) || &targets[0] == &st.dests[0] {
		t.Fatal("scan targets share the scanState destination slice")
	}
	wrapped := 0
	for i, col := range cols {
		if st.dests[i] != original[i] {
			t.Fatalf("scanState destination %s was replaced", col)
		}
		switch col {
		case model.ColVersion:
			scanner, ok := targets[i].(*policySnapshotVersionScanner)
			if !ok || scanner.dst != st.dests[i] {
				t.Fatalf("version target = %T, want scanner over the original *int64", targets[i])
			}
			wrapped++
		case "enabled":
			scanner, ok := targets[i].(*policySnapshotEnabledScanner)
			if !ok || scanner.dst != st.dests[i] {
				t.Fatalf("enabled target = %T, want scanner over the original *bool", targets[i])
			}
			wrapped++
		default:
			if targets[i] != st.dests[i] {
				t.Fatalf("column %s target = %T, want the scanState destination", col, targets[i])
			}
		}
	}
	if wrapped != 2 {
		t.Fatalf("wrapped %d policy scalar targets, want 2", wrapped)
	}
}

func TestPolicySnapshotScalarScannersConvertOrReportConstantErrors(t *testing.T) {
	for _, src := range []any{int64(9), "9", []byte("9"), float64(9)} {
		var dst int64
		if err := (&policySnapshotVersionScanner{dst: &dst}).Scan(src); err != nil || dst != 9 {
			t.Fatalf("version Scan(%T) = %d, %v; want 9", src, dst, err)
		}
	}
	for _, src := range []any{nil, "seven", []byte("1.5"), 2.5, true, "99999999999999999999"} {
		dst := int64(-3)
		if err := (&policySnapshotVersionScanner{dst: &dst}).Scan(src); err != errPolicySnapshotVersion || dst != -3 {
			t.Fatalf("version Scan(%T) = %d, %v; want untouched and constant error", src, dst, err)
		}
	}
	for _, test := range []struct {
		src  any
		want bool
	}{{true, true}, {false, false}, {int64(1), true}, {int64(0), false}, {"true", true}, {[]byte("f"), false}} {
		dst := !test.want
		if err := (&policySnapshotEnabledScanner{dst: &dst}).Scan(test.src); err != nil || dst != test.want {
			t.Fatalf("enabled Scan(%T) = %v, %v; want %v", test.src, dst, err, test.want)
		}
	}
	for _, src := range []any{nil, "maybe", int64(7), 0.25, []byte("yes")} {
		dst := true
		if err := (&policySnapshotEnabledScanner{dst: &dst}).Scan(src); err != errPolicySnapshotEnabled || !dst {
			t.Fatalf("enabled Scan(%T) = %v, %v; want untouched and constant error", src, dst, err)
		}
	}
}

func TestPolicySnapshotScanErrorReplacesOnlyScannerFailures(t *testing.T) {
	for _, sentinel := range []error{errPolicySnapshotVersion, errPolicySnapshotEnabled} {
		wrapped := fmt.Errorf("sql: Scan error on column index 1, name %q: %w", "x", sentinel)
		if got := policySnapshotScanError(wrapped); got != sentinel {
			t.Fatalf("scan error = %v, want exactly %v", got, sentinel)
		}
	}
	for _, passthrough := range []error{
		context.Canceled, store.ErrNotFound, store.ErrReadOnly, sql.ErrConnDone,
		fmt.Errorf("sql: Scan error on column index 3: %w", errors.New("policy snapshot: invalid enabled")),
	} {
		if got := policySnapshotScanError(passthrough); got != passthrough {
			t.Fatalf("scan error %v was replaced by %v", passthrough, got)
		}
	}
}

func TestPolicySnapshotReaderLeavesSharedRepositoryUntouched(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "policy-snapshot-reader")
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, ok := sc.Policies().(*policyRepo)
		if !ok {
			t.Fatalf("policy repository = %T, want *policyRepo", sc.Policies())
		}
		reader := repo.snapshotReader()
		if reader == repo.g || reader.scanTargets == nil || repo.g.scanTargets != nil {
			t.Fatal("snapshot reader is not a separately adapted value copy")
		}
		if reader.tx != repo.g.tx || reader.tenant != repo.g.tenant || !reader.readOnly ||
			reader.dia != repo.g.dia || reader.desc.Table != repo.g.desc.Table {
			t.Fatal("snapshot reader lost the tenant-pinned repository state")
		}
		if _, _, err := repo.ListPolicySnapshots(ctx, model.Query{}); err != nil {
			return err
		}
		if repo.g.scanTargets != nil {
			t.Fatal("snapshot read installed scan targets on the shared repository")
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect snapshot reader: %v", err)
	}
}
