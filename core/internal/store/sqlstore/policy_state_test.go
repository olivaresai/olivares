// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const policyStateKind = "recovery"

func policyState(t *testing.T, sc store.Scope) store.PolicyStateWriter {
	t.Helper()
	repo, ok := sc.Policies().(store.PolicyStateWriter)
	if !ok {
		t.Fatal("policy repository lacks PolicyStateWriter")
	}
	return repo
}

// readRawSpec reads the spec cell through the scope's own transaction without
// going near the codec, so a comparison is of stored bytes and not of a
// round trip. A NULL cell returns nil.
func readRawSpec(ctx context.Context, sc store.Scope, id model.ID) (*string, error) {
	row := sc.(*tenantScope).tx.QueryRowContext(ctx,
		sc.(*tenantScope).s.dia.Rebind("SELECT spec FROM policies WHERE id = ? AND tenant_id = ?"),
		id.String(), sc.Tenant().String())
	var raw *string
	if err := row.Scan(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func sameRawSpec(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// plantRawPolicyPortable is plantRawPolicy with the driver's own placeholders.
// R0's helper writes literal "?" and is therefore SQLite-only; the same fixture
// has to reach PostgreSQL here, so this one goes through Rebind. R0's helper is
// left exactly as it is.
func plantRawPolicyPortable(
	ctx context.Context,
	sc store.Scope,
	name string,
	spec any,
) (model.Policy, error) {
	policy, err := sc.Policies().Create(ctx, model.Policy{
		Name: name, Kind: policyStateKind, Spec: map[string]any{"seed": true}, Enabled: true,
	})
	if err != nil {
		return model.Policy{}, err
	}
	ts := sc.(*tenantScope)
	result, err := ts.tx.ExecContext(ctx,
		ts.s.dia.Rebind("UPDATE policies SET spec = ? WHERE id = ? AND tenant_id = ?"),
		spec, policy.ID.String(), sc.Tenant().String())
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

// policyStateSpecs are the stored documents whose bytes must survive a state
// toggle: SQL NULL, empty text, the literal null, broken JSON, duplicate keys
// with whitespace, an integer lexeme far beyond float64, and exponent/-0 forms.
func policyStateSpecs() []any {
	return []any{
		nil,
		"",
		"null",
		"{\"broken\":",
		"  {\"z\":1,\"a\":2,\"a\":3} \n",
		"{\"n\":1234567890123456789012345678901234567890}",
		"[\n  1.2300e+004, 0.000, -0\n]\t",
	}
}

// runPolicyStateByteIdentity is the headline property: toggling enabled must not
// change one byte of the stored spec, in either direction, on a real engine.
func runPolicyStateByteIdentity(t *testing.T, st store.Store, tenant model.TenantID) {
	t.Helper()
	ctx := context.Background()
	specs := policyStateSpecs()
	planted := make([]model.Policy, len(specs))
	before := make([]*string, len(specs))

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i, spec := range specs {
			policy, err := plantRawPolicyPortable(ctx, sc, "state-"+string(rune('a'+i)), spec)
			if err != nil {
				return err
			}
			planted[i] = policy
			raw, err := readRawSpec(ctx, sc, policy.ID)
			if err != nil {
				return err
			}
			before[i] = raw
		}
		return nil
	}); err != nil {
		t.Fatalf("plant policy state fixtures: %v", err)
	}

	for round, enabled := range []bool{false, true} {
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			observeNow(t, ctx, sc)
			repo := policyState(t, sc)
			for i := range planted {
				current, err := repo.GetPolicyState(ctx, planted[i].ID)
				if err != nil {
					return err
				}
				next, err := repo.SetPolicyEnabled(
					ctx, planted[i].ID, policyStateKind, current.Version, enabled)
				if err != nil {
					return err
				}
				if next.Enabled != enabled {
					t.Fatalf("round %d: enabled = %v, want %v", round, next.Enabled, enabled)
				}
				if next.Version != current.Version+1 {
					t.Fatalf("round %d: version = %d, want %d", round, next.Version, current.Version+1)
				}
				raw, err := readRawSpec(ctx, sc, planted[i].ID)
				if err != nil {
					return err
				}
				if !sameRawSpec(raw, before[i]) {
					t.Fatalf("round %d fixture %d: stored spec changed across a state toggle", round, i)
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("round %d toggle: %v", round, err)
		}
	}

	// The snapshot capability, which is the exact-byte reader, must agree after
	// the toggles: byte identity is not a property of the raw probe alone.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		snap := policySnapshots(t, sc)
		for i := range planted {
			got, err := snap.GetPolicySnapshot(ctx, planted[i].ID)
			if err != nil {
				return err
			}
			if !sameRawSpec(got.Spec, before[i]) {
				t.Fatalf("fixture %d: snapshot spec changed across state toggles", i)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("snapshot re-read: %v", err)
	}
}

func TestPolicyStateSQLiteByteIdentityAcrossToggles(t *testing.T) {
	st := openSQLiteTest(t, nil)
	runPolicyStateByteIdentity(t, st, provisionTenant(t, st, "policy-state"))
}

// TestPolicyStateSQLiteSemantics covers the refusals and the CAS behavior.
// policyStateBackends returns the engines the semantics matrix runs on. The
// PostgreSQL leg is announced when it is skipped: a silently absent engine reads
// exactly like a passing one.
func policyStateBackends(t *testing.T) map[string]func(t *testing.T) store.Store {
	t.Helper()
	return policyStateBackendsWithClock(t, nil)
}

// policyStateSkew is the deliberate application-clock offset. It is far outside
// any plausible engine/observation difference, so an UpdatedAt that matched the
// application clock could not be mistaken for a close engine reading.
const policyStateSkew = -72 * time.Hour

// skewedClock is an application clock parked at a fixed instant three days in
// the past. Ordinary CRUD would stamp this; the state writer must not.
type skewedClock struct{ at time.Time }

func (c skewedClock) Now() model.Timestamp { return model.NewTimestamp(c.at) }

// policyStateBackendsWithClock builds the same two backends with an optional
// injected application clock, so a test can prove the stored time came from the
// engine rather than from Config.Clock.
func policyStateBackendsWithClock(
	t *testing.T,
	clock model.Clock,
) map[string]func(t *testing.T) store.Store {
	t.Helper()
	out := map[string]func(t *testing.T) store.Store{
		"sqlite": func(t *testing.T) store.Store {
			st, err := Open(context.Background(), store.Config{
				Engine: store.EngineSQLite, DSN: ":memory:", Debug: true, Clock: clock,
			}, nil)
			if err != nil {
				t.Fatalf("open sqlite store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		},
	}
	if pgtest.Available(t) {
		out["postgres"] = func(t *testing.T) store.Store {
			st, err := Open(context.Background(), store.Config{
				Engine: store.EnginePostgres, DSN: policyStatePG(t), MaxConns: 4, Clock: clock,
			}, nil)
			if err != nil {
				t.Fatalf("open postgres store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		}
	} else {
		t.Logf("%s unset: skipping the PostgreSQL leg of the policy-state semantics matrix",
			pgtest.EnvSuperuserDSN)
	}
	return out
}

// observeNow performs the transaction-time observation SetPolicyEnabled
// requires, so a test writes through the same precondition a caller must meet.
func observeNow(t *testing.T, ctx context.Context, sc store.Scope) model.Timestamp {
	t.Helper()
	clock, ok := sc.(store.TransactionClock)
	if !ok {
		t.Fatal("scope does not implement TransactionClock")
	}
	now, err := clock.TransactionNow(ctx)
	if err != nil {
		t.Fatalf("observe transaction time: %v", err)
	}
	return now
}

func TestPolicyStateSemantics_CrossBackend(t *testing.T) {
	for name, open := range policyStateBackends(t) {
		open := open
		t.Run(name, func(t *testing.T) { runPolicyStateSemantics(t, open(t)) })
	}
}

func runPolicyStateSemantics(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	tenant := provisionTenant(t, st, "policy-state-semantics")
	foreignTenant := provisionTenant(t, st, "policy-state-foreign")

	var target, foreign model.Policy
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		target, err = plantRawPolicyPortable(ctx, sc, "target", "{\"broken\":")
		return err
	}); err != nil {
		t.Fatalf("plant target: %v", err)
	}
	if err := st.Mutate(ctx, foreignTenant, func(sc store.Scope) error {
		var err error
		foreign, err = plantRawPolicyPortable(ctx, sc, "foreign", "{}")
		return err
	}); err != nil {
		t.Fatalf("plant foreign: %v", err)
	}

	// A View must refuse the write before it reaches SQL, and must still read.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo := policyState(t, sc)
		if _, err := repo.GetPolicyState(ctx, target.ID); err != nil {
			t.Fatalf("read-only GetPolicyState: %v", err)
		}
		if _, err := repo.SetPolicyEnabled(ctx, target.ID, policyStateKind, 1, false); !errors.Is(err, store.ErrReadOnly) {
			t.Fatalf("read-only SetPolicyEnabled err = %v, want ErrReadOnly", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("read-only round: %v", err)
	}

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		observeNow(t, ctx, sc)
		repo := policyState(t, sc)
		current, err := repo.GetPolicyState(ctx, target.ID)
		if err != nil {
			return err
		}
		if current.Kind != policyStateKind || current.Version < 1 || current.ID != target.ID {
			t.Fatalf("state projection = %+v", current)
		}

		// Argument validation happens before any I/O.
		if _, err := repo.SetPolicyEnabled(ctx, model.ID(""), policyStateKind, current.Version, false); err == nil {
			t.Fatal("zero id was accepted")
		}
		if _, err := repo.SetPolicyEnabled(ctx, target.ID, "  ", current.Version, false); err == nil {
			t.Fatal("empty expected kind was accepted")
		}
		if _, err := repo.SetPolicyEnabled(ctx, target.ID, policyStateKind, 0, false); err == nil {
			t.Fatal("non-positive expected version was accepted")
		}
		if _, err := repo.SetPolicyEnabled(
			ctx, target.ID, policyStateKind, math.MaxInt64, false); !errors.Is(err, errPolicyStateOverflow) {
			t.Fatalf("MaxInt64 expected version err = %v, want the overflow refusal", err)
		}

		// Wrong kind and another tenant's row are both NotFound, and neither writes.
		if _, err := repo.SetPolicyEnabled(ctx, target.ID, "other-kind", current.Version, false); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("wrong kind err = %v, want ErrNotFound", err)
		}
		if _, err := repo.SetPolicyEnabled(ctx, foreign.ID, policyStateKind, 1, false); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("foreign row err = %v, want ErrNotFound", err)
		}
		if _, err := repo.GetPolicyState(ctx, foreign.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("foreign GetPolicyState err = %v, want ErrNotFound", err)
		}
		if _, err := repo.GetPolicyState(ctx, model.NewID()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing row err = %v, want ErrNotFound", err)
		}
		if after, err := repo.GetPolicyState(ctx, target.ID); err != nil || after.Version != current.Version {
			t.Fatalf("refused calls advanced the version: %+v err=%v", after, err)
		}

		// A same-state assertion is accepted and advances exactly once.
		same, err := repo.SetPolicyEnabled(ctx, target.ID, policyStateKind, current.Version, current.Enabled)
		if err != nil {
			return err
		}
		if same.Enabled != current.Enabled || same.Version != current.Version+1 {
			t.Fatalf("same-state assertion = %+v, want unchanged bool and version+1", same)
		}

		// The consumed version is now stale: conflict, and no further advance.
		if _, err := repo.SetPolicyEnabled(ctx, target.ID, policyStateKind, current.Version, false); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale version err = %v, want ErrConflict", err)
		}
		if after, err := repo.GetPolicyState(ctx, target.ID); err != nil || after.Version != same.Version {
			t.Fatalf("stale CAS wrote: %+v err=%v", after, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("semantics round: %v", err)
	}
}

// TestPolicyStateRollsBackWithItsCallback proves the write is ordinary
// transactional work: a later failure in the same callback discards it, and no
// independent transaction or commit exists inside the capability.
func TestPolicyStateRollsBackWithItsCallback_CrossBackend(t *testing.T) {
	for name, open := range policyStateBackends(t) {
		open := open
		t.Run(name, func(t *testing.T) { runPolicyStateRollback(t, open(t)) })
	}
}

func runPolicyStateRollback(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	tenant := provisionTenant(t, st, "policy-state-rollback")

	var target model.Policy
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		target, err = plantRawPolicyPortable(ctx, sc, "rollback", "{\"keep\":1}")
		return err
	}); err != nil {
		t.Fatalf("plant: %v", err)
	}

	var seeded store.PolicyState
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		seeded, err = policyState(t, sc).GetPolicyState(ctx, target.ID)
		return err
	}); err != nil {
		t.Fatalf("read seeded state: %v", err)
	}

	sentinel := errors.New("caller aborted after the state write")
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		observeNow(t, ctx, sc)
		if _, err := policyState(t, sc).SetPolicyEnabled(
			ctx, target.ID, policyStateKind, seeded.Version, !seeded.Enabled); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("Mutate err = %v, want the sentinel", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		got, err := policyState(t, sc).GetPolicyState(ctx, target.ID)
		if err != nil {
			return err
		}
		if got.Version != seeded.Version || got.Enabled != seeded.Enabled {
			t.Fatalf("state survived a rolled-back callback: %+v, want %+v", got, seeded)
		}
		return nil
	}); err != nil {
		t.Fatalf("post-rollback read: %v", err)
	}
}

// TestPolicyStateMalformedScalarsAreContained is the F1 case, end to end on a
// real engine. The policy schema declares ordinary INTEGER/TEXT columns with no
// STRICT mode and no metadata CHECK, so SQLite genuinely stores these cells --
// no guard is weakened to plant them. A typed scan of such a row returns the
// stored value inside a conversion error; this asserts the refusal is one of the
// constant sentinels and that no fragment of the cell reaches the caller.
func TestPolicyStateMalformedScalarsAreContained_CrossBackend(t *testing.T) {
	for name, open := range policyStateBackends(t) {
		open := open
		t.Run(name, func(t *testing.T) {
			runPolicyStateMalformedScalars(t, open(t), name == "sqlite")
		})
	}
}

// policyStateMalformedCase is one planted cell. sqliteOnly marks the cases whose
// VALUE is of a storage class PostgreSQL's column type rejects at write time --
// that is a type restriction, not metadata validation, and it is the only reason
// a case is skipped there.
type policyStateMalformedCase struct {
	name       string
	column     string
	value      any
	want       error
	sqliteOnly bool
}

// runPolicyStateMalformedScalars plants each cell through direct SQL with every
// guard intact. PostgreSQL's generated table declares version BIGINT NOT NULL,
// kind and updated_at TEXT NOT NULL, and policyDescriptor carries no CHECK, so
// version 0/-3, an empty kind and an unparseable updated_at are LEGAL fixtures
// there: the engine constrains storage class, not meaning.
func runPolicyStateMalformedScalars(t *testing.T, st store.Store, sqlite bool) {
	t.Helper()
	ctx := context.Background()
	tenant := provisionTenant(t, st, "policy-state-malformed")

	const leak = "SUPERSECRETCELL"
	for _, tc := range []policyStateMalformedCase{
		{name: "enabled as text", column: "enabled", value: leak, want: errPolicyStateEnabled, sqliteOnly: true},
		{name: "enabled as out-of-range integer", column: "enabled", value: int64(7), want: errPolicyStateEnabled, sqliteOnly: true},
		{name: "version as text", column: "version", value: leak, want: errPolicyStateVersion, sqliteOnly: true},
		{name: "version as zero", column: "version", value: int64(0), want: errPolicyStateVersion},
		{name: "version as negative", column: "version", value: int64(-3), want: errPolicyStateVersion},
		{name: "updated_at unparseable", column: "updated_at", value: leak, want: errPolicyStateUpdated},
		{name: "kind emptied", column: "kind", value: "", want: errPolicyStateKind},
	} {
		if tc.sqliteOnly && !sqlite {
			t.Run(tc.name, func(t *testing.T) {
				t.Skipf("PostgreSQL rejects this storage class at write time (%s column type), "+
					"so the row cannot exist without altering the schema", tc.column)
			})
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			var target model.Policy
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				var err error
				target, err = plantRawPolicyPortable(ctx, sc, "malformed-"+tc.column, "{}")
				if err != nil {
					return err
				}
				ts := sc.(*tenantScope)
				// #nosec G202 -- the column comes from this test's closed literal set
				_, err = ts.tx.ExecContext(ctx, ts.s.dia.Rebind(
					"UPDATE policies SET "+tc.column+" = ? WHERE id = ? AND tenant_id = ?"),
					tc.value, target.ID.String(), sc.Tenant().String())
				return err
			}); err != nil {
				t.Fatalf("plant malformed %s: %v", tc.column, err)
			}

			assertContained := func(what string, err error) {
				t.Helper()
				if !errors.Is(err, tc.want) {
					t.Fatalf("%s err = %v, want %v", what, err, tc.want)
				}
				if err != nil && strings.Contains(err.Error(), leak) {
					t.Fatalf("%s error discloses the stored cell: %v", what, err)
				}
				if err != nil && strings.Contains(err.Error(), target.ID.String()) {
					t.Fatalf("%s error discloses the row id: %v", what, err)
				}
			}

			// Seam 1: the ordinary read.
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				_, err := policyState(t, sc).GetPolicyState(ctx, target.ID)
				assertContained("GetPolicyState", err)
				return nil
			}); err != nil {
				t.Fatalf("read seam: %v", err)
			}

			// Seam 2: the zero-row explanation after a CAS that cannot match, and
			// the row must be unchanged afterwards.
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				observeNow(t, ctx, sc)
				_, err := policyState(t, sc).SetPolicyEnabled(
					ctx, target.ID, policyStateKind, 999_999, true)
				assertContained("SetPolicyEnabled explanation", err)
				return nil
			}); err != nil {
				t.Fatalf("explanation seam: %v", err)
			}
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				ts := sc.(*tenantScope)
				var stored any
				// #nosec G202 -- closed literal set, as above
				if err := ts.tx.QueryRowContext(ctx, ts.s.dia.Rebind(
					"SELECT "+tc.column+" FROM policies WHERE id = ? AND tenant_id = ?"),
					target.ID.String(), sc.Tenant().String()).Scan(&stored); err != nil {
					return err
				}
				if fmt.Sprint(stored) != fmt.Sprint(tc.value) {
					t.Fatalf("refused call wrote %s: got %v, want %v", tc.column, stored, tc.value)
				}
				return nil
			}); err != nil {
				t.Fatalf("no-write check: %v", err)
			}
		})
	}
}

