// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	authorizationEpochMatrixClock uint8 = 1 << iota
	authorizationEpochMatrixLocker
	authorizationEpochMatrixAuthority
)

type authorizationEpochTestPorts struct {
	readFact  store.AuthorizationFactRef
	nextFact  *store.AuthorizationFactRef
	readErr   error
	bumpErr   error
	readCalls int
	bumpCalls int
}

func (p *authorizationEpochTestPorts) ReadAuthorizationEpoch(
	context.Context,
) (store.AuthorizationFactRef, error) {
	p.readCalls++
	return p.readFact, p.readErr
}

func (p *authorizationEpochTestPorts) BumpAuthorizationEpoch(
	_ context.Context,
	expected store.AuthorizationFactRef,
) (store.AuthorizationFactRef, error) {
	p.bumpCalls++
	if p.bumpErr != nil {
		return store.AuthorizationFactRef{}, p.bumpErr
	}
	if p.nextFact != nil {
		return *p.nextFact, nil
	}
	next := expected
	next.Version++
	return next, nil
}

type authorizationEpochReaderOnlyScope struct {
	store.Scope
	reader store.AuthorizationEpochReader
}

func (s authorizationEpochReaderOnlyScope) ReadAuthorizationEpoch(
	ctx context.Context,
) (store.AuthorizationFactRef, error) {
	return s.reader.ReadAuthorizationEpoch(ctx)
}

type authorizationEpochBumperOnlyScope struct {
	store.Scope
	bumper store.AuthorizationEpochBumper
}

func (s authorizationEpochBumperOnlyScope) BumpAuthorizationEpoch(
	ctx context.Context,
	expected store.AuthorizationFactRef,
) (store.AuthorizationFactRef, error) {
	return s.bumper.BumpAuthorizationEpoch(ctx, expected)
}

// authorizationEpochTestRawMarker is an intentionally wider method set. The
// confined adapter must not promote either method from the raw scope.
type authorizationEpochTestRawMarker struct{}

func (authorizationEpochTestRawMarker) RawAuthorizationScope() store.Scope { return nil }

func (authorizationEpochTestRawMarker) AuthorizationEpochGenericRepo() store.GenericRepo {
	return nil
}

func authorizationEpochMatrixRaw(
	raw store.Scope,
	clock store.TransactionClock,
	locker store.TransactionLocker,
	authority store.AuthoritySnapshotLocker,
	directory store.DirectorySnapshotReader,
	ports *authorizationEpochTestPorts,
	mask uint8,
	withDirectory bool,
) store.Scope {
	marker := authorizationEpochTestRawMarker{}
	switch mask {
	case 0:
		if withDirectory {
			return struct {
				store.Scope
				store.DirectorySnapshotReader
				*authorizationEpochTestPorts
				authorizationEpochTestRawMarker
			}{raw, directory, ports, marker}
		}
		return struct {
			store.Scope
			*authorizationEpochTestPorts
			authorizationEpochTestRawMarker
		}{raw, ports, marker}
	case authorizationEpochMatrixClock:
		if withDirectory {
			return struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				*authorizationEpochTestPorts
				authorizationEpochTestRawMarker
			}{raw, clock, directory, ports, marker}
		}
		return struct {
			store.Scope
			store.TransactionClock
			*authorizationEpochTestPorts
			authorizationEpochTestRawMarker
		}{raw, clock, ports, marker}
	case authorizationEpochMatrixLocker:
		if withDirectory {
			return struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				*authorizationEpochTestPorts
				authorizationEpochTestRawMarker
			}{raw, locker, directory, ports, marker}
		}
		return struct {
			store.Scope
			store.TransactionLocker
			*authorizationEpochTestPorts
			authorizationEpochTestRawMarker
		}{raw, locker, ports, marker}
	case authorizationEpochMatrixAuthority:
		if withDirectory {
			return struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				*authorizationEpochTestPorts
				authorizationEpochTestRawMarker
			}{raw, authority, directory, ports, marker}
		}
		return struct {
			store.Scope
			store.AuthoritySnapshotLocker
			*authorizationEpochTestPorts
			authorizationEpochTestRawMarker
		}{raw, authority, ports, marker}
	case authorizationEpochMatrixClock | authorizationEpochMatrixLocker:
		if withDirectory {
			return struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				*authorizationEpochTestPorts
				authorizationEpochTestRawMarker
			}{raw, clock, locker, directory, ports, marker}
		}
		return struct {
			store.Scope
			store.TransactionClock
			store.TransactionLocker
			*authorizationEpochTestPorts
			authorizationEpochTestRawMarker
		}{raw, clock, locker, ports, marker}
	case authorizationEpochMatrixClock | authorizationEpochMatrixAuthority:
		if withDirectory {
			return struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				*authorizationEpochTestPorts
				authorizationEpochTestRawMarker
			}{raw, clock, authority, directory, ports, marker}
		}
		return struct {
			store.Scope
			store.TransactionClock
			store.AuthoritySnapshotLocker
			*authorizationEpochTestPorts
			authorizationEpochTestRawMarker
		}{raw, clock, authority, ports, marker}
	case authorizationEpochMatrixLocker | authorizationEpochMatrixAuthority:
		if withDirectory {
			return struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				*authorizationEpochTestPorts
				authorizationEpochTestRawMarker
			}{raw, locker, authority, directory, ports, marker}
		}
		return struct {
			store.Scope
			store.TransactionLocker
			store.AuthoritySnapshotLocker
			*authorizationEpochTestPorts
			authorizationEpochTestRawMarker
		}{raw, locker, authority, ports, marker}
	case authorizationEpochMatrixClock |
		authorizationEpochMatrixLocker |
		authorizationEpochMatrixAuthority:
		if withDirectory {
			return struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				*authorizationEpochTestPorts
				authorizationEpochTestRawMarker
			}{raw, clock, locker, authority, directory, ports, marker}
		}
		return struct {
			store.Scope
			store.TransactionClock
			store.TransactionLocker
			store.AuthoritySnapshotLocker
			*authorizationEpochTestPorts
			authorizationEpochTestRawMarker
		}{raw, clock, locker, authority, ports, marker}
	default:
		panic("unknown authorization epoch capability mask")
	}
}

