// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type rawPolicyFixture struct {
	policy model.Policy
	spec   any
}

func policySnapshots(t *testing.T, sc store.Scope) store.PolicySnapshotRepository {
	t.Helper()
	repo, ok := sc.Policies().(store.PolicySnapshotRepository)
	if !ok {
		t.Fatal("policy repository lacks PolicySnapshotRepository")
	}
	return repo
}

func plantRawPolicy(
	ctx context.Context,
	sc store.Scope,
	name string,
	spec any,
) (model.Policy, error) {
	policy, err := sc.Policies().Create(ctx, model.Policy{
		Name: name, Kind: "recovery", Spec: map[string]any{"seed": true}, Enabled: true,
	})
	if err != nil {
		return model.Policy{}, err
	}
	result, err := sc.(*tenantScope).tx.ExecContext(
		ctx,
		"UPDATE policies SET spec = ? WHERE id = ? AND tenant_id = ?",
		spec, policy.ID.String(), sc.Tenant().String(),
	)
	if err != nil {
		return model.Policy{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return model.Policy{}, err
	}
	if rows != 1 {
		return model.Policy{}, errors.New("raw policy fixture did not update exactly one row")
	}
	return policy, nil
}

func TestPolicySnapshotSQLiteExactSpecTenantAndPaging(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "policy-snapshot")
	foreignTenant := provisionTenant(t, st, "policy-snapshot-foreign")

	want := []rawPolicyFixture{
		{spec: nil},
		{spec: ""},
		{spec: "null"},
		{spec: "{\"broken\":"},
		{spec: "  {\"z\":1,\"a\":2,\"a\":3} \n"},
		{spec: "{\"n\":1234567890123456789012345678901234567890}"},
		{spec: "[\n  1.2300e+004, 0.000, -0\n]\t"},
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i := range want {
			policy, err := plantRawPolicy(ctx, sc, "policy-"+string(rune('a'+i)), want[i].spec)
			if err != nil {
				return err
			}
			want[i].policy = policy
		}
		return nil
	}); err != nil {
		t.Fatalf("plant exact policy specs: %v", err)
	}

	var foreign model.Policy
	if err := st.Mutate(ctx, foreignTenant, func(sc store.Scope) error {
		var err error
		foreign, err = plantRawPolicy(ctx, sc, "foreign-policy", "{\"foreign\":true}")
		return err
	}); err != nil {
		t.Fatalf("plant foreign policy: %v", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo := policySnapshots(t, sc)
		byID := make(map[model.ID]rawPolicyFixture, len(want))
		for _, fixture := range want {
			byID[fixture.policy.ID] = fixture
			snapshot, err := repo.GetPolicySnapshot(ctx, fixture.policy.ID)
			if err != nil {
				return err
			}
			if snapshot.ID != fixture.policy.ID || snapshot.TenantID != tenant ||
				snapshot.CreatedAt != fixture.policy.CreatedAt ||
				snapshot.UpdatedAt != fixture.policy.UpdatedAt ||
				snapshot.Version != fixture.policy.Version || snapshot.Name != fixture.policy.Name ||
				snapshot.Kind != fixture.policy.Kind || snapshot.Enabled != fixture.policy.Enabled {
				t.Fatalf("snapshot metadata = %+v, want planted policy %+v", snapshot, fixture.policy)
			}
			assertExactSpec(t, snapshot.Spec, fixture.spec)
		}
		if _, err := repo.GetPolicySnapshot(ctx, foreign.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("foreign policy Get err = %v, want ErrNotFound", err)
		}

		var listed []store.PolicySnapshot
		q := model.Query{Limit: 2}
		for {
			pageRows, page, err := repo.ListPolicySnapshots(ctx, q)
			if err != nil {
				return err
			}
			if len(pageRows) == 0 {
				t.Fatal("paged policy snapshot returned an empty continuation")
			}
			listed = append(listed, pageRows...)
			if !page.HasMore {
				if page.Cursor != "" {
					t.Fatalf("final page cursor = %q, want empty", page.Cursor)
				}
				break
			}
			if page.Cursor == "" {
				t.Fatal("non-final policy page lacks a cursor")
			}
			q.Cursor = page.Cursor
		}
		if len(listed) != len(want) {
			t.Fatalf("listed %d tenant policies, want %d", len(listed), len(want))
		}
		if !sort.SliceIsSorted(listed, func(i, j int) bool { return listed[i].ID < listed[j].ID }) {
			t.Fatal("default snapshot paging did not preserve id order")
		}
		for _, snapshot := range listed {
			fixture, ok := byID[snapshot.ID]
			if !ok {
				t.Fatalf("snapshot list leaked unknown policy %s", snapshot.ID)
			}
			assertExactSpec(t, snapshot.Spec, fixture.spec)
		}

		sortedRows, page, err := repo.ListPolicySnapshots(ctx, model.Query{
			Filters: []model.Filter{{Column: "kind", Op: model.OpEq, Value: "recovery"}},
			Sort:    []model.Sort{{Column: "name", Desc: true}},
			Limit:   3,
		})
		if err != nil {
			return err
		}
		if len(sortedRows) != 3 || !page.HasMore || page.Cursor != "" ||
			sortedRows[0].Name != "policy-g" || sortedRows[2].Name != "policy-e" {
			t.Fatalf("filtered/sorted snapshot page = %+v, page=%+v", sortedRows, page)
		}
		return nil
	}); err != nil {
		t.Fatalf("read exact policy snapshots: %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := policySnapshots(t, sc).LockPolicySnapshot(ctx, foreign.ID)
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("foreign policy Lock err = %v, want ErrNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect tenant-pinned snapshot lock: %v", err)
	}
}