// TestPolicyStateDriverValueForms pins the supported driver scalar forms
// directly, including the PostgreSQL-shaped ones SQLite never produces.
func TestPolicyStateDriverValueForms(t *testing.T) {
	id := model.NewID()
	stamp := model.NewTimestamp(time.Now().UTC()).String()
	base := func() []any { return []any{id.String(), policyStateKind, true, int64(3), stamp} }

	got, err := policyStateFromDriverValues(base())
	if err != nil || got.ID != id || !got.Enabled || got.Version != 3 {
		t.Fatalf("PostgreSQL-shaped row = %+v err=%v", got, err)
	}
	// SQLite renders text as []byte and BOOLEAN as INTEGER; both are supported.
	sqliteShaped := []any{[]byte(id.String()), []byte(policyStateKind), int64(0), int64(3), []byte(stamp)}
	got, err = policyStateFromDriverValues(sqliteShaped)
	if err != nil || got.ID != id || got.Enabled || got.Version != 3 {
		t.Fatalf("SQLite-shaped row = %+v err=%v", got, err)
	}

	for name, tc := range map[string]struct {
		mutate func([]any)
		want   error
	}{
		"id nil":            {func(r []any) { r[0] = nil }, errPolicyStateID},
		"id non-canonical":  {func(r []any) { r[0] = "not-an-id" }, errPolicyStateID},
		"id numeric":        {func(r []any) { r[0] = int64(1) }, errPolicyStateID},
		"kind empty":        {func(r []any) { r[1] = "" }, errPolicyStateKind},
		"kind nil":          {func(r []any) { r[1] = nil }, errPolicyStateKind},
		"enabled float":     {func(r []any) { r[2] = 1.0 }, errPolicyStateEnabled},
		"enabled text":      {func(r []any) { r[2] = "true" }, errPolicyStateEnabled},
		"enabled integer 2": {func(r []any) { r[2] = int64(2) }, errPolicyStateEnabled},
		"enabled nil":       {func(r []any) { r[2] = nil }, errPolicyStateEnabled},
		"version text":      {func(r []any) { r[3] = "3" }, errPolicyStateVersion},
		"version zero":      {func(r []any) { r[3] = int64(0) }, errPolicyStateVersion},
		"version nil":       {func(r []any) { r[3] = nil }, errPolicyStateVersion},
		"updated_at bad":    {func(r []any) { r[4] = "not-a-time" }, errPolicyStateUpdated},
		"updated_at nil":    {func(r []any) { r[4] = nil }, errPolicyStateUpdated},
	} {
		row := base()
		tc.mutate(row)
		out, err := policyStateFromDriverValues(row)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s = %+v err = %v, want %v", name, out, err, tc.want)
		}
	}
}

