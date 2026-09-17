// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/store"
)

// ir5GenuineSelection builds three REAL Ed25519 keys and the selection that names
// them, together with the digest computed INDEPENDENTLY of opgate's encoder.
//
// The independent computation is the point. Asking opgate.NewKeyset for the expected
// value and then asserting the production path produces it would compare the encoder
// with itself — the same shape of vacuous comparison this sublot closes at the
// custody layer.
func ir5GenuineSelection(t *testing.T, sources [3]string) ([]opgate.SelectedKey, string) {
	t.Helper()
	type entry struct {
		Purpose      string `json:"purpose"`
		Source       string `json:"source"`
		PublicSHA256 string `json:"public_sha256"`
	}
	purposes := [3]opgate.KeyPurpose{opgate.KeyAudit, opgate.KeyCatalog, opgate.KeyPolicy}
	var (
		selected []opgate.SelectedKey
		entries  [3]entry
	)
	for i, p := range purposes {
		public, _, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		f, err := opgate.FingerprintPublicKey(public)
		if err != nil {
			t.Fatal(err)
		}
		selected = append(selected, opgate.SelectedKey{Purpose: p, Source: opgate.CustodySource(sources[i]), PublicSHA256: f})
		sum := sha256.Sum256(public)
		entries[i] = entry{string(p), sources[i], hex.EncodeToString(sum[:])}
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(append([]byte("olivares.dr.keyset.v1\n"), raw...))
	return selected, hex.EncodeToString(digest[:])
}

// ir5CompleteControlWithKeyset installs a pending control and drives its row to
// COMPLETE carrying the given keyset digest, through the already compiled and
// ACL-verified fixture path.
func ir5CompleteControlWithKeyset(t *testing.T, cfg store.Config, superDSN, keysetSHA256 string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{
		OpID: drTestOpA, PlanSHA256: drTestPlanA,
	}); err != nil {
		t.Fatalf("install the pending control: %v", err)
	}
	super := drOpenSuper(t, superDSN)
	if _, err := super.ExecContext(ctx,
		`UPDATE `+drControlRelation+` SET state='complete',keyset_sha256=pg_catalog.decode($1,'hex')`, keysetSHA256); err != nil {
		t.Fatalf("drive the fixture row to complete: %v", err)
	}
	report, err := ReadPostgresRestoreControl(ctx, cfg)
	if err != nil || report.State != opgate.StateComplete || report.KeysetSHA256 != keysetSHA256 {
		t.Fatalf("invalid complete fixture: %+v %v", report, err)
	}
}

// ⛔ THE REMOTE COMPLETED GATE WITH NO LOCAL RECORD AND NO KEYS.
//
// This is the case F3-IR-5 names and the reason the PostgreSQL read had to move into
// the admission. The node has nothing on disk — no sidecar record, no witness, no
// signing keys — and the destination's own control says a restore completed under a
// specific custody generation. Reading that control only inside Open would mean the
// three loaders had already run, and on an empty data directory they MINT: the node
// would fabricate custody for a restored estate and learn the fact afterwards.
//
// So the admission must report the requirement BEFORE any key is loaded, and refuse
// every selection that is not the authorized one.
func TestIR5RemoteCompletedGateRequiresCustodyWithNoLocalRecord(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	authorized, digest := ir5GenuineSelection(t, [3]string{"local", "local", "local"})
	ir5CompleteControlWithKeyset(t, cfg, pg.Superuser, digest)

	// A data directory that does not exist: no anchor, no record, no keys. Exactly the
	// new node a physical restore lands on.
	dataDir := t.TempDir() + "/never-created"

	before := drCoordinationAcquisitions.Load()
	adm, err := BeginLocalAdmission(ctx, cfg, dataDir)
	if err != nil {
		t.Fatalf("the admission refused a healthy completed destination: %v", err)
	}
	defer adm.Close()

	req := adm.CustodyRequirement()
	if !req.CustodyRequired() {
		t.Fatal("a remote COMPLETED control did not require custody, so the loaders would have minted on an empty data directory")
	}
	if req.KeysetSHA256() != digest {
		t.Fatalf("the requirement names %s and the control names %s", req.KeysetSHA256(), digest)
	}
	if req.OperationID() != drTestOpA || req.PlanSHA256() != drTestPlanA {
		t.Fatalf("the requirement is not bound to the operation and plan: %+v", req)
	}
	// With NO local record there is no per-purpose expectation, and none is invented
	// from the digest.
	if got := req.ExpectedKeys(); len(got) != 0 {
		t.Fatalf("a digest-only control produced a per-purpose expectation out of nowhere: %+v", got)
	}
	// ONE shared acquisition for the whole admission, taken here, before the loaders.
	if got := drCoordinationAcquisitions.Load() - before; got != 1 {
		t.Fatalf("the boot admission took %d shared acquisitions, not exactly one", got)
	}

	// NO KEYS AT ALL: the caller has nothing to observe, and the publication refuses
	// with the missing-custody diagnosis rather than proceeding.
	st, oerr := adm.Open(ctx, nil, CustodyObservation{}, nil)
	if st != nil {
		_ = st.Close()
	}
	if oerr == nil {
		t.Fatal("a completed destination published with no observation of the custody this boot loaded")
	}
	if !errors.Is(oerr, ErrRestorePublicationFenced) || !strings.Contains(oerr.Error(), "no observation of the custody it loaded") {
		t.Fatalf("wrong refusal for a missing measurement: %v", oerr)
	}
	if got := drCoordinationAcquisitions.Load() - before; got != 1 {
		t.Fatalf("the refused publication REACQUIRED: %d total acquisitions", got)
	}
	_ = authorized
}