func assertExactSpec(t *testing.T, got *string, want any) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("Spec = %q, want SQL NULL", *got)
		}
		return
	}
	text := want.(string)
	if got == nil || *got != text {
		if got == nil {
			t.Fatalf("Spec = nil, want %q", text)
		}
		t.Fatalf("Spec = %q, want exact %q", *got, text)
	}
}

func TestPolicySnapshotSQLiteOwnershipErrorsAndCapabilities(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "policy-snapshot-capabilities")
	const stored = " {\"same\":1,\"same\":2} "
	var policy model.Policy
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		policy, err = plantRawPolicy(ctx, sc, "opaque-policy", stored)
		return err
	}); err != nil {
		t.Fatalf("plant opaque policy: %v", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo := policySnapshots(t, sc)
		first, err := repo.GetPolicySnapshot(ctx, policy.ID)
		if err != nil {
			return err
		}
		second, err := repo.GetPolicySnapshot(ctx, policy.ID)
		if err != nil {
			return err
		}
		*first.Spec = "changed by caller"
		if second.Spec == nil || *second.Spec != stored {
			t.Fatalf("snapshot Spec aliases another call: %v", second.Spec)
		}

		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := repo.GetPolicySnapshot(canceled, policy.ID); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled snapshot Get err = %v, want context.Canceled", err)
		}
		if _, err := repo.LockPolicySnapshot(ctx, policy.ID); !errors.Is(err, store.ErrReadOnly) {
			t.Fatalf("View snapshot Lock err = %v, want ErrReadOnly", err)
		}
		if _, ok := sc.Agents().(store.PolicySnapshotRepository); ok {
			t.Fatal("agent repository exposes PolicySnapshotRepository")
		}
		workspace, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		confined, err := store.ConfineWorkspace(ctx, sc, workspace.ID)
		if err != nil {
			return err
		}
		if _, ok := confined.Policies().(store.PolicySnapshotRepository); ok {
			t.Fatal("workspace-confined policies expose PolicySnapshotRepository")
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect snapshot ownership and capabilities: %v", err)
	}

	sentinel := errors.New("rollback after snapshot lock")
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo := policySnapshots(t, sc)
		locked, err := repo.LockPolicySnapshot(ctx, policy.ID)
		if err != nil {
			return err
		}
		assertExactSpec(t, locked.Spec, stored)
		if locked.ID != policy.ID || locked.Version != policy.Version {
			t.Fatalf("locked snapshot = %+v, want policy %s version %d", locked, policy.ID, policy.Version)
		}
		locker, ok := sc.Policies().(store.RowLocker[model.Policy])
		if !ok {
			t.Fatal("policy repository lost RowLocker")
		}
		typedLocked, err := locker.Lock(ctx, policy.ID)
		if err != nil {
			return err
		}
		if typedLocked.ID != policy.ID {
			t.Fatalf("typed policy Lock returned %s, want %s", typedLocked.ID, policy.ID)
		}
		valid, err := sc.Policies().Create(ctx, model.Policy{
			Name: "rollback-probe", Kind: "valid", Spec: map[string]any{"ok": true}, Enabled: true,
		})
		if err != nil {
			return err
		}
		if _, err := sc.Policies().Get(ctx, valid.ID); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("snapshot lock transaction err = %v, want rollback sentinel", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		snapshot, err := policySnapshots(t, sc).GetPolicySnapshot(ctx, policy.ID)
		if err != nil {
			return err
		}
		assertExactSpec(t, snapshot.Spec, stored)
		rows, _, err := sc.Policies().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "name", Op: model.OpEq, Value: "rollback-probe"}},
		})
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			t.Fatal("snapshot Lock committed its surrounding transaction")
		}
		return nil
	}); err != nil {
		t.Fatalf("verify snapshot lock left policy unchanged: %v", err)
	}
}

