// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/store"
)

// ir5c2CompleteSQLiteTarget installs a genuine COMPLETE local control on a SQLite
// destination and returns its DSN and the keyset that control authorized.
//
// The record goes through the production codec under a real exclusive lease, exactly as
// an operation would write it, so what these cases read is a control this build wrote.
func ir5c2CompleteSQLiteTarget(t *testing.T) (string, opgate.Keyset) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "olivares.db")
	target, err := opgate.ResolveSQLiteTarget(dsn)
	if err != nil {
		t.Fatal(err)
	}
	anchor := target.Anchor()

	var selected []opgate.SelectedKey
	for _, p := range []opgate.KeyPurpose{opgate.KeyAudit, opgate.KeyCatalog, opgate.KeyPolicy} {
		public, _, gerr := ed25519.GenerateKey(nil)
		if gerr != nil {
			t.Fatal(gerr)
		}
		f, ferr := opgate.FingerprintPublicKey(public)
		if ferr != nil {
			t.Fatal(ferr)
		}
		selected = append(selected, opgate.SelectedKey{Purpose: p, Source: opgate.CustodyLocal, PublicSHA256: f})
	}
	wire, err := opgate.NewKeyset(selected)
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if err != nil || !ok {
		t.Fatalf("take the fixture lease: ok=%t %v", ok, err)
	}
	defer func() { _ = lease.Release() }()
	if cerr := lease.Commit(anchor, opgate.Record{
		Format: opgate.Format, Revision: 1,
		OpID: "b2c3d4e5f60718293a4b5c6d7e8f9001", State: opgate.StateComplete, Enrolled: true,
		PlanSHA256: strings.Repeat("cd", 32),
		Destination: opgate.Destination{
			Engine: "sqlite", CanonicalPath: anchor.Canonical(), SQLiteFile: target.CanonicalPath(),
		},
		Keyset: wire, ObservedAt: "2026-09-12T00:00:00Z",
	}); cerr != nil {
		t.Fatalf("commit the completed fixture control: %v", cerr)
	}
	return dsn, wire
}

// ir5c2AssertNoDestination proves a refusal created neither the database nor a sidecar.
//
// ⛔ THE SIDECARS MATTER AS MUCH AS THE FILE. openSQLite forces its first connection to
// apply the pragmas, which creates the target AND its -wal/-shm companions; a refusal
// that merely avoided returning a Store while leaving those behind would have opened the
// destination it was refusing to open (F3-IR-9).
func ir5c2AssertNoDestination(t *testing.T, dsn string) {
	t.Helper()
	for _, p := range []string{dsn, dsn + "-wal", dsn + "-shm", dsn + "-journal"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("the refusal created %s: %v", p, err)
		}
	}
}

// ⛔ IR5-C2 / F2: A DIRECT SQLITE Open OF A COMPLETED TARGET PUBLISHED WITH NO CUSTODY.
//
// The independent review measured it: a SQLite data directory carrying a genuine COMPLETE
// local control naming a three-key custody generation returned err=<nil> from
// sqlstore.Open and published a store, while the same call on PostgreSQL refused. The
// contract is engine-neutral — "SQLite uses its resolved local control under the same
// lease" — so the closure was simply missing on one engine.
//
// The requirement is now frozen from the record the fence's own lease reads, and the
// refusal lands BEFORE anything opens or creates the destination.
func TestIR5C2DirectSQLiteOpenRefusesACompletedTargetWithoutCustody(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(ctx context.Context, cfg store.Config) (store.Store, error)
	}{
		{"Open", func(ctx context.Context, cfg store.Config) (store.Store, error) {
			return Open(ctx, cfg, nil)
		}},
		{"OpenWithRestoreWitness", func(ctx context.Context, cfg store.Config) (store.Store, error) {
			// A witness can TIGHTEN uncertainty. It is not an observation and it cannot
			// authorize complete, so supplying one changes nothing here.
			return OpenWithRestoreWitness(ctx, cfg, nil, RestoreEnrolmentWitness{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn, wire := ir5c2CompleteSQLiteTarget(t)
			st, err := tc.open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: dsn})
			if st != nil {
				_ = st.Close()
			}
			if err == nil {
				t.Fatal("a direct Open published a store for a completed SQLite target having consulted nothing about its custody")
			}
			if !errors.Is(err, ErrRestorePublicationFenced) {
				t.Fatalf("wrong classification: %v", err)
			}
			if !strings.Contains(err.Error(), "no observation of the custody it actually loaded") {
				t.Fatalf("the refusal does not name the missing measurement: %v", err)
			}
			// The diagnosis names the operation and the authorized generation, which is
			// what an operator needs to provision the right custody.
			if !strings.Contains(err.Error(), "b2c3d4e5f60718293a4b5c6d7e8f9001") {
				t.Fatalf("the refusal does not name the completed operation: %v", err)
			}
			_ = wire
			ir5c2AssertNoDestination(t, dsn)
		})
	}
}

