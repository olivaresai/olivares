// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// directorySnapshotHiddenScope deliberately exports only store.Scope. It pins
// the negative half of the optional-capability contract: confinement may
// preserve a reader that exists, but must never fabricate one a decorator hid.
type directorySnapshotHiddenScope struct{ store.Scope }

type countingDirectorySnapshotReader struct {
	reader         store.DirectorySnapshotReader
	epochCalls     int
	tombstoneCalls int
}

func (r *countingDirectorySnapshotReader) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	r.epochCalls++
	return r.reader.ReadDirectoryEpoch(ctx)
}

func (r *countingDirectorySnapshotReader) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	r.tombstoneCalls++
	return r.reader.ReadDirectoryTombstone(ctx, ref)
}

func TestDirectorySnapshotReaderSQLiteSingleConnection(t *testing.T) {
	st, err := Open(context.Background(), store.Config{
		Engine:   store.EngineSQLite,
		DSN:      filepath.Join(t.TempDir(), "directory-snapshot.db"),
		MaxConns: 1,
		Debug:    true,
	}, nil)
	if err != nil {
		t.Fatalf("open SQLite snapshot fixture: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	directorySnapshotExercise(t, ctx, st, store.EngineSQLite)
}

func TestDirectorySnapshotReaderSurvivesWorkspaceConfinement(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, store.Config{
		Engine:   store.EngineSQLite,
		DSN:      filepath.Join(t.TempDir(), "directory-confined.db"),
		MaxConns: 1,
	}, nil)
	if err != nil {
		t.Fatalf("open SQLite confined snapshot fixture: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	tenant := provisionTenant(t, st, "directory-confined-"+uniqueSuffix())
	directorySnapshotEnsureEpoch(t, ctx, st, tenant)
	if err := st.View(ctx, tenant, func(raw store.Scope) error {
		workspace, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		confined, err := store.ConfineWorkspace(ctx, raw, workspace.ID)
		if err != nil {
			return err
		}
		reader, ok := confined.(store.DirectorySnapshotReader)
		if !ok {
			t.Fatal("workspace confinement hid DirectorySnapshotReader")
		}
		if _, ok := confined.(store.TransactionClock); !ok {
			t.Fatal("workspace confinement hid TransactionClock while preserving directory evidence")
		}
		if _, ok := confined.(store.TransactionLocker); !ok {
			t.Fatal("workspace confinement hid TransactionLocker while preserving directory evidence")
		}
		if _, ok := confined.(store.AuthoritySnapshotLocker); !ok {
			t.Fatal("workspace confinement hid AuthoritySnapshotLocker while preserving directory evidence")
		}
		epoch, err := reader.ReadDirectoryEpoch(ctx)
		if err != nil {
			return err
		}
		if epoch.TenantID != tenant || epoch.Version != 1 {
			t.Fatalf("confined directory epoch = %+v, want tenant %s version 1", epoch, tenant)
		}
		globalUser := store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalUser,
			PrincipalRef:  model.NewID(),
		}
		if witness, found, err := reader.ReadDirectoryTombstone(ctx, globalUser); err != nil ||
			found || witness != (store.DirectoryTombstoneWitness{}) {
			t.Fatalf("confined global User miss: witness=%+v found=%t err=%v",
				witness, found, err)
		}
		localAgent := store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalAgent,
			PrincipalRef:  model.NewID(),
			WorkspaceRef:  workspace.ID,
		}
		if witness, found, err := reader.ReadDirectoryTombstone(ctx, localAgent); err != nil ||
			found || witness != (store.DirectoryTombstoneWitness{}) {
			t.Fatalf("confined local Agent miss: witness=%+v found=%t err=%v",
				witness, found, err)
		}
		globalIdentity := store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalIdentity,
			PrincipalRef:  model.NewID(),
		}
		if witness, found, err := reader.ReadDirectoryTombstone(ctx, globalIdentity); err != nil ||
			found || witness != (store.DirectoryTombstoneWitness{}) {
			t.Fatalf("confined tenant-global Identity miss: witness=%+v found=%t err=%v",
				witness, found, err)
		}
		foreignAgent := localAgent
		foreignAgent.PrincipalRef = model.NewID()
		foreignAgent.WorkspaceRef = model.NewID()
		if witness, found, err := reader.ReadDirectoryTombstone(ctx, foreignAgent); found ||
			witness != (store.DirectoryTombstoneWitness{}) ||
			!errors.Is(err, store.ErrWorkspaceConfinement) {
			t.Fatalf("foreign Agent directory evidence: witness=%+v found=%t err=%v",
				witness, found, err)
		}
		emptyAgent := localAgent
		emptyAgent.PrincipalRef = model.NewID()
		emptyAgent.WorkspaceRef = ""
		if witness, found, err := reader.ReadDirectoryTombstone(ctx, emptyAgent); found ||
			witness != (store.DirectoryTombstoneWitness{}) ||
			!errors.Is(err, store.ErrWorkspaceConfinement) {
			t.Fatalf("workspace-less Agent directory evidence: witness=%+v found=%t err=%v",
				witness, found, err)
		}
		workspaceIdentity := globalIdentity
		workspaceIdentity.PrincipalRef = model.NewID()
		workspaceIdentity.WorkspaceRef = workspace.ID
		if witness, found, err := reader.ReadDirectoryTombstone(ctx, workspaceIdentity); found ||
			witness != (store.DirectoryTombstoneWitness{}) ||
			!errors.Is(err, store.ErrWorkspaceConfinement) {
			t.Fatalf("workspace-bearing Identity directory evidence: witness=%+v found=%t err=%v",
				witness, found, err)
		}

		// Re-run the rejection matrix through an instrumented raw reader. The
		// wrapper must reject every non-canonical workspace spelling before the
		// tenant-wide reader is invoked; otherwise confinement becomes an
		// existence oracle even though the final result is an error.
		spy := &countingDirectorySnapshotReader{reader: raw.(store.DirectorySnapshotReader)}
		spyRaw := struct {
			store.Scope
			store.TransactionClock
			store.TransactionLocker
			store.AuthoritySnapshotLocker
			*countingDirectorySnapshotReader
		}{
			Scope: raw, TransactionClock: raw.(store.TransactionClock),
			TransactionLocker:               raw.(store.TransactionLocker),
			AuthoritySnapshotLocker:         raw.(store.AuthoritySnapshotLocker),
			countingDirectorySnapshotReader: spy,
		}
		spyScope, err := store.ConfineWorkspace(ctx, spyRaw, workspace.ID)
		if err != nil {
			return err
		}
		spyReader := spyScope.(store.DirectorySnapshotReader)
		for name, ref := range map[string]store.DirectoryPrincipalRef{
			"foreign Agent":              foreignAgent,
			"workspace-less Agent":       emptyAgent,
			"workspace-bearing Identity": workspaceIdentity,
		} {
			if _, _, readErr := spyReader.ReadDirectoryTombstone(ctx, ref); !errors.Is(readErr, store.ErrWorkspaceConfinement) {
				t.Fatalf("%s spy error = %v, want ErrWorkspaceConfinement", name, readErr)
			}
		}
		if spy.tombstoneCalls != 0 {
			t.Fatalf("rejected tombstone reads delegated %d times, want zero", spy.tombstoneCalls)
		}
		if _, _, err := spyReader.ReadDirectoryTombstone(ctx, globalIdentity); err != nil {
			return err
		}
		if spy.tombstoneCalls != 1 {
			t.Fatalf("valid tombstone reads delegated %d times, want one", spy.tombstoneCalls)
		}
		masked, err := store.ConfineWorkspace(
			ctx, directorySnapshotHiddenScope{Scope: raw}, workspace.ID,
		)
		if err != nil {
			return err
		}
		if _, ok := masked.(store.DirectorySnapshotReader); ok {
			t.Fatal("workspace confinement fabricated a hidden DirectorySnapshotReader")
		}
		return nil
	}); err != nil {
		t.Fatalf("confined directory snapshot: %v", err)
	}
}

