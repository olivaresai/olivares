// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestRowLockSQLContract(t *testing.T) {
	if got := rowLockSuffix(store.EnginePostgres); got != " FOR UPDATE" {
		t.Fatalf("PostgreSQL row lock suffix = %q, want FOR UPDATE", got)
	}
	if got := rowLockSuffix(store.EngineSQLite); got != "" {
		t.Fatalf("SQLite row lock suffix = %q, want empty (writer acquired by scope reservation)", got)
	}
	if got := authorityIdentityTableLockSQL(store.EnginePostgres); got !=
		"LOCK TABLE ONLY public.identities IN SHARE MODE" {
		t.Fatalf("PostgreSQL identity authority lock = %q", got)
	}
	if got := authorityIdentityTableLockSQL(store.EngineSQLite); got != "" {
		t.Fatalf("SQLite identity authority lock = %q, want implicit writer lock", got)
	}
}

func TestAuthorizationFactAllowlist(t *testing.T) {
	if err := validateAuthorizationFact(model.EntityDescriptor{
		Kind: "core.identity", AuthorizationFact: true, AuthorizationLockOrder: 1,
	}); err != nil {
		t.Fatalf("allowlisted identity fact: %v", err)
	}
	if err := validateAuthorizationFact(model.EntityDescriptor{
		Kind: "sessions.work_item", AuthorizationFact: true, AuthorizationLockOrder: 1,
	}); err == nil {
		t.Fatal("arbitrary module entity opted itself into authorization snapshots")
	}
	if err := validateAuthorizationFact(model.EntityDescriptor{
		Kind: "core.agent", AuthorizationLockOrder: 1,
	}); err == nil {
		t.Fatal("lock order without authorization fact marker was accepted")
	}
	claim := authorityLeaseFactDescriptor()
	if err := validateAuthorizationFact(claim); err != nil {
		t.Fatalf("allowlisted leased Claim fact: %v", err)
	}
	claim.AuthorizationFact = false
	if err := validateAuthorizationFact(claim); err == nil {
		t.Fatal("lease/fence touch without authorization fact opt-in was accepted")
	}
	claim = authorityLeaseFactDescriptor()
	claim.AuthorizationLeaseFence.DeadlineColumn = ""
	if err := validateAuthorizationFact(claim); err == nil {
		t.Fatal("incomplete lease/fence touch declaration was accepted")
	}
	claim = authorityLeaseFactDescriptor()
	claim.AuthorizationLeaseFence.StateColumn = claim.AuthorizationLeaseFence.SubjectColumn
	if err := validateAuthorizationFact(claim); err == nil {
		t.Fatal("aliased lease/fence semantic columns were accepted")
	}
}

const authorityLeaseFactKind model.Kind = "sessions.claim"

func authorityLeaseFactDescriptor() model.EntityDescriptor {
	return model.EntityDescriptor{
		Kind: authorityLeaseFactKind, Table: "sessions_claim",
		AuthorizationFact: true, AuthorizationLockOrder: 40,
		AuthorizationLeaseFence: model.AuthorizationLeaseFenceSpec{
			SubjectColumn: "sid", FenceColumn: "fence", StateColumn: "claim_state",
			ActiveValue: "active", DeadlineColumn: "lease_expires_at",
		},
		Fields: []model.FieldSpec{
			{Name: "sid", Kind: model.KindText},
			{Name: "holder", Kind: model.KindText},
			{Name: "fence", Kind: model.KindInt},
			{Name: "claim_state", Kind: model.KindText},
			{Name: "lease_expires_at", Kind: model.KindTimestamp},
		},
		Indexes: []model.IndexSpec{{
			Name:    "sessions_claim_sid_uniq",
			Columns: []string{model.ColTenantID, "sid"},
			Unique:  true,
		}},
	}
}

func registerAuthorityLeaseFact(reg store.ExtensionRegistry) error {
	return reg.Register(authorityLeaseFactDescriptor())
}