// The admission path supplies the measurement, so the SAME completed target publishes
// when the custody it authorized is actually presented. Without this the case above
// would also pass for a build that refused every completed SQLite target unconditionally.
func TestIR5C2CompletedSQLiteTargetPublishesUnderItsAuthorizedCustody(t *testing.T) {
	ctx := context.Background()
	dsn, wire := ir5c2CompleteSQLiteTarget(t)
	dataDir := filepath.Dir(dsn)

	adm, err := BeginLocalAdmission(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, dataDir)
	if err != nil {
		t.Fatalf("the admission refused a healthy completed target: %v", err)
	}
	defer adm.Close()
	req := adm.CustodyRequirement()
	if !req.CustodyRequired() || req.KeysetSHA256() != wire.SHA256 {
		t.Fatalf("the admission did not freeze the completed requirement: %+v", req)
	}
	st, err := adm.Open(ctx, nil, drObservationFor(t, wire), nil)
	if err != nil {
		t.Fatalf("the authorized custody was refused: %v", err)
	}
	_ = st.Close()
}

// An unenrolled SQLite destination is the ordinary case for every installation that
// exists today, and the direct constructors must still publish for it.
func TestIR5C2DirectSQLiteOpenStillPublishesAnUnenrolledTarget(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "olivares.db")
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("a destination with no restore control was refused: %v", err)
	}
	_ = st.Close()
	if _, serr := os.Stat(dsn); serr != nil {
		t.Fatalf("the ordinary open did not create its destination: %v", serr)
	}
}

