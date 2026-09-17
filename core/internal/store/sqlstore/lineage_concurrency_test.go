// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"testing"
	"time"
)

func TestLineageConcurrentDifferentRelationsPostgres(t *testing.T) {
	st := openLineagePG(t)
	// Two tenant provisions and two fact snapshots are FIXTURE, not behaviour. Charged to
	// the 10 s budget they leave the handoff below with whatever is left of it, and under
	// -race on a contended runner that is how a passing contract reports `context deadline
	// exceeded` (01f81b8e81 / 4859cc43f3 / 346bce0c8a: same class, same remedy).
	a := provisionTenant(t, st, "concurrent-a")
	b := provisionTenant(t, st, "concurrent-b")
	ba := lineageTestFacts(t, st, a)
	bb := lineageTestFacts(t, st, b)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wrote := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- st.Mutate(ctx, a, func(sc store.Scope) error {
			_, e := sc.Sessions().Create(ctx, model.Session{ExternalID: "concurrent"})
			close(wrote)
			<-release
			return e
		})
	}()
	<-wrote
	fast, stop := context.WithTimeout(ctx, 500*time.Millisecond)
	err := st.Mutate(fast, b, func(sc store.Scope) error {
		_, e := sc.Resources().Create(fast, model.Resource{Name: "concurrent", Kind: "folder"})
		return e
	})
	stop()
	close(release)
	first := <-done
	if err != nil || first != nil {
		t.Fatalf("distinct tenant source DML blocked: %v / %v", first, err)
	}
	for k, v := range lineageTestFacts(t, st, a) {
		delta := int64(0)
		if k == "core.session" {
			delta = 1
		}
		if v.Version != ba[k].Version+delta {
			t.Fatal("A epoch crossed relation")
		}
	}
	for k, v := range lineageTestFacts(t, st, b) {
		delta := int64(0)
		if k == "core.resource" {
			delta = 1
		}
		if v.Version != bb[k].Version+delta {
			t.Fatal("B epoch crossed relation")
		}
	}
}