func TestAuthorizationEpochDescriptorAndClosedAllowlist(t *testing.T) {
	if authorizationEpochDescriptor.Kind != model.AuthorizationEpochKind ||
		authorizationEpochDescriptor.Table != "core_authorization_epoch" ||
		!authorizationEpochDescriptor.AuthorizationFact ||
		authorizationEpochDescriptor.AuthorizationLockOrder != 25 {
		t.Fatalf("authorization epoch descriptor = %+v", authorizationEpochDescriptor)
	}
	if len(authorizationEpochDescriptor.Fields) != 0 {
		t.Fatalf("authorization epoch exposes payload fields: %+v", authorizationEpochDescriptor.Fields)
	}
	wantChecks := []string{
		"id = tenant_id",
		"version >= 1",
		"tenant_id <> 'ffffffff-ffff-ffff-ffff-ffffffffffff'",
	}
	if len(authorizationEpochDescriptor.Checks) != len(wantChecks) {
		t.Fatalf("authorization epoch checks = %v, want %v", authorizationEpochDescriptor.Checks, wantChecks)
	}
	for i := range wantChecks {
		if authorizationEpochDescriptor.Checks[i] != wantChecks[i] {
			t.Fatalf("authorization epoch check %d = %q, want %q",
				i, authorizationEpochDescriptor.Checks[i], wantChecks[i])
		}
	}
	if !allowedAuthorizationFactKind(model.AuthorizationEpochKind) {
		t.Fatal("exact authorization epoch kind is absent from authority allowlist")
	}
	for _, lookalike := range []model.Kind{
		"core.authorization_epochs", "core.Authorization_epoch", "authorization_epoch",
	} {
		if allowedAuthorizationFactKind(lookalike) {
			t.Fatalf("lookalike authorization fact kind %q is allowlisted", lookalike)
		}
	}

	var occurrences int
	for _, descriptor := range coreDescriptors() {
		if descriptor.Kind == model.AuthorizationEpochKind {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("core descriptor occurrences = %d, want one", occurrences)
	}
}

func TestAuthorizationEpochWorkspaceConfinementPreservesExactCapabilityMatrix(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "authorization-epoch-confined-matrix")

	err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		workspace, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		clock := raw.(store.TransactionClock)
		locker := raw.(store.TransactionLocker)
		authority := raw.(store.AuthoritySnapshotLocker)
		directory := raw.(store.DirectorySnapshotReader)
		fact := store.AuthorizationFactRef{
			Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: 7,
		}
		names := []string{
			"authorization only",
			"clock authorization",
			"locker authorization",
			"clock locker authorization",
			"authority authorization",
			"clock authority authorization",
			"locker authority authorization",
			"all transaction capabilities authorization",
		}
		for _, withDirectory := range []bool{false, true} {
			directoryName := "without directory"
			if withDirectory {
				directoryName = "with directory"
			}
			t.Run(directoryName, func(t *testing.T) {
				for mask := uint8(0); mask < 8; mask++ {
					t.Run(names[mask], func(t *testing.T) {
						ports := &authorizationEpochTestPorts{readFact: fact}
						testRaw := authorizationEpochMatrixRaw(
							raw, clock, locker, authority, directory, ports, mask, withDirectory,
						)
						confined, confineErr := store.ConfineWorkspace(ctx, testRaw, workspace.ID)
						if confineErr != nil {
							t.Fatalf("confine: %v", confineErr)
						}
						_, hasClock := confined.(store.TransactionClock)
						_, hasLocker := confined.(store.TransactionLocker)
						_, hasAuthority := confined.(store.AuthoritySnapshotLocker)
						_, hasDirectory := confined.(store.DirectorySnapshotReader)
						reader, hasReader := confined.(store.AuthorizationEpochReader)
						bumper, hasBumper := confined.(store.AuthorizationEpochBumper)
						_, hasStore := confined.(store.AuthorizationEpochStore)
						if hasClock != (mask&authorizationEpochMatrixClock != 0) ||
							hasLocker != (mask&authorizationEpochMatrixLocker != 0) ||
							hasAuthority != (mask&authorizationEpochMatrixAuthority != 0) ||
							hasDirectory != withDirectory || !hasReader || !hasBumper || !hasStore {
							t.Fatalf(
								"method set clock=%t locker=%t authority=%t directory=%t reader=%t bumper=%t store=%t",
								hasClock, hasLocker, hasAuthority, hasDirectory,
								hasReader, hasBumper, hasStore,
							)
						}
						got, readErr := reader.ReadAuthorizationEpoch(ctx)
						if readErr != nil || got != fact {
							t.Fatalf("confined read = %+v, %v; want %+v", got, readErr, fact)
						}
						next, bumpErr := bumper.BumpAuthorizationEpoch(ctx, got)
						if bumpErr != nil || next.Version != got.Version+1 ||
							next.Kind != got.Kind || next.ID != got.ID {
							t.Fatalf("confined bump = %+v, %v; prior %+v", next, bumpErr, got)
						}
						if ports.readCalls != 1 || ports.bumpCalls != 1 {
							t.Fatalf("delegations read=%d bump=%d, want 1/1",
								ports.readCalls, ports.bumpCalls)
						}
						if _, widened := confined.(interface{ RawAuthorizationScope() store.Scope }); widened {
							t.Fatal("confined authorization ports exposed the raw scope")
						}
						if _, widened := confined.(interface{ AuthorizationEpochGenericRepo() store.GenericRepo }); widened {
							t.Fatal("confined authorization ports exposed a generic repository")
						}
						same, sameErr := store.ConfineWorkspace(ctx, confined, workspace.ID)
						if sameErr != nil || same != confined {
							t.Fatalf("same-workspace confinement = %T, %v; want idempotent", same, sameErr)
						}
						if _, retargetErr := store.ConfineWorkspace(ctx, confined, model.NewID()); !errors.Is(retargetErr, store.ErrWorkspaceConfinement) {
							t.Fatalf("workspace retarget error = %v, want ErrWorkspaceConfinement", retargetErr)
						}
					})
				}
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("authorization confinement matrix: %v", err)
	}
}

func TestAuthorizationEpochWorkspaceConfinementIsTenantBoundAndPartialClosed(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "authorization-epoch-confined-binding")

	err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		workspace, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		valid := store.AuthorizationFactRef{
			Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: 3,
		}
		ports := &authorizationEpochTestPorts{readFact: valid}
		partial := []struct {
			name string
			raw  store.Scope
		}{
			{name: "absent", raw: struct{ store.Scope }{Scope: raw}},
			{name: "reader only", raw: authorizationEpochReaderOnlyScope{
				Scope: raw, reader: ports,
			}},
			{name: "bumper only", raw: authorizationEpochBumperOnlyScope{
				Scope: raw, bumper: ports,
			}},
		}
		for _, test := range partial {
			t.Run(test.name, func(t *testing.T) {
				confined, confineErr := store.ConfineWorkspace(ctx, test.raw, workspace.ID)
				if confineErr != nil {
					t.Fatalf("confine partial capability: %v", confineErr)
				}
				_, hasReader := confined.(store.AuthorizationEpochReader)
				_, hasBumper := confined.(store.AuthorizationEpochBumper)
				if hasReader || hasBumper {
					t.Fatalf("partial capability became reader=%t bumper=%t", hasReader, hasBumper)
				}
			})
		}

		combinedRaw := authorizationEpochMatrixRaw(
			raw,
			raw.(store.TransactionClock),
			raw.(store.TransactionLocker),
			raw.(store.AuthoritySnapshotLocker),
			raw.(store.DirectorySnapshotReader),
			ports,
			0,
			false,
		)
		confined, err := store.ConfineWorkspace(ctx, combinedRaw, workspace.ID)
		if err != nil {
			return err
		}
		capability := confined.(store.AuthorizationEpochStore)

		ports.readFact.ID = model.NewID()
		if _, err := capability.ReadAuthorizationEpoch(ctx); !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
			t.Fatalf("foreign read error = %v, want unavailable", err)
		}
		ports.readFact = valid
		readFailure := errors.New("read generation failed")
		ports.readErr = readFailure
		if _, err := capability.ReadAuthorizationEpoch(ctx); !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
			t.Fatalf("reader error = %v, want unavailable", err)
		}
		ports.readErr = nil

		foreign := valid
		foreign.ID = model.NewID()
		before := ports.bumpCalls
		if _, err := capability.BumpAuthorizationEpoch(ctx, foreign); !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
			t.Fatalf("foreign expected error = %v, want unavailable", err)
		}
		if ports.bumpCalls != before {
			t.Fatalf("foreign expected delegated %d bump(s), want zero", ports.bumpCalls-before)
		}
		wrongNext := valid
		wrongNext.Version += 2
		ports.nextFact = &wrongNext
		if _, err := capability.BumpAuthorizationEpoch(ctx, valid); !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
			t.Fatalf("discontinuous next error = %v, want unavailable", err)
		}
		ports.nextFact = nil
		next, err := capability.BumpAuthorizationEpoch(ctx, valid)
		if err != nil || next.Version != valid.Version+1 || next.ID != valid.ID {
			t.Fatalf("valid tenant-bound bump = %+v, %v", next, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("authorization confinement binding: %v", err)
	}
}

