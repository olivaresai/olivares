// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// plantMalformedPolicyScalar stores a policy whose enabled or version column
// holds a value database/sql cannot convert. SQLite policies carry no STRICT
// table or CHECK on either column, so the value keeps its storage class.
func plantMalformedPolicyScalar(
	ctx context.Context,
	sc store.Scope,
	name, spec, column string,
	value any,
) (model.Policy, error) {
	if column != "enabled" && column != model.ColVersion {
		return model.Policy{}, errors.New("malformed scalar fixture names an unsupported column")
	}
	policy, err := plantRawPolicy(ctx, sc, name, spec)
	if err != nil {
		return model.Policy{}, err
	}
	result, err := sc.(*tenantScope).tx.ExecContext(ctx,
		"UPDATE policies SET "+column+" = ? WHERE id = ? AND tenant_id = ?",
		value, policy.ID.String(), sc.Tenant().String())
	if err != nil {
		return model.Policy{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return model.Policy{}, err
	}
	if rows != 1 {
		return model.Policy{}, errors.New("malformed scalar fixture did not update exactly one row")
	}
	return policy, nil
}

func TestPolicySnapshotContainsMalformedStoredScalars(t *testing.T) {
	const brokenSpec = " {\"broken\": "
	tests := []struct {
		name      string
		column    string
		value     any
		raw       string
		wantError string
	}{
		{name: "text enabled", column: "enabled", value: "maybe", raw: "maybe",
			wantError: "policy snapshot: invalid enabled"},
		{name: "out of range enabled", column: "enabled", value: int64(7), raw: "7",
			wantError: "policy snapshot: invalid enabled"},
		{name: "fractional enabled", column: "enabled", value: 0.25, raw: "0.25",
			wantError: "policy snapshot: invalid enabled"},
		{name: "text version", column: model.ColVersion, value: "seven", raw: "seven",
			wantError: "policy snapshot: invalid version"},
		{name: "fractional version", column: model.ColVersion, value: 2.5, raw: "2.5",
			wantError: "policy snapshot: invalid version"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			slug := strings.ReplaceAll(test.name, " ", "-")
			st := openSQLiteTest(t, nil)
			tenant := provisionTenant(t, st, "policy-scalar-"+slug)
			foreignTenant := provisionTenant(t, st, "policy-scalar-foreign-"+slug)

			var foreign model.Policy
			if err := st.Mutate(ctx, foreignTenant, func(sc store.Scope) error {
				var err error
				foreign, err = plantMalformedPolicyScalar(ctx, sc, "foreign-corrupt", brokenSpec,
					test.column, test.value)
				return err
			}); err != nil {
				t.Fatalf("plant foreign malformed scalar: %v", err)
			}

			var valid, corrupt model.Policy
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				var err error
				valid, err = sc.Policies().Create(ctx, model.Policy{
					Name: "valid-first", Kind: "recovery", Spec: map[string]any{"ok": true}, Enabled: false,
				})
				if err != nil {
					return err
				}
				if _, err := sc.(*tenantScope).tx.ExecContext(ctx,
					"UPDATE policies SET version = 7 WHERE id = ? AND tenant_id = ?",
					valid.ID.String(), tenant.String()); err != nil {
					return err
				}
				corrupt, err = plantMalformedPolicyScalar(ctx, sc, "corrupt-second", brokenSpec,
					test.column, test.value)
				return err
			}); err != nil {
				t.Fatalf("plant malformed scalar: %v", err)
			}
			if valid.ID >= corrupt.ID {
				t.Fatalf("valid fixture %s does not precede corrupt fixture %s", valid.ID, corrupt.ID)
			}

			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				repo := policySnapshots(t, sc)
				_, err := repo.GetPolicySnapshot(ctx, corrupt.ID)
				assertPolicyScalarError(t, err, test.wantError, brokenSpec, test.raw)

				listed, page, err := repo.ListPolicySnapshots(ctx, model.Query{})
				assertPolicyScalarError(t, err, test.wantError, brokenSpec, test.raw)
				if listed != nil || page != (model.Page{}) {
					t.Fatalf("failed snapshot List escaped rows=%+v page=%+v", listed, page)
				}

				got, err := repo.GetPolicySnapshot(ctx, valid.ID)
				if err != nil {
					return err
				}
				assertValidScalarSnapshot(t, got, valid.ID)
				// List reads limit+1 rows to decide HasMore, so a one-row page whose
				// look-ahead row is malformed fails whole as well.
				first, page, err := repo.ListPolicySnapshots(ctx, model.Query{Limit: 1})
				assertPolicyScalarError(t, err, test.wantError, brokenSpec, test.raw)
				if first != nil || page != (model.Page{}) {
					t.Fatalf("failed one-row page escaped rows=%+v page=%+v", first, page)
				}
				filtered, page, err := repo.ListPolicySnapshots(ctx, model.Query{
					Filters: []model.Filter{{Column: "name", Op: model.OpEq, Value: "valid-first"}},
				})
				if err != nil {
					return err
				}
				if len(filtered) != 1 || page.HasMore {
					t.Fatalf("valid filtered page = %+v, page=%+v", filtered, page)
				}
				assertValidScalarSnapshot(t, filtered[0], valid.ID)

				if _, err := repo.GetPolicySnapshot(ctx, foreign.ID); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("foreign malformed policy Get err = %v, want ErrNotFound", err)
				}
				if _, err := repo.LockPolicySnapshot(ctx, corrupt.ID); !errors.Is(err, store.ErrReadOnly) {
					t.Fatalf("View malformed snapshot Lock err = %v, want ErrReadOnly", err)
				}
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				if _, err := repo.GetPolicySnapshot(canceled, corrupt.ID); !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled malformed snapshot Get err = %v, want context.Canceled", err)
				}
				if _, _, err := repo.ListPolicySnapshots(canceled, model.Query{}); !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled malformed snapshot List err = %v, want context.Canceled", err)
				}

				_, err = sc.Policies().Get(ctx, corrupt.ID)
				if err == nil || err.Error() == test.wantError || !strings.Contains(err.Error(), "Scan error") {
					t.Fatalf("ordinary policy Get err = %v, want the generic database/sql scan error", err)
				}
				return nil
			}); err != nil {
				t.Fatalf("read malformed scalar snapshots: %v", err)
			}

			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo := policySnapshots(t, sc)
				locked, err := repo.LockPolicySnapshot(ctx, valid.ID)
				if err != nil {
					return err
				}
				assertValidScalarSnapshot(t, locked, valid.ID)
				_, err = repo.LockPolicySnapshot(ctx, corrupt.ID)
				assertPolicyScalarError(t, err, test.wantError, brokenSpec, test.raw)
				if _, err := repo.LockPolicySnapshot(ctx, foreign.ID); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("foreign malformed policy Lock err = %v, want ErrNotFound", err)
				}
				return nil
			}); err != nil {
				t.Fatalf("lock malformed scalar snapshots: %v", err)
			}
		})
	}
}