// servingPublication is the ONE derived value both custody checks read. Pinning its
// table here means a future caller cannot be added on one side of the distinction only.
func TestIR5C2ServingPublicationIsDerivedFromPurposeAndMaintenance(t *testing.T) {
	callback := func(*sqlStore) error { return nil }
	for _, tc := range []struct {
		name        string
		purpose     preparePurpose
		maintenance func(*sqlStore) error
		want        bool
	}{
		{"serve_and_boot", prepareThroughReadiness, nil, true},
		{"schema_only_migration", prepareSchemaOnly, nil, false},
		{"directory_maintenance", prepareThroughReadiness, callback, false},
		{"schema_only_with_callback_is_refused_elsewhere", prepareSchemaOnly, callback, false},
	} {
		if got := servingPublication(tc.purpose, tc.maintenance); got != tc.want {
			t.Fatalf("%s: servingPublication = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// ⚠ THE BOUNDARY OF THE DIRECT LEG, PINNED RATHER THAN LEFT IMPLICIT.
//
// A completed control can live on either of an installation's two local anchors: the
// DATA DIRECTORY that holds the custody, and the resolved STORE FILE. The direct
// constructors receive neither a data directory nor anything they could resolve one
// from — store.Config carries Engine, DSN and connection settings, and nothing else —
// so a control placed only on the data-directory anchor is outside what
// `sqlstore.Open` can see.
//
// Deriving it from the DSN's parent directory would be the independent local authority
// source the contract forbids, and it would be WRONG: --data-dir and --dsn are
// independent, so the parent of the database file is not generally the data directory.
//
// This is not a hole in the closure; it is where the closure's input ends. The boot
// admission IS given the data directory, fences both anchors and freezes the
// requirement from whichever carries the completed record — which is the path every
// serving boot of a restored installation actually takes. Both halves are asserted
// here so a future reader can see the division rather than infer it from a silence.
func TestIR5C2ADataDirectoryOnlyControlIsTheAdmissionsToEnforce(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dsn := filepath.Join(dataDir, "olivares.db")
	target, err := opgate.ResolveSQLiteTarget(dsn)
	if err != nil {
		t.Fatal(err)
	}
	anchor, present, err := opgate.AnchorForDataDir(dataDir)
	if err != nil || !present {
		t.Fatalf("anchor the data directory: present=%t %v", present, err)
	}

	var selected []opgate.SelectedKey
	for _, p := range []opgate.KeyPurpose{opgate.KeyAudit, opgate.KeyCatalog, opgate.KeyPolicy} {
		public, _, gerr := ed25519.GenerateKey(nil)
		if gerr != nil {
			t.Fatal(gerr)
		}
		f, ferr := opgate.FingerprintPublicKey(public)
		if ferr != nil {
			t.Fatal(ferr)
		}
		selected = append(selected, opgate.SelectedKey{Purpose: p, Source: opgate.CustodyLocal, PublicSHA256: f})
	}
	wire, err := opgate.NewKeyset(selected)
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if err != nil || !ok {
		t.Fatalf("take the fixture lease: ok=%t %v", ok, err)
	}
	if cerr := lease.Commit(anchor, opgate.Record{
		Format: opgate.Format, Revision: 1,
		OpID: "c3d4e5f60718293a4b5c6d7e8f900112", State: opgate.StateComplete, Enrolled: true,
		PlanSHA256: strings.Repeat("ef", 32),
		Destination: opgate.Destination{
			Engine: "sqlite", CanonicalPath: anchor.Canonical(), SQLiteFile: target.CanonicalPath(),
		},
		Keyset: wire, ObservedAt: "2026-09-12T00:00:00Z",
	}); cerr != nil {
		t.Fatalf("commit the data-directory control: %v", cerr)
	}
	if rerr := lease.Release(); rerr != nil {
		t.Fatal(rerr)
	}

	// THE ADMISSION SEES IT, because it is given the data directory and fences it.
	adm, aerr := BeginLocalAdmission(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, dataDir)
	if aerr != nil {
		t.Fatalf("the admission refused a healthy data-directory control: %v", aerr)
	}
	req := adm.CustodyRequirement()
	if !req.CustodyRequired() || req.KeysetSHA256() != wire.SHA256 {
		t.Fatalf("the admission did not freeze the data-directory requirement: %+v", req)
	}
	st, oerr := adm.Open(ctx, nil, CustodyObservation{}, nil)
	if st != nil {
		_ = st.Close()
	}
	if oerr == nil {
		t.Fatal("the admission published a data-directory-completed installation with no custody observation")
	}
	if !strings.Contains(oerr.Error(), "no observation of the custody it loaded") {
		t.Fatalf("wrong refusal from the admission: %v", oerr)
	}
	adm.Close()

	// THE DIRECT LEG DOES NOT, and the reason is an absent input rather than an absent
	// check: it is handed no data directory. Recording it as a measured boundary keeps
	// a future reader from believing the direct leg covers both anchors.
	direct, derr := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, nil)
	if direct != nil {
		_ = direct.Close()
	}
	if derr != nil {
		t.Fatalf("the direct leg is expected to be blind to a data-directory-only control, "+
			"because store.Config carries no data directory; it refused instead, so this boundary has moved: %v", derr)
	}
}