func TestAuthorizationEpochSQLiteWorkspaceConfinementForwardsRealTransaction(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "authorization-epoch-confined-real")
	initial := authorizationEpochTestRead(t, st, tenant)
	rollback := errors.New("rollback confined authorization bump")

	err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		workspace, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		confined, err := store.ConfineWorkspace(ctx, raw, workspace.ID)
		if err != nil {
			return err
		}
		capability, ok := confined.(store.AuthorizationEpochStore)
		if !ok {
			t.Fatal("confined SQL scope lost authorization epoch store")
		}
		got, err := capability.ReadAuthorizationEpoch(ctx)
		if err != nil || got != initial {
			t.Fatalf("real confined read = %+v, %v; want %+v", got, err, initial)
		}
		next, err := capability.BumpAuthorizationEpoch(ctx, got)
		if err != nil || next.Version != got.Version+1 {
			t.Fatalf("real confined bump = %+v, %v", next, err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("real confined rollback error = %v, want sentinel", err)
	}
	if got := authorizationEpochTestRead(t, st, tenant); got != initial {
		t.Fatalf("real confined rollback left %+v, want %+v", got, initial)
	}

	err = st.View(ctx, tenant, func(raw store.Scope) error {
		workspace, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		confined, err := store.ConfineWorkspace(ctx, raw, workspace.ID)
		if err != nil {
			return err
		}
		_, err = confined.(store.AuthorizationEpochBumper).BumpAuthorizationEpoch(ctx, initial)
		return err
	})
	if !errors.Is(err, store.ErrReadOnly) {
		t.Fatalf("real confined View bump error = %v, want ErrReadOnly", err)
	}
}