func TestPolicySnapshotRejectsCorruptSQLMetadata(t *testing.T) {
	const brokenSpec = " {\"broken\": "
	tests := []struct {
		name      string
		wantError string
	}{
		{name: "zero version", wantError: "policy snapshot: invalid version"},
		{name: "negative version", wantError: "policy snapshot: invalid version"},
		{name: "zero id", wantError: "policy snapshot: invalid id"},
		{name: "malformed id", wantError: "policy snapshot: invalid id"},
		{name: "noncanonical id", wantError: "policy snapshot: invalid id"},
		{name: "unreadable timestamp", wantError: "policy snapshot: invalid created_at"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			st := openSQLiteTest(t, nil)
			tenant := provisionTenant(t, st, "policy-metadata-"+strings.ReplaceAll(test.name, " ", "-"))
			var lookupID model.ID
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				policy, err := plantRawPolicy(ctx, sc, "corrupt-metadata", brokenSpec)
				if err != nil {
					return err
				}
				control, err := policySnapshots(t, sc).GetPolicySnapshot(ctx, policy.ID)
				if err != nil {
					return err
				}
				assertExactSpec(t, control.Spec, brokenSpec)

				lookupID = policy.ID
				tx := sc.(*tenantScope).tx
				var result interface {
					RowsAffected() (int64, error)
				}
				switch test.name {
				case "zero version":
					result, err = tx.ExecContext(ctx,
						"UPDATE policies SET version = 0 WHERE id = ? AND tenant_id = ?",
						policy.ID.String(), tenant.String())
				case "negative version":
					result, err = tx.ExecContext(ctx,
						"UPDATE policies SET version = -1 WHERE id = ? AND tenant_id = ?",
						policy.ID.String(), tenant.String())
				case "zero id":
					lookupID = model.ID("00000000-0000-0000-0000-000000000000")
					result, err = tx.ExecContext(ctx,
						"UPDATE policies SET id = ? WHERE id = ? AND tenant_id = ?",
						lookupID.String(), policy.ID.String(), tenant.String())
				case "malformed id":
					lookupID = model.ID("not-a-policy-uuid")
					result, err = tx.ExecContext(ctx,
						"UPDATE policies SET id = ? WHERE id = ? AND tenant_id = ?",
						lookupID.String(), policy.ID.String(), tenant.String())
				case "noncanonical id":
					lookupID = model.ID(strings.ReplaceAll(policy.ID.String(), "-", ""))
					parsed, parseErr := model.ParseID(lookupID.String())
					if parseErr != nil || parsed != policy.ID {
						t.Fatalf("noncanonical fixture does not normalize to original id")
					}
					result, err = tx.ExecContext(ctx,
						"UPDATE policies SET id = ? WHERE id = ? AND tenant_id = ?",
						lookupID.String(), policy.ID.String(), tenant.String())
				case "unreadable timestamp":
					result, err = tx.ExecContext(ctx,
						"UPDATE policies SET created_at = ? WHERE id = ? AND tenant_id = ?",
						"not-a-timestamp", policy.ID.String(), tenant.String())
				}
				if err != nil {
					return err
				}
				rows, err := result.RowsAffected()
				if err != nil {
					return err
				}
				if rows != 1 {
					t.Fatalf("metadata corruption affected %d rows, want 1", rows)
				}
				return nil
			}); err != nil {
				t.Fatalf("plant corrupt policy metadata: %v", err)
			}

			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				repo := policySnapshots(t, sc)
				_, err := repo.GetPolicySnapshot(ctx, lookupID)
				assertPolicySnapshotError(t, err, test.wantError, brokenSpec)
				_, _, err = repo.ListPolicySnapshots(ctx, model.Query{})
				assertPolicySnapshotError(t, err, test.wantError, brokenSpec)
				return nil
			}); err != nil {
				t.Fatalf("read corrupt policy metadata: %v", err)
			}
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				_, err := policySnapshots(t, sc).LockPolicySnapshot(ctx, lookupID)
				assertPolicySnapshotError(t, err, test.wantError, brokenSpec)
				return nil
			}); err != nil {
				t.Fatalf("lock corrupt policy metadata: %v", err)
			}
		})
	}
}