// TestSQLiteRowLockerFencesAnotherStoreInstance is the embedded-engine
// concurrency barrier. Two separately opened stores model two processes that
// can point at the same file. Lock must acquire SQLite's writer slot before it
// returns; a plain SELECT in a deferred transaction would let the second store
// disable this identity while the first caller still believed its authorization
// snapshot was stable.
func TestSQLiteRowLockerFencesAnotherStoreInstance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dsn := filepath.Join(t.TempDir(), "row-lock.sqlite")
	open := func() store.Store {
		st, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn, Debug: true}, nil)
		if err != nil {
			t.Fatalf("open SQLite row-lock store: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	}
	first := open()
	tenant := provisionTenant(t, first, "row-lock-sqlite")
	second := open()

	var identity model.Identity
	if err := first.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		identity, err = sc.Identities().Create(ctx, model.Identity{
			Name: "row-lock-agent", Kind: "service_account", ExternalID: "agent:row-lock",
			Metadata: map[string]any{"disabled": false},
		})
		return err
	}); err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	holderReady := make(chan struct{})
	releaseHolder := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- first.Mutate(ctx, tenant, func(sc store.Scope) error {
			locker, ok := sc.Identities().(store.RowLocker[model.Identity])
			if !ok {
				return store.ErrRowLockUnavailable
			}
			locked, err := locker.Lock(ctx, identity.ID)
			if err != nil {
				return err
			}
			if locked.Version != identity.Version {
				return errors.New("row lock changed the identity version")
			}
			close(holderReady)
			select {
			case <-releaseHolder:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-holderReady:
	case err := <-holderDone:
		t.Fatalf("row-lock holder ended early: %v", err)
	case <-ctx.Done():
		t.Fatalf("row-lock holder did not become ready: %v", ctx.Err())
	}

	updaterDone := make(chan error, 1)
	go func() {
		updaterDone <- second.Mutate(ctx, tenant, func(sc store.Scope) error {
			current, err := sc.Identities().Get(ctx, identity.ID)
			if err != nil {
				return err
			}
			current.Metadata = map[string]any{"disabled": true}
			_, err = sc.Identities().Update(ctx, current)
			return err
		})
	}()
	select {
	case err := <-updaterDone:
		close(releaseHolder)
		t.Fatalf("concurrent identity update crossed the row lock: %v", err)
	case <-time.After(200 * time.Millisecond):
		// Expected: the other store waits for the lock-holding transaction.
	}
	close(releaseHolder)
	if err := <-holderDone; err != nil {
		t.Fatalf("row-lock holder: %v", err)
	}
	select {
	case err := <-updaterDone:
		if err != nil {
			t.Fatalf("identity update after release: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("identity update stayed blocked after release: %v", ctx.Err())
	}

	if err := first.View(ctx, tenant, func(sc store.Scope) error {
		locker := sc.Identities().(store.RowLocker[model.Identity])
		_, err := locker.Lock(ctx, identity.ID)
		if !errors.Is(err, store.ErrReadOnly) {
			t.Fatalf("View row lock err = %v, want ErrReadOnly", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect View row lock: %v", err)
	}
}

const confinedRowLockKind model.Kind = "rowlock.confined_item"

func registerConfinedRowLockEntity(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  confinedRowLockKind,
		Table: "rowlock_confined_items",
		Fields: []model.FieldSpec{
			{Name: "workspace_id", Kind: model.KindUUID, Indexed: true},
			{Name: "label", Kind: model.KindText},
		},
		WorkspaceLineage: model.WorkspaceLineageSpec{
			Column:   "workspace_id",
			Encoding: model.WorkspaceLineageID,
			Unset:    model.WorkspaceUnsetHidden,
		},
	})
}

// TestWorkspaceConfinementPreservesRowLockWithoutEscape pins both halves of
// the capability decorator. A confined caller can still lock facts it may see,
// but presenting an ID from another workspace never turns Lock into a raw-DAO
// escape hatch. Identity visibility is indirect through a visible agent
// binding; the generic row uses its declared workspace lineage.
func TestWorkspaceConfinementPreservesRowLockWithoutEscape(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerConfinedRowLockEntity)
	tenant := provisionTenant(t, st, "confined-row-lock")

	type fixture struct {
		workspace model.Workspace
		identity  model.Identity
		agent     model.Agent
		item      model.Record
	}
	var a, b fixture
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		seed := func(name string) (fixture, error) {
			workspace, err := sc.Workspaces().Create(ctx, model.Workspace{
				Name: name, Slug: name, Status: model.StatusActive,
			})
			if err != nil {
				return fixture{}, err
			}
			identity, err := sc.Identities().Create(ctx, model.Identity{
				Name: name + "-identity", Kind: "service_account", ExternalID: "agent:" + name,
			})
			if err != nil {
				return fixture{}, err
			}
			agent, err := sc.Agents().Create(ctx, model.Agent{
				Name: name + "-agent", Kind: "service", Status: model.StatusActive,
				WorkspaceID: workspace.ID, IdentityID: identity.ID,
			})
			if err != nil {
				return fixture{}, err
			}
			repo, err := sc.Ext(confinedRowLockKind)
			if err != nil {
				return fixture{}, err
			}
			item, err := repo.Create(ctx, model.Record{
				"workspace_id": workspace.ID.String(), "label": name,
			})
			return fixture{workspace: workspace, identity: identity, agent: agent, item: item}, err
		}
		var err error
		if a, err = seed("rowlock-a"); err != nil {
			return err
		}
		b, err = seed("rowlock-b")
		return err
	}); err != nil {
		t.Fatalf("seed confined row-lock fixture: %v", err)
	}

	if err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, a.workspace.ID)
		if err != nil {
			return err
		}

		agents, ok := confined.Agents().(store.RowLocker[model.Agent])
		if !ok {
			t.Fatal("confined agent repo lost RowLocker")
		}
		if _, err := agents.Lock(ctx, a.agent.ID); err != nil {
			t.Errorf("lock visible agent: %v", err)
		}
		if _, err := agents.Lock(ctx, b.agent.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("lock foreign agent err = %v, want ErrNotFound", err)
		}

		identities, ok := confined.Identities().(store.RowLocker[model.Identity])
		if !ok {
			t.Fatal("confined identity repo lost RowLocker")
		}
		if _, err := identities.Lock(ctx, a.identity.ID); err != nil {
			t.Errorf("lock identity bound to visible agent: %v", err)
		}
		if _, err := identities.Lock(ctx, b.identity.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("lock foreign identity err = %v, want ErrNotFound", err)
		}

		ext, err := confined.Ext(confinedRowLockKind)
		if err != nil {
			return err
		}
		items, ok := ext.(store.RowLocker[model.Record])
		if !ok {
			t.Fatal("confined generic repo lost RowLocker")
		}
		if _, err := items.Lock(ctx, model.ID(a.item.String(model.ColID))); err != nil {
			t.Errorf("lock visible generic row: %v", err)
		}
		if _, err := items.Lock(ctx, model.ID(b.item.String(model.ColID))); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("lock foreign generic row err = %v, want ErrNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("exercise confined row locks: %v", err)
	}
}