func TestAuthorizationEpochSQLiteSeedReadBumpOCCRollbackAndLock(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "authorization-epoch-tenant")

	initial := authorizationEpochTestRead(t, st, tenant)
	if initial.Kind != model.AuthorizationEpochKind || initial.ID != model.ID(tenant) ||
		initial.Version != 1 {
		t.Fatalf("initial authorization epoch = %+v", initial)
	}

	err := st.View(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.(store.AuthorizationEpochBumper).BumpAuthorizationEpoch(ctx, initial)
		return err
	})
	if !errors.Is(err, store.ErrReadOnly) {
		t.Fatalf("view bump error = %v, want ErrReadOnly", err)
	}

	rollback := errors.New("rollback authorization epoch bump")
	err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
		capability, ok := sc.(store.AuthorizationEpochStore)
		if !ok {
			t.Fatal("tenant scope lacks authorization epoch capability")
		}
		got, err := capability.ReadAuthorizationEpoch(ctx)
		if err != nil || got != initial {
			t.Fatalf("read before rollback bump = %+v, %v", got, err)
		}
		next, err := capability.BumpAuthorizationEpoch(ctx, got)
		if err != nil {
			t.Fatalf("bump before rollback: %v", err)
		}
		if next.Version != got.Version+1 || next.Kind != got.Kind || next.ID != got.ID {
			t.Fatalf("next witness = %+v, prior %+v", next, got)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("rollback mutate error = %v, want sentinel", err)
	}
	if got := authorizationEpochTestRead(t, st, tenant); got != initial {
		t.Fatalf("rolled-back epoch = %+v, want %+v", got, initial)
	}

	var committed store.AuthorizationFactRef
	err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		committed, err = sc.(store.AuthorizationEpochBumper).BumpAuthorizationEpoch(ctx, initial)
		return err
	})
	if err != nil {
		t.Fatalf("commit authorization epoch bump: %v", err)
	}
	if committed.Version != 2 || authorizationEpochTestRead(t, st, tenant) != committed {
		t.Fatalf("committed authorization epoch = %+v", committed)
	}

	err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.(store.AuthorizationEpochBumper).BumpAuthorizationEpoch(ctx, initial)
		return err
	})
	if !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
		t.Fatalf("stale bump error = %v, want unavailable", err)
	}

	err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
		locker := sc.(store.AuthoritySnapshotLocker)
		if err := locker.LockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{committed}); err != nil {
			return err
		}
		return locker.LockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{initial})
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale authority lock error = %v, want ErrConflict", err)
	}
}