// TestPolicyStateUsesObservedTransactionTime is the F2 case: updated_at must be
// the engine time the caller observed through this transaction, not the injected
// application clock, and a missing observation must refuse before writing.
func TestPolicyStateUsesObservedTransactionTime_CrossBackend(t *testing.T) {
	skew := skewedClock{at: time.Now().UTC().Add(policyStateSkew)}
	for name, open := range policyStateBackendsWithClock(t, skew) {
		open := open
		t.Run(name, func(t *testing.T) { runPolicyStateObservedTime(t, open(t), skew) })
	}
}

func runPolicyStateObservedTime(t *testing.T, st store.Store, skew skewedClock) {
	t.Helper()
	ctx := context.Background()
	tenant := provisionTenant(t, st, "policy-state-clock")

	var target model.Policy
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		target, err = plantRawPolicyPortable(ctx, sc, "clock", "{}")
		return err
	}); err != nil {
		t.Fatalf("plant: %v", err)
	}
	var seeded store.PolicyState
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		seeded, err = policyState(t, sc).GetPolicyState(ctx, target.ID)
		return err
	}); err != nil {
		t.Fatalf("seed read: %v", err)
	}

	// Without the caller's observation the write is refused BEFORE any SQL.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := policyState(t, sc).SetPolicyEnabled(
			ctx, target.ID, policyStateKind, seeded.Version, !seeded.Enabled)
		if !errors.Is(err, store.ErrTransactionTimeNotObserved) {
			t.Fatalf("unobserved write err = %v, want ErrTransactionTimeNotObserved", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("unobserved round: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		after, err := policyState(t, sc).GetPolicyState(ctx, target.ID)
		if err != nil {
			return err
		}
		if after.Version != seeded.Version || !after.UpdatedAt.Time().Equal(seeded.UpdatedAt.Time()) {
			t.Fatalf("refused write changed the row: %+v, want %+v", after, seeded)
		}
		return nil
	}); err != nil {
		t.Fatalf("post-refusal read: %v", err)
	}

	// With the observation, updated_at equals it EXACTLY. The injected
	// application clock is deliberately skewed far away, so a fallback to it
	// could not produce this value by coincidence.
	var observed model.Timestamp
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		observed = observeNow(t, ctx, sc)
		// The injected application clock is parked three days in the past, so the
		// engine observation must differ from it by a wide margin. Without this
		// the equality below could pass on a store whose clock was never skewed.
		appNow := skew.Now()
		if observed == appNow {
			t.Fatalf("observed engine time equals the injected application clock %v", appNow)
		}
		if gap := observed.Time().Sub(appNow.Time()); gap < 48*time.Hour {
			t.Fatalf("observed engine time %v is only %v from the skewed application clock %v",
				observed, gap, appNow)
		}
		got, err := policyState(t, sc).SetPolicyEnabled(
			ctx, target.ID, policyStateKind, seeded.Version, !seeded.Enabled)
		if err != nil {
			return err
		}
		if got.UpdatedAt != observed {
			t.Fatalf("updated_at = %v, want the observed engine time %v", got.UpdatedAt, observed)
		}
		return nil
	}); err != nil {
		t.Fatalf("observed round: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		after, err := policyState(t, sc).GetPolicyState(ctx, target.ID)
		if err != nil {
			return err
		}
		if after.UpdatedAt != observed {
			t.Fatalf("persisted updated_at = %v, want %v", after.UpdatedAt, observed)
		}
		if after.Version != seeded.Version+1 {
			t.Fatalf("version = %d, want %d", after.Version, seeded.Version+1)
		}
		if after.UpdatedAt.Time().Sub(skew.Now().Time()) < 48*time.Hour {
			t.Fatalf("persisted updated_at %v is near the skewed application clock", after.UpdatedAt)
		}
		return nil
	}); err != nil {
		t.Fatalf("persisted read: %v", err)
	}

	// A FAILED observation must invalidate a previous SUCCESSFUL one, or a stale
	// retained stamp would silently satisfy the precondition. The failing read
	// runs on a canceled CHILD context so the outer transaction and its own
	// context stay usable: the refusal and the readback must both be assessable,
	// and a generic transaction-aborted error would not prove this sentinel.
	var state struct {
		first     model.Timestamp
		failErr   error
		setErr    error
		readErr   error
		afterSet  store.PolicyState
		txUsable  bool
		setBefore store.PolicyState
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			t.Fatal("scope does not implement TransactionClock")
		}
		var err error
		state.first, err = clock.TransactionNow(ctx)
		if err != nil {
			return err
		}
		state.setBefore, err = policyState(t, sc).GetPolicyState(ctx, target.ID)
		if err != nil {
			return err
		}

		dead, cancel := context.WithCancel(ctx)
		cancel()
		_, state.failErr = clock.TransactionNow(dead)

		// The write is attempted on the LIVE outer context.
		_, state.setErr = policyState(t, sc).SetPolicyEnabled(
			ctx, target.ID, policyStateKind, state.setBefore.Version, !state.setBefore.Enabled)

		state.afterSet, state.readErr = policyState(t, sc).GetPolicyState(ctx, target.ID)
		state.txUsable = state.readErr == nil
		return nil
	}); err != nil {
		t.Fatalf("invalidation round: %v", err)
	}

	if state.failErr == nil {
		t.Fatal("TransactionNow on a canceled child context returned no error")
	}
	if !errors.Is(state.setErr, store.ErrTransactionTimeNotObserved) {
		t.Fatalf("after a failed observation, Set err = %v, want ErrTransactionTimeNotObserved "+
			"(a stale successful observation at %v must not satisfy the precondition)",
			state.setErr, state.first)
	}
	// Truthful capture of backend behavior: if this driver leaves the outer
	// transaction unusable after the canceled child read, the readback cannot be
	// asserted and that is reported rather than glossed.
	if !state.txUsable {
		t.Logf("backend behavior: the outer transaction was not usable for readback after the "+
			"canceled child observation (read err = %v); the sentinel above is still asserted",
			state.readErr)
	} else if state.afterSet.Version != state.setBefore.Version ||
		state.afterSet.Enabled != state.setBefore.Enabled {
		t.Fatalf("refused write changed the row: %+v, want %+v", state.afterSet, state.setBefore)
	}

	// The row is unchanged in a fresh transaction either way.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		final, err := policyState(t, sc).GetPolicyState(ctx, target.ID)
		if err != nil {
			return err
		}
		if final.Version != state.setBefore.Version || final.Enabled != state.setBefore.Enabled {
			t.Fatalf("refused write persisted: %+v, want %+v", final, state.setBefore)
		}
		return nil
	}); err != nil {
		t.Fatalf("final read: %v", err)
	}
}