func TestAuthoritySnapshotIsOpaqueVersionedAndDenyClosed(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerConfinedRowLockEntity)
	tenant := provisionTenant(t, st, "authority-snapshot")

	var identity model.Identity
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		identity, err = sc.Identities().Create(ctx, model.Identity{
			Name: "snapshot-agent", Kind: "service_account", ExternalID: "agent:snapshot",
		})
		return err
	}); err != nil {
		t.Fatalf("seed authority identity: %v", err)
	}
	valid := []store.AuthorizationFactRef{{
		Kind: "core.identity", ID: identity.ID, Version: identity.Version,
	}}

	if err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		workspace, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		confined, err := store.ConfineWorkspace(ctx, raw, workspace.ID)
		if err != nil {
			return err
		}
		locker, ok := confined.(store.AuthoritySnapshotLocker)
		if !ok {
			t.Fatal("workspace confinement lost AuthoritySnapshotLocker")
		}
		return locker.LockAuthoritySnapshot(ctx, valid)
	}); err != nil {
		t.Fatalf("lock valid opaque snapshot through confinement: %v", err)
	}

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		locker := sc.(store.AuthoritySnapshotLocker)
		stale := append([]store.AuthorizationFactRef(nil), valid...)
		stale[0].Version++
		if err := locker.LockAuthoritySnapshot(ctx, stale); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale authority version err = %v, want ErrConflict", err)
		}
		foreignKind := []store.AuthorizationFactRef{{
			Kind: confinedRowLockKind, ID: identity.ID, Version: identity.Version,
		}}
		if err := locker.LockAuthoritySnapshot(ctx, foreignKind); err == nil {
			t.Fatal("non-allowlisted entity entered authority snapshot")
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect authority refusal directions: %v", err)
	}

	// A newly inserted row with the same ExternalID changes which human/NHI the
	// reference could mean even though the original row's version did not move.
	// SQLite's writer lock prevents a concurrent phantom; this committed fixture
	// pins the required post-lock exact-count revalidation itself.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Identities().Create(ctx, model.Identity{
			Name: "snapshot-duplicate", Kind: "service_account", ExternalID: identity.ExternalID,
		})
		return err
	}); err != nil {
		t.Fatalf("seed ambiguous identity: %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		err := sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx, valid)
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("ambiguous ExternalID snapshot err = %v, want ErrConflict", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect ambiguous authority: %v", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		err := sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx, valid)
		if !errors.Is(err, store.ErrReadOnly) {
			t.Fatalf("View authority lock err = %v, want ErrReadOnly", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect View authority lock: %v", err)
	}
}

func authorityLeaseFactRef(t *testing.T, row model.Record) store.AuthorizationFactRef {
	t.Helper()
	deadline, err := model.ParseTimestamp(row.String("lease_expires_at"))
	if err != nil {
		t.Fatalf("parse leased authority deadline: %v", err)
	}
	ref, err := store.NewLeaseFenceAuthorizationFactRef(
		authorityLeaseFactKind,
		model.ID(row.String(model.ColID)),
		row.Int(model.ColVersion),
		row.String("sid"),
		row.Int("fence"),
		deadline,
	)
	if err != nil {
		t.Fatalf("build leased authority ref: %v", err)
	}
	return ref
}

func TestAuthoritySnapshotLeaseFenceTouchIsPayloadFreeAndDenyClosed(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerAuthorityLeaseFact)
	tenant := provisionTenant(t, st, "authority-lease-touch")
	deadline := model.NewTimestamp(time.Now().UTC().Add(time.Hour))

	var identity model.Identity
	var claim model.Record
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		identity, err = sc.Identities().Create(ctx, model.Identity{
			Name: "lease authority", Kind: "user", ExternalID: "human:lease-authority",
		})
		if err != nil {
			return err
		}
		repo, err := sc.Ext(authorityLeaseFactKind)
		if err != nil {
			return err
		}
		claim, err = repo.Create(ctx, model.Record{
			"sid": "ses_claim_touch", "holder": "holder-a", "fence": int64(7),
			"claim_state": "active", "lease_expires_at": deadline.String(),
		})
		return err
	}); err != nil {
		t.Fatalf("seed leased authority fixture: %v", err)
	}

	claimRef := authorityLeaseFactRef(t, claim)
	encoded, err := json.Marshal(claimRef)
	if err != nil {
		t.Fatalf("marshal opaque leased authority ref: %v", err)
	}
	if strings.Contains(string(encoded), "ses_claim_touch") ||
		strings.Contains(string(encoded), deadline.String()) ||
		strings.Contains(string(encoded), "leaseFence") {
		t.Fatalf("serialized authority ref exposed leased row witness: %s", encoded)
	}

	if err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		workspace, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		confined, err := store.ConfineWorkspace(ctx, raw, workspace.ID)
		if err != nil {
			return err
		}
		if _, err := confined.Ext(authorityLeaseFactKind); !errors.Is(
			err, store.ErrWorkspaceLineageRequired,
		) {
			t.Fatalf("confined Claim repo error = %v, want ErrWorkspaceLineageRequired", err)
		}
		return confined.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(
			ctx,
			[]store.AuthorizationFactRef{
				claimRef,
				{Kind: "core.identity", ID: identity.ID, Version: identity.Version},
			},
		)
	}); err != nil {
		t.Fatalf("touch leased authority through confinement: %v", err)
	}

	readClaim := func() model.Record {
		t.Helper()
		var got model.Record
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(authorityLeaseFactKind)
			if err != nil {
				return err
			}
			got, err = repo.Get(ctx, model.ID(claim.String(model.ColID)))
			return err
		}); err != nil {
			t.Fatalf("read leased authority row: %v", err)
		}
		return got
	}
	touched := readClaim()
	if touched.Int(model.ColVersion) != claim.Int(model.ColVersion)+1 ||
		touched.String("sid") != claim.String("sid") ||
		touched.Int("fence") != claim.Int("fence") ||
		touched.String("lease_expires_at") != claim.String("lease_expires_at") {
		t.Fatalf("leased authority touch changed semantic payload: before=%v after=%v", claim, touched)
	}

	assertConflictWithoutTouch := func(name string, ref store.AuthorizationFactRef) {
		t.Helper()
		before := readClaim().Int(model.ColVersion)
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			err := sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(
				ctx, []store.AuthorizationFactRef{ref},
			)
			if !errors.Is(err, store.ErrConflict) {
				t.Fatalf("%s error = %v, want ErrConflict", name, err)
			}
			return nil
		}); err != nil {
			t.Fatalf("%s inspection transaction: %v", name, err)
		}
		if after := readClaim().Int(model.ColVersion); after != before {
			t.Fatalf("%s changed Claim version = %d, want %d", name, after, before)
		}
	}
	assertConflictWithoutTouch("stale version", claimRef)

	current := readClaim()
	wrongFence, err := store.NewLeaseFenceAuthorizationFactRef(
		authorityLeaseFactKind,
		model.ID(current.String(model.ColID)),
		current.Int(model.ColVersion),
		current.String("sid"),
		current.Int("fence")+1,
		deadline,
	)
	if err != nil {
		t.Fatalf("build wrong-fence fact: %v", err)
	}
	assertConflictWithoutTouch("wrong fence", wrongFence)
	wrongDeadline, err := store.NewLeaseFenceAuthorizationFactRef(
		authorityLeaseFactKind,
		model.ID(current.String(model.ColID)),
		current.Int(model.ColVersion),
		current.String("sid"),
		current.Int("fence"),
		model.NewTimestamp(deadline.Time().Add(time.Minute)),
	)
	if err != nil {
		t.Fatalf("build wrong-deadline fact: %v", err)
	}
	assertConflictWithoutTouch("wrong deadline", wrongDeadline)
	missing, err := store.NewLeaseFenceAuthorizationFactRef(
		authorityLeaseFactKind, model.NewID(), current.Int(model.ColVersion),
		current.String("sid"), current.Int("fence"), deadline,
	)
	if err != nil {
		t.Fatalf("build missing-row fact: %v", err)
	}
	assertConflictWithoutTouch("missing row", missing)

	rollbackCause := errors.New("rollback after leased authority touch")
	currentRef := authorityLeaseFactRef(t, current)
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(
			ctx, []store.AuthorizationFactRef{currentRef},
		); err != nil {
			return err
		}
		return rollbackCause
	}); !errors.Is(err, rollbackCause) {
		t.Fatalf("rollback touch error = %v, want cause", err)
	}
	if after := readClaim().Int(model.ColVersion); after != current.Int(model.ColVersion) {
		t.Fatalf("rolled-back OCC touch version = %d, want %d",
			after, current.Int(model.ColVersion))
	}

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(authorityLeaseFactKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(ctx, model.ID(current.String(model.ColID)))
		if err != nil {
			return err
		}
		row["claim_state"] = "released"
		_, err = repo.Update(ctx, row)
		return err
	}); err != nil {
		t.Fatalf("release leased authority fixture: %v", err)
	}
	released := readClaim()
	assertConflictWithoutTouch("released state", authorityLeaseFactRef(t, released))

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(authorityLeaseFactKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(ctx, model.ID(current.String(model.ColID)))
		if err != nil {
			return err
		}
		row["claim_state"] = "active"
		row["lease_expires_at"] = model.NewTimestamp(time.Now().UTC().Add(-time.Minute)).String()
		_, err = repo.Update(ctx, row)
		return err
	}); err != nil {
		t.Fatalf("expire leased authority fixture: %v", err)
	}
	expired := readClaim()
	assertConflictWithoutTouch("expired deadline", authorityLeaseFactRef(t, expired))

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		err := sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{{
			Kind: authorityLeaseFactKind,
			ID:   model.ID(current.String(model.ColID)), Version: current.Int(model.ColVersion),
		}})
		if err == nil {
			t.Fatal("leased authority descriptor accepted a version-only ref")
		}
		identityWitness, witnessErr := store.NewLeaseFenceAuthorizationFactRef(
			"core.identity", identity.ID, identity.Version,
			"ses_claim_touch", 7, deadline,
		)
		if witnessErr != nil {
			return witnessErr
		}
		if err := sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(
			ctx, []store.AuthorizationFactRef{identityWitness},
		); err == nil {
			t.Fatal("ordinary authority descriptor accepted a lease/fence witness")
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect witness/descriptor mismatch: %v", err)
	}
}