func TestAuthorizationEpochSQLiteRejectsMalformedWitnessAbsenceAndOverflow(t *testing.T) {
	ctx := context.Background()
	t.Run("malformed expected witness", func(t *testing.T) {
		st := openSQLiteTest(t, nil)
		tenant := provisionTenant(t, st, "authorization-malformed-witness")
		initial := authorizationEpochTestRead(t, st, tenant)
		bad := []store.AuthorizationFactRef{
			{Kind: "core.authorization_epochs", ID: initial.ID, Version: initial.Version},
			{Kind: initial.Kind, ID: model.NewID(), Version: initial.Version},
			{Kind: initial.Kind, ID: initial.ID, Version: 0},
		}
		for _, witness := range bad {
			err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				_, err := sc.(store.AuthorizationEpochBumper).BumpAuthorizationEpoch(ctx, witness)
				return err
			})
			if !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
				t.Fatalf("malformed witness %+v error = %v, want unavailable", witness, err)
			}
		}
	})

	t.Run("absent row", func(t *testing.T) {
		st := openSQLiteTest(t, nil)
		tenant := provisionTenant(t, st, "authorization-absent-row")
		err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			ts := sc.(*tenantScope)
			_, err := ts.tx.ExecContext(ctx,
				"DELETE FROM main.core_authorization_epoch WHERE tenant_id = ?", tenant.String())
			return err
		})
		if err != nil {
			t.Fatalf("delete authorization epoch fixture: %v", err)
		}
		err = st.View(ctx, tenant, func(sc store.Scope) error {
			_, err := sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(ctx)
			return err
		})
		if !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
			t.Fatalf("absent read error = %v, want unavailable", err)
		}
		err = st.System(ctx, func(sys store.SystemScope) error {
			return sys.DropTenant(ctx, tenant)
		})
		if !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
			t.Fatalf("drop with absent epoch error = %v, want unavailable", err)
		}
		ss := st.(*sqlStore)
		if authorizationEpochTestCount(t, ss.db, orgDescriptor.Table, tenant) != 1 ||
			authorizationEpochTestCount(t, ss.db, directoryEpochDescriptor.Table, tenant) != 1 {
			t.Fatal("failed tenant drop did not roll back its earlier directory/audit work")
		}
	})

	t.Run("generation exhaustion", func(t *testing.T) {
		st := openSQLiteTest(t, nil)
		tenant := provisionTenant(t, st, "authorization-overflow")
		err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			ts := sc.(*tenantScope)
			_, err := ts.tx.ExecContext(ctx,
				"UPDATE main.core_authorization_epoch SET version = ? WHERE tenant_id = ?",
				int64(math.MaxInt64), tenant.String())
			return err
		})
		if err != nil {
			t.Fatalf("seed exhausted generation: %v", err)
		}
		exhausted := authorizationEpochTestRead(t, st, tenant)
		err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, err := sc.(store.AuthorizationEpochBumper).BumpAuthorizationEpoch(ctx, exhausted)
			return err
		})
		if !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
			t.Fatalf("overflow bump error = %v, want unavailable", err)
		}
		if got := authorizationEpochTestRead(t, st, tenant); got.Version != math.MaxInt64 {
			t.Fatalf("overflow changed generation to %d", got.Version)
		}
	})
}