func TestLineageClaimDropInterleavingPostgres(t *testing.T) {
	for _, finalize := range []bool{false, true} {
		t.Run(fmt.Sprintf("finalize_%t", finalize), func(t *testing.T) {
			st := openLineagePG(t)
			// The tenant provision and the PEP service / claim seed are fixture; the 10 s
			// budget below is for the claim-drop interleaving alone. Started above them it
			// was spent by the fixture under -race on a contended runner, and the test then
			// failed at the wait instead of at the lock order (01f81b8e81 / 4859cc43f3 /
			// 346bce0c8a).
			setupCtx := context.Background()
			tenant := provisionTenant(t, st, "claim-drop")
			var service model.PEPService
			var claim model.PDPDecisionClaim
			if e := st.AuthMutate(setupCtx, func(as store.AuthScope) error {
				var e error
				service, e = as.PEPServices().Create(setupCtx, model.PEPService{Name: "claim-drop", TargetTenantID: tenant})
				if e != nil {
					return e
				}
				if finalize {
					claim, _, e = as.ClaimDecision(setupCtx, model.PDPDecisionClaim{TargetTenantID: tenant, HandleJTI: model.NewID(), PEPServiceID: service.ID, NonceHash: "nonce", RequestFingerprint: "fingerprint", RequestIssuedAt: model.NewTimestamp(time.Now())}, nil)
				}
				return e
			}); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			entered := make(chan error, 1)
			release := make(chan struct{})
			claimDone := make(chan error, 1)
			decisionClaimBeforeDMLTestHook = func(ctx context.Context, tx *sql.Tx) error {
				var global, audit bool
				e := tx.QueryRowContext(ctx, "SELECT "+lineageHeldLockSQL("'"+directoryWriterLockKey+"'", "ExclusiveLock")+", "+lineageHeldLockSQL("'"+model.SystemTenantID.String()+"'", "ExclusiveLock")).Scan(&global, &audit)
				if e == nil && (!global || !audit) {
					e = errors.New("Claim reached DML before directory and system audit locks")
				}
				entered <- e
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				return e
			}
			t.Cleanup(func() { decisionClaimBeforeDMLTestHook = nil })
			go func() {
				claimDone <- st.AuthMutate(ctx, func(as store.AuthScope) error {
					if finalize {
						doc := []byte(`{"decision":"allow"}`)
						_, e := as.FinalizeDecisionClaim(ctx, claim.ID, claim.Version, doc, sha256HexBytes(doc), "v1")
						return e
					}
					var e error
					claim, _, e = as.ClaimDecision(ctx, model.PDPDecisionClaim{TargetTenantID: tenant, HandleJTI: model.NewID(), PEPServiceID: service.ID, NonceHash: "nonce", RequestFingerprint: "fingerprint", RequestIssuedAt: model.NewTimestamp(time.Now())}, nil)
					return e
				})
			}()
			if e := <-entered; e != nil {
				close(release)
				<-claimDone
				t.Fatal(e)
			}
			dropDone := make(chan error, 1)
			go func() {
				dropDone <- st.System(ctx, func(sys store.SystemScope) error { return sys.DropTenant(ctx, tenant) })
			}()
			select {
			case e := <-dropDone:
				close(release)
				<-claimDone
				t.Fatalf("Drop bypassed held authority: %v", e)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			for _, ch := range []<-chan error{claimDone, dropDone} {
				if e := <-ch; e != nil {
					t.Fatal(e)
				}
			}
			decisionClaimBeforeDMLTestHook = nil
			if e := st.AuthView(ctx, func(as store.AuthScope) error {
				_, e := as.PDPDecisionClaims().Get(ctx, claim.ID)
				if !errors.Is(e, store.ErrNotFound) {
					return fmt.Errorf("claim remains after Drop: %v", e)
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestLineageWaitingSystemOwnsNoLowerLocksPostgres(t *testing.T) {
	st := openLineagePG(t)
	ss := st.(*sqlStore)
	// Budget after the fixture: the tenant provision is not what the 10 s deadline samples,
	// and paying for it here is how this class turns a green contract red on a contended
	// runner (01f81b8e81 / 4859cc43f3 / 346bce0c8a).
	tenant := provisionTenant(t, st, "system-wait")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	writer := make(chan error, 1)
	go func() {
		writer <- st.Mutate(ctx, tenant, func(store.Scope) error { close(entered); <-release; return nil })
	}()
	<-entered
	pidCh := make(chan int, 1)
	system := make(chan error, 1)
	go func() {
		system <- st.System(ctx, func(sys store.SystemScope) error {
			var pid int
			if err := sys.(*systemScope).tx.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&pid); err != nil {
				return err
			}
			pidCh <- pid
			if _, err := sys.EnsureSystemTenant(ctx); err != nil {
				return err
			}
			return sys.EnsureDefaultWorkspaces(ctx)
		})
	}()
	pid := <-pidCh
	observed := false
	for i := 0; i < 100; i++ {
		var waiting, held int
		if err := ss.adminDB.QueryRowContext(ctx, "SELECT count(*) FILTER(WHERE NOT granted),count(*) FILTER(WHERE granted) FROM pg_catalog.pg_locks WHERE pid=$1 AND locktype='advisory'", pid).Scan(&waiting, &held); err != nil {
			close(release)
			<-writer
			<-system
			t.Fatal(err)
		}
		if waiting > 0 {
			if held != 0 {
				close(release)
				<-writer
				<-system
				t.Fatal("waiting System already owns lower advisory lock")
			}
			observed = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	for _, ch := range []<-chan error{writer, system} {
		if err := <-ch; err != nil {
			t.Fatal(err)
		}
	}
	if !observed {
		t.Fatal("never observed actual System gate wait")
	}
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		return sys.EnsureDefaultWorkspaces(ctx)
	}); err != nil {
		t.Fatalf("idempotent boot callback: %v", err)
	}
}