// TestDirectorySnapshotReaderPreservesEveryTransactionCapabilityCombination is
// the directory half of the optional-capability matrix. The existing
// TestTransactionCapabilitiesSurviveWorkspaceConfinement covers the eight
// combinations without DirectorySnapshotReader; these eight inputs prove that
// adding the reader neither hides nor fabricates clock/lock/authority methods.
func TestDirectorySnapshotReaderPreservesEveryTransactionCapabilityCombination(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "directory-capability-matrix")
	directorySnapshotEnsureEpoch(t, ctx, st, tenant)

	if err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		workspace, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		clock := raw.(store.TransactionClock)
		locker := raw.(store.TransactionLocker)
		authority := raw.(store.AuthoritySnapshotLocker)
		directory := raw.(store.DirectorySnapshotReader)

		newReader := func() *countingDirectorySnapshotReader {
			return &countingDirectorySnapshotReader{reader: directory}
		}
		directoryOnly := newReader()
		clockDirectory := newReader()
		lockerDirectory := newReader()
		authorityDirectory := newReader()
		clockLockerDirectory := newReader()
		clockAuthorityDirectory := newReader()
		lockerAuthorityDirectory := newReader()
		cases := []struct {
			name          string
			raw           store.Scope
			reader        *countingDirectorySnapshotReader
			wantClock     bool
			wantLocker    bool
			wantAuthority bool
		}{
			{
				name: "directory only",
				raw: struct {
					store.Scope
					*countingDirectorySnapshotReader
				}{Scope: raw, countingDirectorySnapshotReader: directoryOnly},
				reader: directoryOnly,
			},
			{
				name: "clock and directory",
				raw: struct {
					clockOnlyScope
					*countingDirectorySnapshotReader
				}{
					clockOnlyScope:                  clockOnlyScope{Scope: raw, clock: clock},
					countingDirectorySnapshotReader: clockDirectory,
				},
				reader: clockDirectory, wantClock: true,
			},
			{
				name: "locker and directory",
				raw: struct {
					lockerOnlyScope
					*countingDirectorySnapshotReader
				}{
					lockerOnlyScope:                 lockerOnlyScope{Scope: raw, locker: locker},
					countingDirectorySnapshotReader: lockerDirectory,
				},
				reader: lockerDirectory, wantLocker: true,
			},
			{
				name: "authority and directory",
				raw: struct {
					authorityOnlyScope
					*countingDirectorySnapshotReader
				}{
					authorityOnlyScope:              authorityOnlyScope{Scope: raw, authority: authority},
					countingDirectorySnapshotReader: authorityDirectory,
				},
				reader: authorityDirectory, wantAuthority: true,
			},
			{
				name: "clock locker and directory",
				raw: struct {
					clockLockerOnlyScope
					*countingDirectorySnapshotReader
				}{
					clockLockerOnlyScope:            clockLockerOnlyScope{Scope: raw, clock: clock, locker: locker},
					countingDirectorySnapshotReader: clockLockerDirectory,
				},
				reader: clockLockerDirectory, wantClock: true, wantLocker: true,
			},
			{
				name: "clock authority and directory",
				raw: struct {
					clockAuthorityOnlyScope
					*countingDirectorySnapshotReader
				}{
					clockAuthorityOnlyScope:         clockAuthorityOnlyScope{Scope: raw, clock: clock, authority: authority},
					countingDirectorySnapshotReader: clockAuthorityDirectory,
				},
				reader: clockAuthorityDirectory, wantClock: true, wantAuthority: true,
			},
			{
				name: "locker authority and directory",
				raw: struct {
					lockerAuthorityOnlyScope
					*countingDirectorySnapshotReader
				}{
					lockerAuthorityOnlyScope:        lockerAuthorityOnlyScope{Scope: raw, locker: locker, authority: authority},
					countingDirectorySnapshotReader: lockerAuthorityDirectory,
				},
				reader: lockerAuthorityDirectory, wantLocker: true, wantAuthority: true,
			},
			{
				name: "all", raw: raw,
				wantClock: true, wantLocker: true, wantAuthority: true,
			},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				confined, confineErr := store.ConfineWorkspace(ctx, test.raw, workspace.ID)
				if confineErr != nil {
					t.Fatalf("confine: %v", confineErr)
				}
				reader, hasDirectory := confined.(store.DirectorySnapshotReader)
				_, hasClock := confined.(store.TransactionClock)
				_, hasLocker := confined.(store.TransactionLocker)
				_, hasAuthority := confined.(store.AuthoritySnapshotLocker)
				if !hasDirectory || hasClock != test.wantClock || hasLocker != test.wantLocker ||
					hasAuthority != test.wantAuthority {
					t.Fatalf("capabilities directory=%t clock=%t locker=%t authority=%t",
						hasDirectory, hasClock, hasLocker, hasAuthority)
				}
				if _, readErr := reader.ReadDirectoryEpoch(ctx); readErr != nil {
					t.Fatalf("forwarded directory epoch: %v", readErr)
				}
				if test.reader != nil && test.reader.epochCalls != 1 {
					t.Fatalf("directory epoch delegations = %d, want 1", test.reader.epochCalls)
				}
			})
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect directory capability matrix: %v", err)
	}
}

