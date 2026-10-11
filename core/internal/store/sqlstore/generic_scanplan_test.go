// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The bench's fixture policy kind; the policy writer refuses an unregistered one.
func init() {
	model.MustRegisterPolicyKind("widebench-policies", model.None("a fixture policy kind for the A5 scan-plan benchmark: generic_scanplan_test.go"))
}

// CUTS A5 (2026-10-02): genericRepo.List rebuilt the scan plan for every row
// (AU2-09: 12.29 us per 64-field row; ~79 ns with the plan built once). The
// plan is now built once per query and the destinations are reused — safe
// because record() materializes each row before the next Scan overwrites the
// pointers. These benchmarks measure the eliminated per-row build and the List
// after the change.

func wideDescriptor(n int) model.EntityDescriptor {
	fields := make([]model.FieldSpec, n)
	for i := 0; i < n; i++ {
		fields[i] = model.FieldSpec{Name: fmt.Sprintf("f%02d", i), Kind: model.KindText}
	}
	return model.EntityDescriptor{Kind: "widebench", Table: "widebench", Fields: fields}
}

// The eliminated per-row cost, on the 64-field row AU2-09 measured.
func BenchmarkScanStateBuild64Fields(b *testing.B) {
	desc := wideDescriptor(64)
	cols := desc.AllColumns()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st, err := newScanState(desc, cols)
		if err != nil {
			b.Fatal(err)
		}
		_ = st
	}
}

// The List after the change: 2,000 rows read through the hoisted plan.
func BenchmarkListAfterPlanHoist(b *testing.B) {
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(context.Background()); e != nil {
			return e
		}
		org, e := sys.CreateOrg(context.Background(), model.Org{Name: "a5", Slug: "a5", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i := 0; i < 2000; i++ {
			if _, err := sc.Policies().Create(ctx, model.Policy{
				Name: fmt.Sprintf("p%04d", i), Kind: "widebench-policies", Enabled: true,
				Spec: map[string]any{"amount": i, "period": "monthly"},
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			total := 0
			q := model.Query{}
			for {
				rows, page, err := sc.Policies().List(ctx, q)
				if err != nil {
					return err
				}
				total += len(rows)
				if !page.HasMore {
					break
				}
				q.Cursor = page.Cursor
			}
			if total != 2000 {
				return fmt.Errorf("rows = %d", total)
			}
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
}