func TestAuthorizationEpochSQLiteDuplicateAndMalformedStorageAreUnavailable(t *testing.T) {
	ctx := context.Background()
	timestamp := model.NewTimestamp(time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)).String()
	tests := []struct {
		name string
		rows func(model.TenantID) [][]any
	}{
		{
			name: "duplicate tenant rows",
			rows: func(tenant model.TenantID) [][]any {
				return [][]any{
					{tenant.String(), tenant.String(), timestamp, timestamp, int64(1)},
					{model.NewID().String(), tenant.String(), timestamp, timestamp, int64(2)},
				}
			},
		},
		{
			name: "mismatched id",
			rows: func(tenant model.TenantID) [][]any {
				return [][]any{{model.NewID().String(), tenant.String(), timestamp, timestamp, int64(1)}}
			},
		},
		{
			name: "zero version",
			rows: func(tenant model.TenantID) [][]any {
				return [][]any{{tenant.String(), tenant.String(), timestamp, timestamp, int64(0)}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := openSQLiteTest(t, nil)
			tenant := provisionTenant(t, st, "authorization-storage-"+test.name)
			err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				ts := sc.(*tenantScope)
				if _, err := ts.tx.ExecContext(ctx, "DROP TABLE main.core_authorization_epoch"); err != nil {
					return err
				}
				if _, err := ts.tx.ExecContext(ctx, `CREATE TABLE main.core_authorization_epoch (
					id TEXT NOT NULL,
					tenant_id TEXT NOT NULL,
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL,
					version INTEGER NOT NULL
				)`); err != nil {
					return err
				}
				for _, row := range test.rows(tenant) {
					if _, err := ts.tx.ExecContext(ctx, `INSERT INTO main.core_authorization_epoch
						(id, tenant_id, created_at, updated_at, version) VALUES (?, ?, ?, ?, ?)`, row...); err != nil {
						return err
					}
				}
				_, err := sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(ctx)
				if !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
					t.Fatalf("corrupt storage read error = %v, want unavailable", err)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("exercise corrupt storage: %v", err)
			}
		})
	}
}