func assertValidScalarSnapshot(t *testing.T, got store.PolicySnapshot, id model.ID) {
	t.Helper()
	if got.ID != id || got.Enabled || got.Version != 7 {
		t.Fatalf("valid snapshot = %+v, want %s disabled at version 7", got, id)
	}
	assertExactSpec(t, got.Spec, `{"ok":true}`)
}

func assertPolicyScalarError(t *testing.T, err error, want, spec, raw string) {
	t.Helper()
	assertPolicySnapshotError(t, err, want, spec)
	if strings.Contains(err.Error(), raw) {
		t.Fatalf("snapshot error echoed the stored scalar")
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("snapshot error wraps a cause")
	}
}

// TestPolicySnapshotPostgresValidScalarsAndLock confirms the snapshot scan
// adapter on the PostgreSQL driver's native bool and bigint values, including the
// FOR UPDATE row lock. PostgreSQL rejects a malformed enabled write by type, so
// the malformed read cases are SQLite-only.
func TestPolicySnapshotPostgresValidScalarsAndLock(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, nil)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tenant := provisionTenant(t, st, "policy-snapshot-pg")
	foreignTenant := provisionTenant(t, st, "policy-snapshot-pg-foreign")

	var foreign model.Policy
	if err := st.Mutate(ctx, foreignTenant, func(sc store.Scope) error {
		var err error
		foreign, err = sc.Policies().Create(ctx, model.Policy{Name: "pg-foreign", Kind: "recovery", Enabled: true})
		return err
	}); err != nil {
		t.Fatalf("create foreign policy: %v", err)
	}
	var disabled, enabled model.Policy
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		disabled, err = sc.Policies().Create(ctx, model.Policy{
			Name: "pg-disabled", Kind: "recovery", Spec: map[string]any{"ok": true}, Enabled: false,
		})
		if err != nil {
			return err
		}
		enabled, err = sc.Policies().Create(ctx, model.Policy{Name: "pg-enabled", Kind: "recovery", Enabled: true})
		if err != nil {
			return err
		}
		_, err = sc.(*tenantScope).tx.ExecContext(ctx,
			"UPDATE policies SET version = 7 WHERE id = $1 AND tenant_id = $2",
			disabled.ID.String(), tenant.String())
		return err
	}); err != nil {
		t.Fatalf("create postgres policies: %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.(*tenantScope).tx.ExecContext(ctx,
			"UPDATE policies SET enabled = $1 WHERE id = $2 AND tenant_id = $3",
			"maybe", enabled.ID.String(), tenant.String())
		return err
	}); err == nil {
		t.Fatal("PostgreSQL accepted a malformed enabled write")
	}

	check := func(t *testing.T, got store.PolicySnapshot) {
		t.Helper()
		switch got.ID {
		case disabled.ID:
			assertValidScalarSnapshot(t, got, disabled.ID)
		case enabled.ID:
			if !got.Enabled || got.Version != 1 {
				t.Fatalf("enabled snapshot = %+v, want enabled at version 1", got)
			}
		default:
			t.Fatalf("snapshot leaked policy %s", got.ID)
		}
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo := policySnapshots(t, sc)
		for _, id := range []model.ID{disabled.ID, enabled.ID} {
			got, err := repo.GetPolicySnapshot(ctx, id)
			if err != nil {
				return err
			}
			check(t, got)
		}
		listed, page, err := repo.ListPolicySnapshots(ctx, model.Query{})
		if err != nil {
			return err
		}
		if len(listed) != 2 || page.HasMore {
			t.Fatalf("postgres snapshot list = %+v, page=%+v", listed, page)
		}
		for _, got := range listed {
			check(t, got)
		}
		if _, err := repo.GetPolicySnapshot(ctx, foreign.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("foreign postgres Get err = %v, want ErrNotFound", err)
		}
		if _, err := repo.LockPolicySnapshot(ctx, disabled.ID); !errors.Is(err, store.ErrReadOnly) {
			t.Fatalf("View postgres Lock err = %v, want ErrReadOnly", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("read postgres snapshots: %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo := policySnapshots(t, sc)
		for _, id := range []model.ID{disabled.ID, enabled.ID} {
			got, err := repo.LockPolicySnapshot(ctx, id)
			if err != nil {
				return err
			}
			check(t, got)
		}
		if _, err := repo.LockPolicySnapshot(ctx, foreign.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("foreign postgres Lock err = %v, want ErrNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("lock postgres snapshots: %v", err)
	}
}