func TestPolicySnapshotValidatesObservedTenant(t *testing.T) {
	const brokenSpec = "{"
	canonicalTenant := model.NewTenantID()
	tests := []struct {
		name      string
		observed  string
		wantError string
	}{
		{
			name:      "zero tenant",
			observed:  "00000000-0000-0000-0000-000000000000",
			wantError: "policy snapshot: invalid tenant_id",
		},
		{
			name:      "noncanonical tenant",
			observed:  strings.ReplaceAll(canonicalTenant.String(), "-", ""),
			wantError: "policy snapshot: invalid tenant_id",
		},
		{
			name:     "system tenant",
			observed: model.SystemTenantID.String(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := model.Record{
				model.ColID:        model.NewID().String(),
				model.ColTenantID:  test.observed,
				model.ColCreatedAt: "2026-09-09T00:00:00.000000000Z",
				model.ColUpdatedAt: "2026-09-09T00:00:00.000000000Z",
				model.ColVersion:   int64(1),
				"name":             "observed tenant",
				"kind":             "recovery",
				"enabled":          true,
				"spec":             brokenSpec,
			}
			snapshot, err := policySnapshotFromRecord(rec)
			if test.wantError != "" {
				assertPolicySnapshotError(t, err, test.wantError, brokenSpec)
				return
			}
			if err != nil {
				t.Fatalf("validate SYSTEM tenant: %v", err)
			}
			if snapshot.TenantID != model.SystemTenantID {
				t.Fatalf("system tenant snapshot = %s, want SYSTEM", snapshot.TenantID)
			}
			assertExactSpec(t, snapshot.Spec, brokenSpec)
		})
	}
}

func assertPolicySnapshotError(t *testing.T, err error, want, forbidden string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("snapshot error = %v, want %q", err, want)
	}
	if strings.Contains(err.Error(), forbidden) {
		t.Fatalf("snapshot error echoed opaque Spec")
	}
}