func TestDirectorySnapshotReaderPostgresSplitOwner(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx := context.Background()
	st, err := Open(ctx, store.Config{
		Engine:   store.EnginePostgres,
		DSN:      pg.App,
		OwnerDSN: pg.Owner,
		MaxConns: 1,
	}, nil)
	if err != nil {
		t.Fatalf("open PostgreSQL split-owner snapshot fixture: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	directorySnapshotExercise(t, ctx, st, store.EnginePostgres)
}

func TestDirectorySnapshotReaderIgnoresSQLiteTempShadows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		shadow func(context.Context, *sqlStore, model.TenantID) error
		read   func(context.Context, store.Scope, store.DirectoryPrincipalRef) error
	}{
		{
			name: "epoch",
			shadow: func(ctx context.Context, raw *sqlStore, tenant model.TenantID) error {
				if _, err := raw.db.ExecContext(ctx,
					"CREATE TEMP TABLE core_directory_epoch AS SELECT * FROM main.core_directory_epoch",
				); err != nil {
					return err
				}
				_, err := raw.db.ExecContext(ctx,
					"DELETE FROM main.core_directory_epoch WHERE tenant_id = ?", tenant.String())
				return err
			},
			read: func(ctx context.Context, scope store.Scope, _ store.DirectoryPrincipalRef) error {
				_, err := scope.(store.DirectorySnapshotReader).ReadDirectoryEpoch(ctx)
				if !errors.Is(err, store.ErrDirectoryUnavailable) {
					return fmt.Errorf("epoch read through TEMP shadow returned %v", err)
				}
				return nil
			},
		},
		{
			name: "tombstone",
			shadow: func(ctx context.Context, raw *sqlStore, _ model.TenantID) error {
				_, err := raw.db.ExecContext(ctx,
					"CREATE TEMP TABLE core_directory_tombstone AS "+
						"SELECT * FROM main.core_directory_tombstone WHERE 0")
				return err
			},
			read: func(ctx context.Context, scope store.Scope, ref store.DirectoryPrincipalRef) error {
				_, found, err := scope.(store.DirectorySnapshotReader).
					ReadDirectoryTombstone(ctx, ref)
				if err != nil || !found {
					return fmt.Errorf("main tombstone hidden by TEMP shadow: found=%t err=%v", found, err)
				}
				return nil
			},
		},
		{
			name: "audit anchor",
			shadow: func(ctx context.Context, raw *sqlStore, _ model.TenantID) error {
				if _, err := raw.db.ExecContext(ctx,
					"CREATE TEMP TABLE audit_events AS SELECT * FROM main.audit_events",
				); err != nil {
					return err
				}
				_, err := raw.db.ExecContext(ctx, "UPDATE temp.audit_events SET action = 'shadowed'")
				return err
			},
			read: func(ctx context.Context, scope store.Scope, ref store.DirectoryPrincipalRef) error {
				_, found, err := scope.(store.DirectorySnapshotReader).
					ReadDirectoryTombstone(ctx, ref)
				if err != nil || !found {
					return fmt.Errorf("main anchor hidden by TEMP shadow: found=%t err=%v", found, err)
				}
				return nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := Open(ctx, store.Config{
				Engine: store.EngineSQLite,
				DSN:    filepath.Join(t.TempDir(), "directory-shadow.db"), MaxConns: 1,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			raw := st.(*sqlStore)
			tenant := provisionTenant(t, st, "directory-shadow-"+tc.name)
			directorySnapshotEnsureEpoch(t, ctx, st, tenant)
			ref := store.DirectoryPrincipalRef{
				PrincipalKind: model.DirectoryPrincipalIdentity,
				PrincipalRef:  model.NewID(),
			}
			directorySnapshotInsertLocal(
				t, ctx, st, tenant, ref, 1, directorySnapshotAnchorExact,
			)
			if err := tc.shadow(ctx, raw, tenant); err != nil {
				t.Fatalf("install TEMP shadow: %v", err)
			}
			if err := st.View(ctx, tenant, func(scope store.Scope) error {
				return tc.read(ctx, scope, ref)
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func directorySnapshotExercise(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	engine store.Engine,
) {
	t.Helper()
	tenant := provisionTenant(t, st, "directory-reader-"+uniqueSuffix())
	otherTenant := provisionTenant(t, st, "directory-reader-other-"+uniqueSuffix())
	directorySnapshotEnsureEpoch(t, ctx, st, tenant)
	directorySnapshotEnsureEpoch(t, ctx, st, otherTenant)

	workspace := model.NewID()
	identity := store.DirectoryPrincipalRef{
		PrincipalKind: model.DirectoryPrincipalIdentity,
		PrincipalRef:  model.NewID(),
		WorkspaceRef:  workspace,
	}
	identityTombstone := directorySnapshotInsertLocal(
		t, ctx, st, tenant, identity, 1, directorySnapshotAnchorExact,
	)
	user := store.DirectoryPrincipalRef{
		PrincipalKind: model.DirectoryPrincipalUser,
		PrincipalRef:  model.NewID(),
	}
	userTombstone := directorySnapshotInsertUser(
		t,
		ctx,
		st,
		user,
		map[model.TenantID]int64{tenant: 1},
		directorySnapshotAnchorExact,
	)

	if err := st.View(ctx, tenant, func(scope store.Scope) error {
		reader := scope.(store.DirectorySnapshotReader)
		epoch, err := reader.ReadDirectoryEpoch(ctx)
		if err != nil {
			t.Fatalf("read valid epoch: %v", err)
		}
		if epoch.ID != model.ID(tenant) || epoch.TenantID != tenant || epoch.Version != 1 {
			t.Fatalf("epoch = %+v, want tenant-bound version one", epoch)
		}

		witness, found, err := reader.ReadDirectoryTombstone(ctx, identity)
		if err != nil || !found {
			t.Fatalf("read tenant tombstone: found=%v err=%v", found, err)
		}
		directorySnapshotWantWitness(
			t, witness, model.DirectoryTombstoneKind, identityTombstone, identity, 1,
		)

		witness, found, err = reader.ReadDirectoryTombstone(ctx, user)
		if err != nil || !found {
			t.Fatalf("read user tombstone: found=%v err=%v", found, err)
		}
		directorySnapshotWantWitness(
			t, witness, model.UserTombstoneKind, userTombstone, user, 1,
		)
		directorySnapshotWantBoundTenant(t, ctx, scope.(*tenantScope), engine, tenant)

		// A successful global rebind must not contaminate the next local read.
		witness, found, err = reader.ReadDirectoryTombstone(ctx, identity)
		if err != nil || !found || witness.TombstoneID != identityTombstone {
			t.Fatalf("tenant read after user rebind: witness=%+v found=%v err=%v",
				witness, found, err)
		}

		missing := store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalAgent,
			PrincipalRef:  model.NewID(),
		}
		witness, found, err = reader.ReadDirectoryTombstone(ctx, missing)
		if err != nil || found || witness != (store.DirectoryTombstoneWitness{}) {
			t.Fatalf("authoritative miss: witness=%+v found=%v err=%v", witness, found, err)
		}
		missingUser := store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalUser,
			PrincipalRef:  model.NewID(),
		}
		witness, found, err = reader.ReadDirectoryTombstone(ctx, missingUser)
		if err != nil || found || witness != (store.DirectoryTombstoneWitness{}) {
			t.Fatalf("authoritative user miss: witness=%+v found=%v err=%v",
				witness, found, err)
		}
		directorySnapshotWantBoundTenant(t, ctx, scope.(*tenantScope), engine, tenant)
		return nil
	}); err != nil {
		t.Fatalf("valid snapshot view: %v", err)
	}

	// A global tombstone that omits this tenant is incomplete evidence, not a
	// clean miss. The rebind must still be restored on that error path.
	if err := st.View(ctx, otherTenant, func(scope store.Scope) error {
		reader := scope.(store.DirectorySnapshotReader)
		_, found, err := reader.ReadDirectoryTombstone(ctx, user)
		if found || !errors.Is(err, store.ErrDirectoryUnavailable) {
			t.Fatalf("user map without tenant: found=%v err=%v", found, err)
		}
		directorySnapshotWantBoundTenant(t, ctx, scope.(*tenantScope), engine, otherTenant)
		if _, err := reader.ReadDirectoryEpoch(ctx); err != nil {
			t.Fatalf("epoch after failed global rebind: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("incomplete user evidence view: %v", err)
	}

	for _, tc := range []struct {
		name string
		mode directorySnapshotAnchorMode
	}{
		{"mismatched audit hash", directorySnapshotAnchorBadHash},
		{"mismatched audit sequence", directorySnapshotAnchorBadSequence},
		{"mismatched audit action", directorySnapshotAnchorBadAction},
		{"mismatched audit target", directorySnapshotAnchorBadTarget},
		{"missing audit event", directorySnapshotAnchorMissing},
	} {
		ref := store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalAgent,
			PrincipalRef:  model.NewID(),
		}
		directorySnapshotInsertLocal(t, ctx, st, tenant, ref, 1, tc.mode)
		directorySnapshotWantUnavailable(t, ctx, st, tenant, ref, tc.name)
	}

	futureRef := store.DirectoryPrincipalRef{
		PrincipalKind: model.DirectoryPrincipalIdentity,
		PrincipalRef:  model.NewID(),
	}
	directorySnapshotInsertLocal(
		t, ctx, st, tenant, futureRef, 2, directorySnapshotAnchorExact,
	)
	directorySnapshotWantUnavailable(t, ctx, st, tenant, futureRef, "future retirement epoch")

	badUser := store.DirectoryPrincipalRef{
		PrincipalKind: model.DirectoryPrincipalUser,
		PrincipalRef:  model.NewID(),
	}
	directorySnapshotInsertUser(
		t,
		ctx,
		st,
		badUser,
		map[model.TenantID]int64{tenant: 1},
		directorySnapshotAnchorBadHash,
	)
	if err := st.View(ctx, tenant, func(scope store.Scope) error {
		reader := scope.(store.DirectorySnapshotReader)
		_, found, err := reader.ReadDirectoryTombstone(ctx, badUser)
		if found || !errors.Is(err, store.ErrDirectoryUnavailable) {
			t.Fatalf("bad user audit anchor: found=%v err=%v", found, err)
		}
		directorySnapshotWantBoundTenant(t, ctx, scope.(*tenantScope), engine, tenant)
		_, found, err = reader.ReadDirectoryTombstone(ctx, identity)
		if err != nil || !found {
			t.Fatalf("local read after failed user rebind: found=%v err=%v", found, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("failed user rebind view: %v", err)
	}
}

func TestDirectorySnapshotReaderRejectsInvalidEpochBeforeNegativeLookup(t *testing.T) {
	for _, mutation := range []string{"absent", "version-zero", "duplicate"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			st, err := Open(ctx, store.Config{
				Engine:   store.EngineSQLite,
				DSN:      filepath.Join(t.TempDir(), "invalid-epoch.db"),
				MaxConns: 1,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			tenant := provisionTenant(t, st, "invalid-epoch-"+mutation)
			directorySnapshotEnsureEpoch(t, ctx, st, tenant)
			directorySnapshotCorruptEpoch(t, ctx, st, tenant, mutation)

			ref := store.DirectoryPrincipalRef{
				PrincipalKind: model.DirectoryPrincipalAgent,
				PrincipalRef:  model.NewID(),
			}
			directorySnapshotWantUnavailable(t, ctx, st, tenant, ref, mutation)
		})
	}
}

type directorySnapshotAnchorMode int

const (
	directorySnapshotAnchorExact directorySnapshotAnchorMode = iota
	directorySnapshotAnchorBadHash
	directorySnapshotAnchorBadSequence
	directorySnapshotAnchorBadAction
	directorySnapshotAnchorBadTarget
	directorySnapshotAnchorMissing
)

func directorySnapshotEnsureEpoch(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	tenant model.TenantID,
) {
	t.Helper()
	if err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		ts := scope.(*tenantScope)
		_, err := ts.repo(directoryEpochDescriptor).Get(ctx, model.ID(tenant))
		if err == nil {
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		_, err = ts.repo(directoryEpochDescriptor).CreateWithID(
			ctx, model.ID(tenant), model.Record{},
		)
		return err
	}); err != nil {
		t.Fatalf("ensure directory epoch for %s: %v", tenant, err)
	}
}

func directorySnapshotInsertLocal(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	tenant model.TenantID,
	ref store.DirectoryPrincipalRef,
	resultingEpoch int64,
	anchorMode directorySnapshotAnchorMode,
) model.ID {
	t.Helper()
	tombstoneID := model.NewID()
	if err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		ts := scope.(*tenantScope)
		now, err := ts.TransactionNow(ctx)
		if err != nil {
			return err
		}
		anchor, err := directorySnapshotTestAnchor(
			ctx,
			ts,
			model.AuditActionDirectoryPrincipalRetire,
			model.DirectoryTombstoneKind,
			tombstoneID,
			anchorMode,
		)
		if err != nil {
			return err
		}
		tombstone := model.DirectoryTombstone{
			BaseFields: model.BaseFields{
				ID: tombstoneID, TenantID: tenant, CreatedAt: now, UpdatedAt: now, Version: 1,
			},
			PrincipalKind:  ref.PrincipalKind,
			PrincipalRef:   ref.PrincipalRef,
			WorkspaceRef:   ref.WorkspaceRef,
			ResultingEpoch: resultingEpoch,
			Actor:          "system:directory-snapshot-test",
			RetiredAt:      now,
			AuditAnchor:    anchor,
		}
		switch ref.PrincipalKind {
		case model.DirectoryPrincipalIdentity:
			tombstone.SourceKind = "core.identity"
			tombstone.SourceID = ref.PrincipalRef
			tombstone.Cause = model.DirectoryCauseIdentityRetired
		case model.DirectoryPrincipalAgent:
			tombstone.SourceKind = "core.agent"
			tombstone.SourceID = model.NewID()
			tombstone.Cause = model.DirectoryCauseAgentRetired
		default:
			return errors.New("test fixture requires an identity or agent")
		}
		rec, err := directoryTombstoneCodec.Encode(tombstone)
		if err != nil {
			return err
		}
		_, err = ts.repo(directoryTombstoneDescriptor).CreateWithID(ctx, tombstoneID, rec)
		return err
	}); err != nil {
		t.Fatalf("insert local directory tombstone: %v", err)
	}
	return tombstoneID
}

func directorySnapshotInsertUser(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	ref store.DirectoryPrincipalRef,
	epochs map[model.TenantID]int64,
	anchorMode directorySnapshotAnchorMode,
) model.ID {
	t.Helper()
	tombstoneID := model.NewID()
	if err := st.AuthMutate(ctx, func(scope store.AuthScope) error {
		ts := scope.(*authScope).ts
		now, err := ts.TransactionNow(ctx)
		if err != nil {
			return err
		}
		anchor, err := directorySnapshotTestAnchor(
			ctx,
			ts,
			model.AuditActionUserRetire,
			model.UserTombstoneKind,
			tombstoneID,
			anchorMode,
		)
		if err != nil {
			return err
		}
		evidence, err := model.NewDirectoryEpochEvidence(epochs)
		if err != nil {
			return err
		}
		tombstone := model.UserTombstone{
			BaseFields: model.BaseFields{
				ID: tombstoneID, TenantID: model.SystemTenantID,
				CreatedAt: now, UpdatedAt: now, Version: 1,
			},
			PrincipalKind:   model.DirectoryPrincipalUser,
			PrincipalRef:    ref.PrincipalRef,
			SourceKind:      "core.user",
			SourceID:        ref.PrincipalRef,
			ResultingEpochs: evidence,
			Cause:           model.DirectoryCauseUserErased,
			Actor:           "system:directory-snapshot-test",
			RetiredAt:       now,
			AuditAnchor:     anchor,
		}
		rec, err := userTombstoneCodec.Encode(tombstone)
		if err != nil {
			return err
		}
		_, err = ts.repo(userTombstoneDescriptor).CreateWithID(ctx, tombstoneID, rec)
		return err
	}); err != nil {
		t.Fatalf("insert user directory tombstone: %v", err)
	}
	return tombstoneID
}

func directorySnapshotTestAnchor(
	ctx context.Context,
	ts *tenantScope,
	action string,
	targetKind model.Kind,
	targetID model.ID,
	mode directorySnapshotAnchorMode,
) (model.RetirementAuditAnchor, error) {
	if mode == directorySnapshotAnchorMissing {
		return model.RetirementAuditAnchor{
			EventID: model.NewID(), Seq: 999, Hash: make([]byte, 32),
			Action: action, TargetKind: targetKind, TargetID: targetID,
		}, nil
	}
	ledgerAction := action
	ledgerTargetKind := targetKind
	ledgerTargetID := targetID
	if mode == directorySnapshotAnchorBadAction {
		ledgerAction += ".corrupt"
	}
	if mode == directorySnapshotAnchorBadTarget {
		ledgerTargetKind = "core.agent"
		ledgerTargetID = model.NewID()
	}
	event, err := ts.Audit().Append(ctx, model.AuditDraft{
		Actor:      model.ActorSystem,
		ActorKind:  model.ActorSystem,
		Action:     ledgerAction,
		TargetKind: ledgerTargetKind,
		TargetID:   ledgerTargetID,
	})
	if err != nil {
		return model.RetirementAuditAnchor{}, err
	}
	hash := append([]byte(nil), event.Hash...)
	if mode == directorySnapshotAnchorBadHash {
		hash[0] ^= 0xff
	}
	seq := event.Seq
	if mode == directorySnapshotAnchorBadSequence {
		seq++
	}
	return model.RetirementAuditAnchor{
		EventID: event.ID, Seq: seq, Hash: hash,
		Action: action, TargetKind: targetKind, TargetID: targetID,
	}, nil
}

func directorySnapshotWantWitness(
	t *testing.T,
	got store.DirectoryTombstoneWitness,
	kind model.Kind,
	id model.ID,
	principal store.DirectoryPrincipalRef,
	epoch int64,
) {
	t.Helper()
	if got.TombstoneKind != kind || got.TombstoneID != id ||
		got.TombstoneVersion != 1 || got.Principal != principal ||
		got.RetirementEpoch != epoch {
		t.Fatalf("witness = %+v, want kind=%s id=%s principal=%+v epoch=%d",
			got, kind, id, principal, epoch)
	}
}

func directorySnapshotWantBoundTenant(
	t *testing.T,
	ctx context.Context,
	ts *tenantScope,
	engine store.Engine,
	want model.TenantID,
) {
	t.Helper()
	var got string
	query := "SELECT tenant_id FROM " + dialect.ScopeTenantTable
	if engine == store.EnginePostgres {
		query = "SELECT pg_catalog.current_setting('app.tenant_id')"
	}
	if err := ts.tx.QueryRowContext(ctx, query).Scan(&got); err != nil {
		t.Fatalf("read restored tenant binding: %v", err)
	}
	if got != want.String() {
		t.Fatalf("tenant binding = %q, want %q", got, want)
	}
}

func directorySnapshotWantUnavailable(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	tenant model.TenantID,
	ref store.DirectoryPrincipalRef,
	reason string,
) {
	t.Helper()
	if err := st.View(ctx, tenant, func(scope store.Scope) error {
		witness, found, err := scope.(store.DirectorySnapshotReader).
			ReadDirectoryTombstone(ctx, ref)
		if found || witness != (store.DirectoryTombstoneWitness{}) ||
			!errors.Is(err, store.ErrDirectoryUnavailable) {
			t.Fatalf("%s: witness=%+v found=%v err=%v",
				reason, witness, found, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("%s view: %v", reason, err)
	}
}

func directorySnapshotCorruptEpoch(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	tenant model.TenantID,
	mutation string,
) {
	t.Helper()
	if err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		ts := scope.(*tenantScope)
		switch mutation {
		case "absent":
			_, err := ts.tx.ExecContext(ctx,
				"DELETE FROM core_directory_epoch WHERE tenant_id = ?", tenant.String())
			return err
		case "version-zero":
			if _, err := ts.tx.ExecContext(ctx, "PRAGMA ignore_check_constraints = ON"); err != nil {
				return err
			}
			if _, err := ts.tx.ExecContext(ctx,
				"UPDATE core_directory_epoch SET version = 0 WHERE tenant_id = ?",
				tenant.String(),
			); err != nil {
				return err
			}
			_, err := ts.tx.ExecContext(ctx, "PRAGMA ignore_check_constraints = OFF")
			return err
		case "duplicate":
			if _, err := ts.tx.ExecContext(ctx,
				"DROP INDEX core_directory_epoch_tenant_uniq"); err != nil {
				return err
			}
			if _, err := ts.tx.ExecContext(ctx, "PRAGMA ignore_check_constraints = ON"); err != nil {
				return err
			}
			now := model.SystemClock{}.Now().String()
			_, err := ts.tx.ExecContext(ctx, `INSERT INTO core_directory_epoch
(id, tenant_id, created_at, updated_at, version) VALUES (?, ?, ?, ?, 1)`,
				model.NewID().String(), tenant.String(), now, now)
			if err != nil {
				return err
			}
			_, err = ts.tx.ExecContext(ctx, "PRAGMA ignore_check_constraints = OFF")
			return err
		default:
			return errors.New("unknown epoch mutation")
		}
	}); err != nil {
		t.Fatalf("corrupt epoch (%s): %v", mutation, err)
	}
}