// TestIR5CompletedGateAdmitsTheAuthorizedSelectionAndRefusesEveryOther is the
// comparison itself, on a real server.
func TestIR5CompletedGateAdmitsTheAuthorizedSelectionAndRefusesEveryOther(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources [3]string
		mutate  func(t *testing.T, authorized []opgate.SelectedKey) []opgate.SelectedKey
		admits  bool
		reason  string
	}{
		{
			name:    "exact_authorized_selection",
			sources: [3]string{"local", "local", "local"},
			mutate:  func(t *testing.T, a []opgate.SelectedKey) []opgate.SelectedKey { return a },
			admits:  true,
		},
		{
			name:    "wrong_public_key",
			sources: [3]string{"local", "local", "local"},
			mutate: func(t *testing.T, a []opgate.SelectedKey) []opgate.SelectedKey {
				other, _ := ir5GenuineSelection(t, [3]string{"local", "local", "local"})
				out := append([]opgate.SelectedKey(nil), a...)
				out[1].PublicSHA256 = other[1].PublicSHA256
				return out
			},
			reason: "the signers this process would serve with are not the ones the completed operation published",
		},
		{
			name:    "correct_key_wrong_source",
			sources: [3]string{"local", "local", "local"},
			mutate: func(t *testing.T, a []opgate.SelectedKey) []opgate.SelectedKey {
				out := append([]opgate.SelectedKey(nil), a...)
				out[2].Source = opgate.CustodyBYOKFile
				return out
			},
			reason: "the signers this process would serve with are not the ones the completed operation published",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pg := isolatedPGSplit(t)
			cfg := drPGConfig(pg)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			authorized, digest := ir5GenuineSelection(t, tc.sources)
			ir5CompleteControlWithKeyset(t, cfg, pg.Superuser, digest)

			before := drCoordinationAcquisitions.Load()
			adm, err := BeginLocalAdmission(ctx, cfg, t.TempDir())
			if err != nil {
				t.Fatalf("take the admission: %v", err)
			}
			defer adm.Close()
			if !adm.CustodyRequirement().CustodyRequired() {
				t.Fatal("the completed control did not require custody")
			}

			observed, oerr := NewCustodyObservation(tc.mutate(t, authorized))
			if oerr != nil {
				t.Fatalf("build the observation: %v", oerr)
			}
			st, perr := adm.Open(ctx, nil, observed, nil)
			if st != nil {
				t.Cleanup(func() { _ = st.Close() })
			}
			if tc.admits {
				if perr != nil {
					t.Fatalf("the authorized selection was refused: %v", perr)
				}
			} else {
				if perr == nil {
					t.Fatalf("%s was published under custody the control never authorized", tc.name)
				}
				if !errors.Is(perr, ErrRestorePublicationFenced) || !strings.Contains(perr.Error(), tc.reason) {
					t.Fatalf("wrong refusal: %v", perr)
				}
			}
			// EXACTLY ONE shared acquisition either way: the session the control was
			// read on before the loaders is the session the decision is made on, and
			// nothing reconnects to recover an admission.
			if got := drCoordinationAcquisitions.Load() - before; got != 1 {
				t.Fatalf("the admission took %d shared acquisitions, not exactly one", got)
			}
		})
	}
}

// TestIR5NoncompletedRemoteGatesRefuseBeforeAnyKeyCouldLoad keeps the early refusals
// where the whole point is that they happen BEFORE the caller can load a key: the
// admission returns an error and no requirement at all.
func TestIR5NoncompletedRemoteGatesRefuseBeforeAnyKeyCouldLoad(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{
		OpID: drTestOpA, PlanSHA256: drTestPlanA,
	}); err != nil {
		t.Fatalf("install the pending control: %v", err)
	}
	adm, err := BeginLocalAdmission(ctx, cfg, t.TempDir())
	if adm != nil {
		adm.Close()
	}
	if err == nil {
		t.Fatal("a PENDING remote control admitted a boot that would then have loaded keys")
	}
	if !errors.Is(err, ErrRestorePublicationFenced) || !strings.Contains(err.Error(), opgate.StatePending) {
		t.Fatalf("wrong refusal for a pending control: %v", err)
	}
}

// TestIR5AnUnenrolledRemoteDestinationStillBootsOrdinarily is the preserved positive:
// legacy_or_lost_unknown with no surviving witness demands no custody, so a genuine
// ordinary boot continues with every guard it already had.
func TestIR5AnUnenrolledRemoteDestinationStillBootsOrdinarily(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	adm, err := BeginLocalAdmission(ctx, cfg, t.TempDir())
	if err != nil {
		t.Fatalf("the admission refused a destination with no control at all: %v", err)
	}
	defer adm.Close()
	if adm.CustodyRequirement().CustodyRequired() {
		t.Fatal("a destination with no restore control demanded completed custody")
	}
	// An unenrolled destination publishes with no measurement at all, which is what
	// every installation that exists today does.
	st, oerr := adm.Open(ctx, nil, CustodyObservation{}, nil)
	if oerr != nil {
		t.Fatalf("the ordinary unenrolled boot was refused: %v", oerr)
	}
	_ = st.Close()
}
