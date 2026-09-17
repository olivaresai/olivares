// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/store"
)

// Test access to the real private operation, never a production constructor or
// public fixture option. Tests exercise exact noncomplete CAS under real locks.
func drTransitionControlForTest(ctx context.Context, cfg store.Config, spec PendingRestoreSpec, predecessor restorePredecessor, next string) (RestoreControlReport, error) {
	return withPostgresRestoreControl(ctx, cfg, spec, func(op *restoreOperation) (RestoreControlReport, error) {
		return op.transition(ctx, predecessor, next)
	})
}

// drSetCompleteReaderRowForTest changes only an already compiled/ACL-verified
// fixture. SQL setup is not receipt attribution, custody readback or a restore.
func drSetCompleteReaderRowForTest(t *testing.T, pg pgtest.DSNs, cfg store.Config) {
	t.Helper()
	super := drOpenSuper(t, pg.Superuser)
	if _, err := super.Exec(`UPDATE `+drControlRelation+` SET state='complete',keyset_sha256=pg_catalog.decode($1,'hex')`, drFactualKeyset().SHA256); err != nil {
		t.Fatal(err)
	}
	report, err := ReadPostgresRestoreControl(context.Background(), cfg)
	if err != nil || report.State != opgate.StateComplete {
		t.Fatalf("invalid complete reader SQL fixture: %+v %v", report, err)
	}
}

// drFactualKeyset is the DETERMINISTIC keyset these control-shape fixtures compare
// against, and its digest is verified against an INDEPENDENT computation here.
//
// ⚠ IT IS A CONTROL-SHAPE FIXTURE AND NOT A CUSTODY FIXTURE, which is the distinction
// F3-IR-5 turns on. Its three keys are genuine Ed25519 keys, but nothing in this
// package loads them from a file or constructs a signer from them, so a test built on
// it can only prove things about the control's SHAPE — its digest column, its
// malformation handling, its destination binding. It can never prove that the custody
// a boot actually selected was the authorized one.
//
// The custody comparison is measured where the keys are real and on disk and the
// signers are really constructed from them: drselectedcustody_pg_test.go here, and
// cmd/olivares/dr_selectedcustody_test.go for the loaders and the signer identities.
// Do not extend this helper into standing for those.
func drFactualKeyset() opgate.Keyset {
	var keys []opgate.SelectedKey
	for i, p := range []opgate.KeyPurpose{opgate.KeyAudit, opgate.KeyCatalog, opgate.KeyPolicy} {
		public := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(11 + i)}, 32)).Public().(ed25519.PublicKey)
		f, _ := opgate.FingerprintPublicKey(public)
		keys = append(keys, opgate.SelectedKey{Purpose: p, Source: opgate.CustodyLocal, PublicSHA256: f})
	}
	k, err := opgate.NewKeyset(keys)
	if err != nil {
		panic(err)
	}
	return k
}

// TestTheFactualKeysetFixtureCommitsToItsOwnPublicKeys checks the fixture's digest
// against a second, hand-written implementation of the ratified v1 encoding rather
// than against opgate.NewKeyset, which produced it. An encoder compared with itself
// proves nothing, and that is the whole shape of the defect this sublot closes.
func TestTheFactualKeysetFixtureCommitsToItsOwnPublicKeys(t *testing.T) {
	type entry struct {
		Purpose      string `json:"purpose"`
		Source       string `json:"source"`
		PublicSHA256 string `json:"public_sha256"`
	}
	var entries [3]entry
	for i, p := range []string{"audit", "catalog", "policy"} {
		public := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(11 + i)}, 32)).Public().(ed25519.PublicKey)
		sum := sha256.Sum256(public)
		entries[i] = entry{p, "local", hex.EncodeToString(sum[:])}
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(append([]byte("olivares.dr.keyset.v1\n"), raw...))
	if got := drFactualKeyset().SHA256; got != hex.EncodeToString(digest[:]) {
		t.Fatalf("the fixture digest %s is not the independently computed commitment %s", got, hex.EncodeToString(digest[:]))
	}
}
func drFixtureDestination(t *testing.T, dsn, database string) opgate.PostgresDestination {
	t.Helper()
	db := drOpenSuper(t, dsn)
	var sysid string
	if err := db.QueryRowContext(context.Background(), `SELECT system_identifier::pg_catalog.text FROM pg_catalog.pg_control_system()`).Scan(&sysid); err != nil {
		t.Fatal(err)
	}
	return opgate.PostgresDestination{Database: database, Schema: "public", SystemIdentifier: sysid}
}

// drObservationFor is the measurement that matches a fixture's authorized keyset.
//
// It goes through NewCustodyObservation from the per-purpose selection, not from the
// digest: there is deliberately no constructor that accepts one, and a test that
// wanted to hand a digest in would be re-creating the defect.
func drObservationFor(t *testing.T, k opgate.Keyset) CustodyObservation {
	t.Helper()
	typed, err := k.Typed()
	if err != nil {
		t.Fatalf("the fixture keyset is not a valid commitment: %v", err)
	}
	keys := typed.Keys()
	observed, err := NewCustodyObservation(keys[:])
	if err != nil {
		t.Fatalf("build the fixture observation: %v", err)
	}
	return observed
}

// drOpenUnderBootAdmission opens a destination the way an ordinary boot does: through
// the admission that read the control BEFORE the keys were loaded, carrying the same
// retained session into the publication decision, with the custody measurement.
//
// ⚠ IT REPLACES OpenWithRestoreWitness FOR COMPLETED FIXTURES, and that is a
// deliberate consequence of the ratified contract rather than a convenience. A direct
// Open of a completed enrolled target now refuses with a missing-custody diagnosis:
// the plain witness is local evidence and can only TIGHTEN, and it was never a
// measurement of what this process loaded. Cases about a NONCOMPLETED or absent
// control keep using the direct constructors, because nothing there requires custody.
func drOpenUnderBootAdmission(ctx context.Context, t *testing.T, cfg store.Config, observed CustodyObservation) (store.Store, error) {
	t.Helper()
	adm, err := BeginLocalAdmission(ctx, cfg, t.TempDir())
	if err != nil {
		return nil, err
	}
	defer adm.Close()
	return adm.Open(ctx, nil, observed, nil)
}