func exerciseAuthorityLeaseFenceK2K3Order(
	t *testing.T,
	st store.Store,
	name string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tenant := provisionTenant(t, st, "authority-order-"+name)
	deadline := model.NewTimestamp(time.Now().UTC().Add(time.Hour))
	var identity model.Identity
	var claim model.Record
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		identity, err = sc.Identities().Create(ctx, model.Identity{
			Name: "K2 K3 authority order", Kind: "user",
			ExternalID: "human:authority-order:" + name,
		})
		if err != nil {
			return err
		}
		repo, err := sc.Ext(authorityLeaseFactKind)
		if err != nil {
			return err
		}
		claim, err = repo.Create(ctx, model.Record{
			"sid": "ses_k2_k3_order", "holder": "holder-order", "fence": int64(11),
			"claim_state": "active", "lease_expires_at": deadline.String(),
		})
		return err
	}); err != nil {
		t.Fatalf("seed %s authority order fixture: %v", name, err)
	}

	readClaim := func() model.Record {
		t.Helper()
		var row model.Record
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(authorityLeaseFactKind)
			if err != nil {
				return err
			}
			row, err = repo.Get(ctx, model.ID(claim.String(model.ColID)))
			return err
		}); err != nil {
			t.Fatalf("read %s authority-order Claim: %v", name, err)
		}
		return row
	}
	identityRef := store.AuthorizationFactRef{
		Kind: "core.identity", ID: identity.ID, Version: identity.Version,
	}
	k2 := func(held chan<- struct{}, release <-chan struct{}, rollback error) error {
		return st.Mutate(ctx, tenant, func(sc store.Scope) error {
			if err := sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(
				ctx, []store.AuthorizationFactRef{identityRef},
			); err != nil {
				return err
			}
			repo, err := sc.Ext(authorityLeaseFactKind)
			if err != nil {
				return err
			}
			row, err := repo.Get(ctx, model.ID(claim.String(model.ColID)))
			if err != nil {
				return err
			}
			if _, err := repo.Update(ctx, row); err != nil {
				return err
			}
			close(held)
			select {
			case <-release:
				return rollback
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}
	k3 := func(
		claimRef store.AuthorizationFactRef,
		held chan<- struct{},
		release <-chan struct{},
		rollback error,
	) error {
		return st.Mutate(ctx, tenant, func(sc store.Scope) error {
			// Deliberately present Claim first. The locker must impose the global
			// authority order Identity(10) -> Claim(40), independent of input.
			if err := sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(
				ctx, []store.AuthorizationFactRef{claimRef, identityRef},
			); err != nil {
				return err
			}
			close(held)
			select {
			case <-release:
				return rollback
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}

	assertBlocked := func(done <-chan error, label string) {
		t.Helper()
		select {
		case err := <-done:
			t.Fatalf("%s crossed the held authority order: %v", label, err)
		case <-time.After(150 * time.Millisecond):
		}
	}
	waitDone := func(done <-chan error, want error, label string) {
		t.Helper()
		select {
		case err := <-done:
			if want == nil && err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			if want != nil && !errors.Is(err, want) {
				t.Fatalf("%s = %v, want %v", label, err, want)
			}
		case <-ctx.Done():
			t.Fatalf("%s did not finish: %v", label, ctx.Err())
		}
	}

	initialVersion := claim.Int(model.ColVersion)
	initialClaimRef := authorityLeaseFactRef(t, claim)
	k2Held, releaseK2 := make(chan struct{}), make(chan struct{})
	k2Rollback := errors.New("rollback K2 ordered touch")
	k2Done := make(chan error, 1)
	go func() { k2Done <- k2(k2Held, releaseK2, k2Rollback) }()
	select {
	case <-k2Held:
	case <-ctx.Done():
		t.Fatalf("K2 did not acquire Identity then Claim: %v", ctx.Err())
	}
	k3Held, releaseK3 := make(chan struct{}), make(chan struct{})
	k3Done := make(chan error, 1)
	go func() {
		k3Done <- k3(initialClaimRef, k3Held, releaseK3, nil)
	}()
	assertBlocked(k3Done, "K3 behind K2")
	close(releaseK2)
	waitDone(k2Done, k2Rollback, "K2 rollback")
	select {
	case <-k3Held:
		close(releaseK3)
	case <-ctx.Done():
		t.Fatalf("K3 did not acquire after K2 rollback: %v", ctx.Err())
	}
	waitDone(k3Done, nil, "K3 after K2 rollback")
	afterK3 := readClaim()
	if afterK3.Int(model.ColVersion) != initialVersion+1 {
		t.Fatalf("K2 rollback/K3 commit Claim version = %d, want %d",
			afterK3.Int(model.ColVersion), initialVersion+1)
	}

	k3Held, releaseK3 = make(chan struct{}), make(chan struct{})
	k3Rollback := errors.New("rollback K3 ordered touch")
	afterK3Ref := authorityLeaseFactRef(t, afterK3)
	k3Done = make(chan error, 1)
	go func() {
		k3Done <- k3(afterK3Ref, k3Held, releaseK3, k3Rollback)
	}()
	select {
	case <-k3Held:
	case <-ctx.Done():
		t.Fatalf("K3 did not acquire Identity then Claim: %v", ctx.Err())
	}
	k2Held, releaseK2 = make(chan struct{}), make(chan struct{})
	k2Done = make(chan error, 1)
	go func() { k2Done <- k2(k2Held, releaseK2, nil) }()
	assertBlocked(k2Done, "K2 behind K3")
	close(releaseK3)
	waitDone(k3Done, k3Rollback, "K3 rollback")
	select {
	case <-k2Held:
		close(releaseK2)
	case <-ctx.Done():
		t.Fatalf("K2 did not acquire after K3 rollback: %v", ctx.Err())
	}
	waitDone(k2Done, nil, "K2 after K3 rollback")
	afterK2 := readClaim()
	if afterK2.Int(model.ColVersion) != afterK3.Int(model.ColVersion)+1 {
		t.Fatalf("K3 rollback/K2 commit Claim version = %d, want %d",
			afterK2.Int(model.ColVersion), afterK3.Int(model.ColVersion)+1)
	}
}

func TestSQLiteAuthorityLeaseFenceOrderMatchesK2Touch(t *testing.T) {
	st := openSQLiteTest(t, registerAuthorityLeaseFact)
	exerciseAuthorityLeaseFenceK2K3Order(t, st, "sqlite")
}

func TestPostgresAuthorityLeaseFenceOrderMatchesK2Touch(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: isolatedPG(t).App, MaxConns: 4,
	}, registerAuthorityLeaseFact)
	if err != nil {
		t.Fatalf("open PostgreSQL leased authority store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	exerciseAuthorityLeaseFenceK2K3Order(t, st, "postgres")
}

type clockOnlyScope struct {
	store.Scope
	clock store.TransactionClock
}

func (s clockOnlyScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

type lockerOnlyScope struct {
	store.Scope
	locker store.TransactionLocker
}

func (s lockerOnlyScope) LockTransaction(ctx context.Context, key string) error {
	return s.locker.LockTransaction(ctx, key)
}

type authorityOnlyScope struct {
	store.Scope
	authority store.AuthoritySnapshotLocker
}

func (s authorityOnlyScope) LockAuthoritySnapshot(
	ctx context.Context,
	refs []store.AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

type clockLockerOnlyScope struct {
	store.Scope
	clock  store.TransactionClock
	locker store.TransactionLocker
}

func (s clockLockerOnlyScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s clockLockerOnlyScope) LockTransaction(ctx context.Context, key string) error {
	return s.locker.LockTransaction(ctx, key)
}

type clockAuthorityOnlyScope struct {
	store.Scope
	clock     store.TransactionClock
	authority store.AuthoritySnapshotLocker
}

func (s clockAuthorityOnlyScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s clockAuthorityOnlyScope) LockAuthoritySnapshot(
	ctx context.Context,
	refs []store.AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

type lockerAuthorityOnlyScope struct {
	store.Scope
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
}

func (s lockerAuthorityOnlyScope) LockTransaction(ctx context.Context, key string) error {
	return s.locker.LockTransaction(ctx, key)
}

func (s lockerAuthorityOnlyScope) LockAuthoritySnapshot(
	ctx context.Context,
	refs []store.AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

// TestSQLiteTransactionLockerMutateAndView pins both directions of SQLite's
// implementation: its single-writer Mutate scope needs no additional lock, but
// a View scope must not pretend to acquire write authority.
func TestSQLiteTransactionLockerMutateAndView(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "transaction-locker-sqlite")

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		locker, ok := sc.(store.TransactionLocker)
		if !ok {
			t.Fatal("SQLite Mutate scope does not expose store.TransactionLocker")
		}
		if err := locker.LockTransaction(ctx, "sessions.work_lease:test"); err != nil {
			return err
		}
		if err := locker.LockTransaction(ctx, ""); err == nil {
			t.Error("empty transaction lock key was accepted")
		}
		return nil
	}); err != nil {
		t.Fatalf("lock SQLite Mutate transaction: %v", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		locker, ok := sc.(store.TransactionLocker)
		if !ok {
			t.Fatal("SQLite View scope does not expose store.TransactionLocker")
		}
		if err := locker.LockTransaction(ctx, "sessions.work_lease:test"); !errors.Is(err, store.ErrReadOnly) {
			t.Fatalf("View LockTransaction err = %v, want ErrReadOnly", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect SQLite View transaction lock: %v", err)
	}
}

// TestTransactionCapabilitiesSurviveWorkspaceConfinement is the eight-cell
// optional-capability matrix. Each wrapper masks precisely the capability its
// name omits, so this detects both losing a capability and manufacturing one.
func TestTransactionCapabilitiesSurviveWorkspaceConfinement(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "confined-transaction-capabilities")

	if err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		workspace, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		clock, clockOK := raw.(store.TransactionClock)
		locker, lockerOK := raw.(store.TransactionLocker)
		authority, authorityOK := raw.(store.AuthoritySnapshotLocker)
		if !clockOK || !lockerOK || !authorityOK {
			t.Fatalf("raw SQL capabilities clock=%v locker=%v authority=%v, want all",
				clockOK, lockerOK, authorityOK)
		}

		cases := []struct {
			name          string
			raw           store.Scope
			wantClock     bool
			wantLocker    bool
			wantAuthority bool
		}{
			{name: "neither", raw: struct{ store.Scope }{Scope: raw}},
			{name: "clock only", raw: clockOnlyScope{Scope: raw, clock: clock}, wantClock: true},
			{name: "locker only", raw: lockerOnlyScope{Scope: raw, locker: locker}, wantLocker: true},
			{name: "authority only", raw: authorityOnlyScope{Scope: raw, authority: authority}, wantAuthority: true},
			{name: "clock and locker", raw: clockLockerOnlyScope{
				Scope: raw, clock: clock, locker: locker,
			}, wantClock: true, wantLocker: true},
			{name: "clock and authority", raw: clockAuthorityOnlyScope{
				Scope: raw, clock: clock, authority: authority,
			}, wantClock: true, wantAuthority: true},
			{name: "locker and authority", raw: lockerAuthorityOnlyScope{
				Scope: raw, locker: locker, authority: authority,
			}, wantLocker: true, wantAuthority: true},
			{name: "all", raw: raw, wantClock: true, wantLocker: true, wantAuthority: true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				confined, confineErr := store.ConfineWorkspace(ctx, tc.raw, workspace.ID)
				if confineErr != nil {
					t.Fatalf("confine: %v", confineErr)
				}
				confinedClock, gotClock := confined.(store.TransactionClock)
				confinedLocker, gotLocker := confined.(store.TransactionLocker)
				_, gotAuthority := confined.(store.AuthoritySnapshotLocker)
				if gotClock != tc.wantClock || gotLocker != tc.wantLocker ||
					gotAuthority != tc.wantAuthority {
					t.Fatalf("capabilities clock=%v locker=%v authority=%v, want clock=%v locker=%v authority=%v",
						gotClock, gotLocker, gotAuthority,
						tc.wantClock, tc.wantLocker, tc.wantAuthority)
				}
				if gotClock {
					if _, clockErr := confinedClock.TransactionNow(ctx); clockErr != nil {
						t.Errorf("forwarded TransactionNow: %v", clockErr)
					}
				}
				if gotLocker {
					if lockErr := confinedLocker.LockTransaction(ctx, "sessions.work_lease:"+tc.name); lockErr != nil {
						t.Errorf("forwarded LockTransaction: %v", lockErr)
					}
				}
			})
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect confined transaction capabilities: %v", err)
	}
}

// TestConfinedViewTransactionLockerDeniesWrite proves that the combined
// clock+locker+authority adapter forwards both write-capability refusals instead
// of laundering a View scope into apparent write authority.
func TestConfinedViewTransactionLockerDeniesWrite(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "confined-view-transaction-locker")

	if err := st.View(ctx, tenant, func(raw store.Scope) error {
		workspace, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		confined, err := store.ConfineWorkspace(ctx, raw, workspace.ID)
		if err != nil {
			return err
		}
		locker, ok := confined.(store.TransactionLocker)
		if !ok {
			t.Fatal("confined SQL scope lost TransactionLocker")
		}
		if err := locker.LockTransaction(ctx, "sessions.work_lease:view"); !errors.Is(err, store.ErrReadOnly) {
			t.Fatalf("confined View lock err = %v, want ErrReadOnly", err)
		}
		authority, ok := confined.(store.AuthoritySnapshotLocker)
		if !ok {
			t.Fatal("confined SQL scope lost AuthoritySnapshotLocker")
		}
		err = authority.LockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{{
			Kind: "core.identity", ID: model.NewID(), Version: 1,
		}})
		if !errors.Is(err, store.ErrReadOnly) {
			t.Fatalf("confined View authority lock err = %v, want ErrReadOnly", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect confined View transaction lock: %v", err)
	}
}

// TestPostgresTransactionLockerSerializesMatchingKeys exercises the production
// advisory lock. A distinct key must proceed while the holder is open; the same
// key must remain blocked until that transaction ends.
func TestPostgresTransactionLockerSerializesMatchingKeys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: isolatedPG(t).App, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("open PostgreSQL store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tenant := provisionTenant(t, st, "transaction-locker-postgres")

	const heldKey = "sessions.work_lease:tenant:workspace:item"
	holderReady := make(chan struct{})
	releaseHolder := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHolder) }) }
	t.Cleanup(release)
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- st.Mutate(ctx, tenant, func(sc store.Scope) error {
			locker, ok := sc.(store.TransactionLocker)
			if !ok {
				return errors.New("postgresql mutate scope lacks TransactionLocker")
			}
			if err := locker.LockTransaction(ctx, heldKey); err != nil {
				return err
			}
			close(holderReady)
			select {
			case <-releaseHolder:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-holderReady:
	case err := <-holderDone:
		t.Fatalf("holder ended before acquiring lock: %v", err)
	case <-ctx.Done():
		t.Fatalf("holder did not acquire lock: %v", ctx.Err())
	}

	distinctDone := make(chan error, 1)
	go func() {
		distinctDone <- st.Mutate(ctx, tenant, func(sc store.Scope) error {
			return sc.(store.TransactionLocker).LockTransaction(ctx, heldKey+":other")
		})
	}()
	select {
	case err := <-distinctDone:
		if err != nil {
			t.Fatalf("distinct-key lock: %v", err)
		}
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("distinct transaction lock key was blocked by the holder")
	}

	matchingAcquired := make(chan struct{})
	matchingDone := make(chan error, 1)
	go func() {
		matchingDone <- st.Mutate(ctx, tenant, func(sc store.Scope) error {
			if err := sc.(store.TransactionLocker).LockTransaction(ctx, heldKey); err != nil {
				return err
			}
			close(matchingAcquired)
			return nil
		})
	}()
	select {
	case <-matchingAcquired:
		release()
		t.Fatal("matching transaction lock acquired before the holder ended")
	case err := <-matchingDone:
		release()
		t.Fatalf("matching transaction ended before holder release: %v", err)
	case <-time.After(200 * time.Millisecond):
		// Expected: the matching key remains blocked while the first transaction is open.
	}

	release()
	if err := <-holderDone; err != nil {
		t.Fatalf("holder transaction: %v", err)
	}
	select {
	case <-matchingAcquired:
	case <-ctx.Done():
		t.Fatalf("matching transaction stayed blocked after release: %v", ctx.Err())
	}
	select {
	case err := <-matchingDone:
		if err != nil {
			t.Fatalf("matching transaction: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("matching transaction did not commit after release: %v", ctx.Err())
	}
}

// TestPostgresAuthoritySnapshotBlocksIdentityPhantoms is the live-engine half
// of the sponsor ambiguity fence. SELECT FOR UPDATE cannot block an INSERT whose
// row did not exist yet under READ COMMITTED, so the snapshot takes SHARE on the
// allowlisted identities table before it re-counts ExternalID. A duplicate
// insert must wait until the authority transaction ends; afterwards the same
// snapshot is stale because the reference has become ambiguous.
func TestPostgresAuthoritySnapshotBlocksIdentityPhantoms(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: isolatedPG(t).App, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("open PostgreSQL authority store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tenant := provisionTenant(t, st, "authority-phantom-postgres")

	var identity model.Identity
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		identity, err = sc.Identities().Create(ctx, model.Identity{
			Name: "authority sponsor", Kind: "user", ExternalID: "human:authority-phantom",
		})
		return err
	}); err != nil {
		t.Fatalf("seed PostgreSQL authority identity: %v", err)
	}
	snapshot := []store.AuthorizationFactRef{{
		Kind: "core.identity", ID: identity.ID, Version: identity.Version,
	}}

	holderReady := make(chan struct{})
	releaseHolder := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHolder) }) }
	t.Cleanup(release)
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- st.Mutate(ctx, tenant, func(sc store.Scope) error {
			if err := sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx, snapshot); err != nil {
				return err
			}
			close(holderReady)
			select {
			case <-releaseHolder:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-holderReady:
	case err := <-holderDone:
		t.Fatalf("PostgreSQL authority holder ended early: %v", err)
	case <-ctx.Done():
		t.Fatalf("PostgreSQL authority holder did not become ready: %v", ctx.Err())
	}

	inserterDone := make(chan error, 1)
	go func() {
		inserterDone <- st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, err := sc.Identities().Create(ctx, model.Identity{
				Name: "authority duplicate", Kind: "user", ExternalID: identity.ExternalID,
			})
			return err
		})
	}()
	select {
	case err := <-inserterDone:
		release()
		t.Fatalf("identity phantom crossed PostgreSQL authority lock: %v", err)
	case <-time.After(200 * time.Millisecond):
		// Expected: INSERT's RowExclusive waits on the authority SHARE lock.
	}
	release()
	if err := <-holderDone; err != nil {
		t.Fatalf("PostgreSQL authority holder: %v", err)
	}
	select {
	case err := <-inserterDone:
		if err != nil {
			t.Fatalf("identity phantom insert after release: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("identity phantom stayed blocked after release: %v", ctx.Err())
	}

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		err := sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx, snapshot)
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("ambiguous PostgreSQL authority snapshot err = %v, want ErrConflict", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect PostgreSQL ambiguous authority: %v", err)
	}
}