// TestPolicyRepositoryExposesEveryCapability pins the composed method set: the
// state capability is ADDITIVE and removes nothing R0 or the ordinary CRUD had.
func TestPolicyRepositoryExposesEveryCapability(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "policy-capabilities")
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo := sc.Policies()
		if _, ok := repo.(store.Repository[model.Policy]); !ok {
			t.Fatal("policy repository lost Repository[Policy]")
		}
		if _, ok := repo.(store.RowLocker[model.Policy]); !ok {
			t.Fatal("policy repository lost RowLocker[Policy]")
		}
		if _, ok := repo.(store.PolicySnapshotRepository); !ok {
			t.Fatal("policy repository lost PolicySnapshotRepository")
		}
		if _, ok := repo.(store.PolicyStateWriter); !ok {
			t.Fatal("policy repository lacks PolicyStateWriter")
		}
		return nil
	}); err != nil {
		t.Fatalf("capability assertions: %v", err)
	}
}

// policyStatePG provisions an isolated PostgreSQL database for this leg.
func policyStatePG(t *testing.T) string {
	t.Helper()
	if !pgtest.Available(t) {
		t.Skipf("set %s (a superuser DSN) to run the policy-state PostgreSQL leg", pgtest.EnvSuperuserDSN)
	}
	superDSN := os.Getenv(pgtest.EnvSuperuserDSN)
	suffix := pgtest.Suffix(t)
	role := "olv_app_" + suffix
	spec := store.PgProvisionSpec{
		Database: "olv_" + suffix,
		App:      store.PgRole{Name: role, Password: "pw-" + suffix},
	}
	pgtest.Provision(t, superDSN, spec, ProvisionPostgres, role)
	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse %s: %v", pgtest.EnvSuperuserDSN, err)
	}
	u.User = url.UserPassword(spec.App.Name, spec.App.Password)
	u.Path = "/" + spec.Database
	return u.String()
}

