// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

type storeProofEstate struct {
	st       store.Store
	sm       *sessions.Module
	tenant   model.TenantID
	listOrgs sessionOrgLister
}

func openStoreProofEstate(t *testing.T, cfg store.Config, provision bool) storeProofEstate {
	t.Helper()
	ctx := context.Background()
	sm := sessions.New()
	st, err := coreengine.Open(ctx, cfg, sm.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	estate := storeProofEstate{st: st, sm: sm}
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		if provision {
			org, err := sys.CreateOrg(ctx, model.Org{Name: "proof", Slug: "proof", Status: model.StatusActive})
			estate.tenant = org.TenantID
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	data := api.NewModuleData(st)
	sm.UseData(data)
	sm.UseCommunicationGuardReconciliationData(sessions.NewCommunicationGuardReconciliationData(data))
	estate.listOrgs = func(ctx context.Context) ([]model.Org, error) {
		var orgs []model.Org
		err := st.System(ctx, func(sys store.SystemScope) error {
			var err error
			orgs, err = sys.ListOrgs(ctx)
			return err
		})
		return orgs, err
	}
	return estate
}

func (e storeProofEstate) proofWitness(isLeader func() bool) *communicationStoreProofWitness {
	guard := newCommunicationGuardStoreWitness(e.listOrgs, nil, e.sm, isLeader)
	return newCommunicationStoreProofWitness(e.st, guard, e.sm, e.listOrgs, isLeader, time.Now)
}

// hiddenStatusStore embeds the Store interface value, so the optional
// DirectoryStatuser capability is NOT forwarded: exactly what a decorator that
// forgot the capability looks like to the proof.
type hiddenStatusStore struct{ store.Store }

func TestCommunicationStoreProofRequiresExplicitActivationThenReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "proof.db")}
	leader := func() bool { return true }

	staged := openStoreProofEstate(t, cfg, true)
	witness := staged.proofWitness(leader)
	if err := witness.ReconcileAndVerify(ctx); err == nil {
		t.Fatal("staged writer control passed the store proof")
	}
	proof := witness.Proof()
	if proof.Ready || !proof.Supported || proof.ControlMode != store.DirectoryControlStaged ||
		!proof.GuardVerified || !proof.SchemaVerified || proof.HistoricalEnabled ||
		proof.WriterPosture != store.DirectoryWriterSQLiteCapability {
		t.Fatalf("staged proof = %+v", proof)
	}
	if !strings.Contains(strings.Join(proof.Blockers, "\n"), "writer_control_not_enforced") {
		t.Fatalf("staged blockers = %v", proof.Blockers)
	}
	if ready, _ := witness.CommunicationStoreReady(ctx); ready {
		t.Fatal("staged proof reported ready")
	}
	if err := staged.st.Close(); err != nil {
		t.Fatal(err)
	}

	// The ceremony must present the SAME edition the estate was created with:
	// the migration guard derives the rollout identity from the registered
	// schema, and a narrower registration is refused at preflight.
	raw, err := coreengine.Open(ctx, cfg, sessions.New().RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coreengine.ActivateDirectoryWriter(ctx, raw, cfg, coreengine.DirectoryWriterActivationRequest{
		ExpectedGeneration: 1, WritersUpgraded: true, WritersDrained: true,
		Actor: "test-operator", Reason: "store proof acceptance",
	})
	if err != nil || !result.Changed || !result.ReopenRequired {
		t.Fatalf("activate = %+v err=%v", result, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	enforced := openStoreProofEstate(t, cfg, false)
	witness = enforced.proofWitness(leader)
	if err := witness.ReconcileAndVerify(ctx); err != nil {
		t.Fatalf("enforced proof: %v\n%+v", err, witness.Proof())
	}
	proof = witness.Proof()
	if !proof.Ready || proof.ControlMode != store.DirectoryControlEnforced || proof.ExpectedGeneration != 2 ||
		proof.TenantsProved != 1 || !proof.EpochCoverageComplete || proof.HistoricalEnabled {
		t.Fatalf("enforced proof = %+v", proof)
	}
	if ready, err := witness.CommunicationStoreReady(ctx); !ready || err != nil {
		t.Fatalf("enforced readiness = %t %v", ready, err)
	}

	// Demotion removes readiness without changing the measured proof.
	demoted := enforced.proofWitness(func() bool { return false })
	_ = demoted.ReconcileAndVerify(ctx)
	if ready, _ := demoted.CommunicationStoreReady(ctx); ready || !demoted.Proof().Ready {
		t.Fatalf("demoted witness ready=%t proof=%+v", ready, demoted.Proof())
	}

	// A store that hides the status capability is unsupported, never ready.
	hidden := storeProofEstate{st: hiddenStatusStore{enforced.st}, sm: enforced.sm, listOrgs: enforced.listOrgs}
	unsupported := hidden.proofWitness(leader)
	if err := unsupported.ReconcileAndVerify(ctx); err == nil {
		t.Fatal("hidden status capability passed the proof")
	}
	if p := unsupported.Proof(); p.Supported || p.Ready || !strings.Contains(strings.Join(p.Blockers, " "), "directory_status_unsupported") {
		t.Fatalf("unsupported proof = %+v", p)
	}
}
