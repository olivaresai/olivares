// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
)

// standbyStore is a node that lost the leader election: the store's write gate
// refuses Custody (a read-write door) with ErrNotLeader, exactly as sqlStore does
// on a Postgres standby. Everything else is the real store.
type standbyStore struct{ store.Store }

func (standbyStore) Custody(context.Context, model.TenantID, func(store.CustodyScope) error) error {
	return store.ErrNotLeader
}

func standbyManifest(t *testing.T, e *estate, ledger store.AuditReader) (*dr.Manifest, error) {
	t.Helper()
	return standbyManifestOn(t, e, standbyStore{e.st}, ledger)
}

func standbyManifestOn(t *testing.T, e *estate, st store.Store, ledger store.AuditReader) (*dr.Manifest, error) {
	t.Helper()
	return dr.BuildManifest(context.Background(), st, e.pub(), e.cpVerifier(t), dr.BuildOptions{
		EngineKind: string(store.EngineSQLite),
		Version:    "test",
		TipMatch:   dr.TipAdvisory,
		Now:        time.Unix(1_700_000_000, 0),
		Ledger:     ledger,
	})
}

func openLedger(t *testing.T, e *estate) store.AuditReader {
	t.Helper()
	ledger, err := sqlstore.OpenAuditReader(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: e.dbPath})
	if err != nil {
		t.Fatalf("open the ledger reader: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	return ledger
}

// #496: an online Postgres backup builds its manifest on a node that is a standby
// because `serve` holds the lock. Without a Ledger the manifest reads through
// Store.Custody and the write gate refuses it; with one the tips are the leader's.
func TestBuildManifestOnAStandbyReadsTheTipsThroughTheLedger(t *testing.T) {
	e := newEstate(t)
	a, b := e.newTenant(t), e.newTenant(t)
	e.appendN(t, a, 3)
	e.appendN(t, b, 5)
	e.checkpointAll(t)
	want := manifestFor(t, e)

	// The baseline that keeps the next assertion honest: this store really is
	// refused, so a manifest that succeeds below did not read through Custody.
	if _, err := standbyManifest(t, e, nil); !errors.Is(err, store.ErrNotLeader) {
		t.Fatalf("a standby without a Ledger must be refused with ErrNotLeader, got %v", err)
	}

	got, err := standbyManifest(t, e, openLedger(t, e))
	if err != nil {
		t.Fatalf("a standby with a Ledger must build the manifest: %v", err)
	}
	if len(got.Tenants) != len(want.Tenants) {
		t.Fatalf("tenants: got %d, want %d", len(got.Tenants), len(want.Tenants))
	}
	for i, w := range want.Tenants {
		if g := got.Tenants[i]; g != w {
			t.Errorf("tenant %s: tip through the Ledger is %+v, the leader's is %+v", w.Tenant, g, w)
		}
	}
	if tip := tipFor(t, got, b.String()); tip.HeadSeq < 5 || !tip.VerifiedAtBackup {
		t.Fatalf("tenant %s tip = %+v; want its real chain, verified", b, tip)
	}
}

// The Ledger path must not turn a backup into a certificate for a corrupt ledger.
func TestBuildManifestThroughTheLedgerStillRefusesToCertifyATamperedChain(t *testing.T) {
	e := newEstate(t)
	tn := e.newTenant(t)
	e.appendN(t, tn, 3)
	e.checkpointAll(t)
	rawExec(t, e.dbPath,
		"DROP TRIGGER audit_events_no_update",
		"UPDATE audit_events SET sig = randomblob(64) WHERE tenant_id = '"+tn.String()+"' AND action = 'audit.checkpoint'",
	)
	m, err := standbyManifest(t, e, openLedger(t, e))
	if err != nil {
		t.Fatalf("build the manifest: %v", err)
	}
	if tip := tipFor(t, m, tn.String()); tip.VerifiedAtBackup || !strings.HasPrefix(tip.VerifyReason, "checkpoints:") {
		t.Fatalf("tampered tenant tip = %+v; want unverified with a checkpoints reason", tip)
	}
}

// A tenant whose service is withdrawn still gets a real, verified tip: the Ledger is
// a custodial read, and the suspension guard that denies the service door does not
// reach it. The store is layered as in production (suspension over the node's store).
func TestBuildManifestThroughTheLedgerCoversAWithdrawnTenant(t *testing.T) {
	e := newEstate(t)
	tn := e.newTenant(t)
	e.appendN(t, tn, 4)
	e.checkpointAll(t)
	if err := e.st.System(context.Background(), func(sys store.SystemScope) error {
		_, err := sys.SetOrgStatus(context.Background(), tn, model.StatusSuspended)
		return err
	}); err != nil {
		t.Fatalf("withdraw the tenant: %v", err)
	}
	m, err := standbyManifestOn(t, e, suspension.Guard(standbyStore{e.st}, nil), openLedger(t, e))
	if err != nil {
		t.Fatalf("build the manifest: %v", err)
	}
	if tip := tipFor(t, m, tn.String()); tip.HeadSeq < 4 || !tip.VerifiedAtBackup {
		t.Fatalf("withdrawn tenant tip = %+v; want its real chain, verified", tip)
	}
}