func TestAuthorizationEpochSQLiteCreateOrgAndUpgradeBackfillAreAtomic(t *testing.T) {
	ctx := context.Background()
	t.Run("CreateOrg seed rollback", func(t *testing.T) {
		st := openSQLiteTest(t, nil)
		ss := st.(*sqlStore)
		injected := errors.New("authorization epoch seed failure")
		var attempted model.TenantID
		authorizationEpochBeforeInsertTestHook = func(tenant model.TenantID) error {
			attempted = tenant
			return injected
		}
		t.Cleanup(func() { authorizationEpochBeforeInsertTestHook = nil })
		err := st.System(ctx, func(sys store.SystemScope) error {
			_, err := sys.CreateOrg(ctx, model.Org{
				Name: "authorization-seed-failure", Slug: "authorization-seed-failure",
				Status: model.StatusActive,
			})
			return err
		})
		if !errors.Is(err, injected) || attempted.IsZero() {
			t.Fatalf("CreateOrg error/tenant = %v/%s", err, attempted)
		}
		for _, table := range []string{
			orgDescriptor.Table, directoryEpochDescriptor.Table, authorizationEpochDescriptor.Table,
		} {
			if got := authorizationEpochTestCount(t, ss.db, table, attempted); got != 0 {
				t.Fatalf("rolled-back CreateOrg left %d row(s) in %s", got, table)
			}
		}
	})

	t.Run("additive upgrade and all-tenant rollback", func(t *testing.T) {
		dsn := filepath.Join(t.TempDir(), "authorization-epoch-upgrade.db")
		cfg := store.Config{Engine: store.EngineSQLite, DSN: dsn, Debug: true}
		initial, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("open initial store: %v", err)
		}
		tenantA := provisionTenant(t, initial, "authorization-upgrade-a")
		tenantB := provisionTenant(t, initial, "authorization-upgrade-b")
		if err := initial.Close(); err != nil {
			t.Fatalf("close initial store: %v", err)
		}

		raw, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatalf("open raw upgrade fixture: %v", err)
		}
		if _, err := raw.ExecContext(ctx, "DROP TABLE core_authorization_epoch"); err != nil {
			_ = raw.Close()
			t.Fatalf("remove post-v2 table fixture: %v", err)
		}
		if err := raw.Close(); err != nil {
			t.Fatalf("close raw upgrade fixture: %v", err)
		}

		injected := errors.New("authorization backfill failure")
		var calls int
		authorizationEpochBeforeInsertTestHook = func(model.TenantID) error {
			calls++
			if calls == 2 {
				return injected
			}
			return nil
		}
		_, err = Open(ctx, cfg, nil)
		if !errors.Is(err, injected) || calls != 2 {
			t.Fatalf("failed upgrade = %v, calls=%d", err, calls)
		}
		authorizationEpochBeforeInsertTestHook = nil

		raw, err = sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatalf("reopen raw failed-upgrade fixture: %v", err)
		}
		for _, tenant := range []model.TenantID{tenantA, tenantB} {
			if got := authorizationEpochTestCount(t, raw, authorizationEpochDescriptor.Table, tenant); got != 0 {
				_ = raw.Close()
				t.Fatalf("failed backfill left %d rows for tenant %s", got, tenant)
			}
		}
		if err := raw.Close(); err != nil {
			t.Fatalf("close failed-upgrade fixture: %v", err)
		}

		reopened, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("retry additive upgrade: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		for _, tenant := range []model.TenantID{tenantA, tenantB} {
			fact := authorizationEpochTestRead(t, reopened, tenant)
			if fact.ID != model.ID(tenant) || fact.Version != 1 {
				t.Fatalf("backfilled fact for %s = %+v", tenant, fact)
			}
		}
	})
}

func authorizationEpochTestRead(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
) store.AuthorizationFactRef {
	t.Helper()
	var out store.AuthorizationFactRef
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		reader, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			t.Fatal("tenant scope lacks authorization epoch reader")
		}
		var err error
		out, err = reader.ReadAuthorizationEpoch(context.Background())
		return err
	})
	if err != nil {
		t.Fatalf("read authorization epoch: %v", err)
	}
	return out
}

func authorizationEpochTestCount(
	t *testing.T,
	db *sql.DB,
	table string,
	tenant model.TenantID,
) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM "+table+" WHERE tenant_id = ?", tenant.String(),
	).Scan(&count); err != nil {
		t.Fatalf("count %s for %s: %v", table, tenant, err)
	}
	return count
}