func TestPolicyStatePostgresByteIdentityAcrossToggles(t *testing.T) {
	dsn := policyStatePG(t)
	st, err := Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runPolicyStateByteIdentity(t, st, provisionTenant(t, st, "policy-state-pg"))
}

// TestPolicyStatePostgresConcurrentCASAdmitsOne is the leg SQLite cannot run:
// under READ COMMITTED on separate connections, N flips race for one expected
// version and exactly one may win.
func TestPolicyStatePostgresConcurrentCASAdmitsOne(t *testing.T) {
	dsn := policyStatePG(t)
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 8}, nil)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tenant := provisionTenant(t, st, "policy-state-pg-cas")

	var target model.Policy
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		target, err = plantRawPolicyPortable(ctx, sc, "cas", "{\"keep\":true}")
		return err
	}); err != nil {
		t.Fatalf("plant: %v", err)
	}
	var seeded store.PolicyState
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		seeded, err = policyState(t, sc).GetPolicyState(ctx, target.ID)
		return err
	}); err != nil {
		t.Fatalf("read seeded state: %v", err)
	}

	const flips = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	var mu sync.Mutex
	var admitted, conflicts int
	wg.Add(flips)
	for i := 0; i < flips; i++ {
		go func() {
			defer wg.Done()
			<-start
			err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				observeNow(t, ctx, sc)
				_, serr := policyState(t, sc).SetPolicyEnabled(
					ctx, target.ID, policyStateKind, seeded.Version, !seeded.Enabled)
				return serr
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				admitted++
			case errors.Is(err, store.ErrConflict):
				conflicts++
			default:
				t.Errorf("unexpected concurrent error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if admitted != 1 {
		t.Fatalf("admitted %d flips for one expected version, want exactly 1", admitted)
	}
	if conflicts != flips-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, flips-1)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		got, err := policyState(t, sc).GetPolicyState(ctx, target.ID)
		if err != nil {
			return err
		}
		if got.Version != seeded.Version+1 {
			t.Fatalf("version = %d, want exactly one advance from %d", got.Version, seeded.Version)
		}
		return nil
	}); err != nil {
		t.Fatalf("post-race read: %v", err)
	}
}
