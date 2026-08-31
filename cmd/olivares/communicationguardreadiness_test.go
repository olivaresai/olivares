// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

type communicationGuardReconcilerProbe struct {
	staged      []model.TenantID
	verified    []model.TenantID
	stageErr    error
	stageErrAt  int
	verifyErr   error
	verifyErrAt int
	onStage     func()
	onVerify    func()
}

func (p *communicationGuardReconcilerProbe) ReconcileCommunicationGuards(
	_ context.Context,
	tenant model.TenantID,
	mode sessions.CommunicationGuardReconcileMode,
) error {
	if mode != sessions.CommunicationGuardReconcileStaged {
		return fmt.Errorf("unexpected reconcile mode %q", mode)
	}
	p.staged = append(p.staged, tenant)
	if p.onStage != nil {
		p.onStage()
	}
	if p.stageErr != nil && len(p.staged) == p.stageErrAt {
		return p.stageErr
	}
	return nil
}

func (p *communicationGuardReconcilerProbe) VerifyCommunicationGuards(
	_ context.Context,
	tenant model.TenantID,
) error {
	p.verified = append(p.verified, tenant)
	if p.onVerify != nil {
		p.onVerify()
	}
	if p.verifyErr != nil && len(p.verified) == p.verifyErrAt {
		return p.verifyErr
	}
	return nil
}

func communicationGuardTestOrg(
	tenant model.TenantID,
	status model.LifecycleStatus,
	region string,
) model.Org {
	return model.Org{
		BaseFields: model.BaseFields{ID: model.ID(tenant), TenantID: tenant},
		Status:     status, DataRegion: region,
	}
}

func TestCommunicationGuardStoreWitnessRequiresFullEnforcedCoverage(t *testing.T) {
	ctx := context.Background()
	tenantA := model.TenantID(model.NewID())
	tenantB := model.TenantID(model.NewID())
	orgs := []model.Org{
		communicationGuardTestOrg(model.SystemTenantID, model.StatusActive, ""),
		communicationGuardTestOrg(tenantA, model.StatusActive, ""),
		communicationGuardTestOrg(tenantB, model.StatusSuspended, ""),
	}
	listCalls := 0
	probe := &communicationGuardReconcilerProbe{}
	leader := true
	singleRegion, err := residency.NewRegistry("", nil)
	if err != nil {
		t.Fatal(err)
	}
	witness := newCommunicationGuardStoreWitness(
		func(context.Context) ([]model.Org, error) {
			listCalls++
			return append([]model.Org(nil), orgs...), nil
		}, singleRegion, probe, func() bool { return leader },
	)
	assertStillOff := func() {
		t.Helper()
		if ready, err := witness.CommunicationStoreReady(ctx); ready || err != nil {
			t.Errorf("in-flight witness = (%v,%v), want false,nil until enforced completes", ready, err)
		}
	}
	probe.onStage = assertStillOff
	probe.onVerify = assertStillOff

	if ready, err := witness.CommunicationStoreReady(ctx); ready || err != nil {
		t.Fatalf("unverified witness = (%v,%v), want false,nil", ready, err)
	}
	if err := witness.ReconcileAndVerify(ctx); err != nil {
		t.Fatalf("reconcile and verify: %v", err)
	}
	if listCalls != 2 {
		t.Fatalf("authoritative inventory calls = %d, want staged+enforced snapshots", listCalls)
	}
	wantTenants := []model.TenantID{tenantA, tenantB}
	sort.Slice(wantTenants, func(i, j int) bool { return wantTenants[i].String() < wantTenants[j].String() })
	if len(probe.staged) != 2 || len(probe.verified) != 2 ||
		probe.staged[0] != wantTenants[0] || probe.staged[1] != wantTenants[1] ||
		probe.verified[0] != wantTenants[0] || probe.verified[1] != wantTenants[1] {
		t.Fatalf("staged=%v verified=%v, want both business tenants in order", probe.staged, probe.verified)
	}
	if ready, err := witness.CommunicationStoreReady(ctx); !ready || err != nil {
		t.Fatalf("verified witness = (%v,%v), want true,nil", ready, err)
	}
	leader = false
	if ready, err := witness.CommunicationStoreReady(ctx); ready || err != nil {
		t.Fatalf("demoted witness = (%v,%v), want false,nil", ready, err)
	}
}

func TestCommunicationGuardStoreWitnessNeverPublishesPartialCoverage(t *testing.T) {
	ctx := context.Background()
	tenant := model.TenantID(model.NewID())
	orgs := []model.Org{communicationGuardTestOrg(tenant, model.StatusActive, "")}
	boom := errors.New("late guard failure")

	for _, test := range []struct {
		name       string
		list       sessionOrgLister
		probe      *communicationGuardReconcilerProbe
		wantBase   error
		wantStage  int
		wantVerify int
	}{
		{
			name: "authoritative enumeration unavailable",
			list: func(context.Context) ([]model.Org, error) {
				// ListOrgs may return visible rows alongside the sentinel. They
				// are not a partial worklist and must produce zero tenant writes.
				return orgs, store.ErrEnumerationNotAuthoritative
			},
			probe:    &communicationGuardReconcilerProbe{},
			wantBase: store.ErrEnumerationNotAuthoritative,
		},
		{
			name: "second authoritative snapshot fails",
			list: func() sessionOrgLister {
				calls := 0
				return func(context.Context) ([]model.Org, error) {
					calls++
					if calls == 2 {
						return nil, store.ErrEnumerationNotAuthoritative
					}
					return orgs, nil
				}
			}(),
			probe:    &communicationGuardReconcilerProbe{},
			wantBase: store.ErrEnumerationNotAuthoritative, wantStage: 1,
		},
		{
			name:     "staged repair fails",
			list:     func(context.Context) ([]model.Org, error) { return orgs, nil },
			probe:    &communicationGuardReconcilerProbe{stageErr: boom, stageErrAt: 1},
			wantBase: boom, wantStage: 1,
		},
		{
			name:     "enforced verification fails",
			list:     func(context.Context) ([]model.Org, error) { return orgs, nil },
			probe:    &communicationGuardReconcilerProbe{verifyErr: boom, verifyErrAt: 1},
			wantBase: boom, wantStage: 1, wantVerify: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			witness := newCommunicationGuardStoreWitness(test.list, nil, test.probe, func() bool { return true })
			err := witness.ReconcileAndVerify(ctx)
			if !errors.Is(err, test.wantBase) {
				t.Fatalf("ReconcileAndVerify error = %v, want %v", err, test.wantBase)
			}
			if len(test.probe.staged) != test.wantStage || len(test.probe.verified) != test.wantVerify {
				t.Fatalf("staged=%v verified=%v, want counts %d/%d",
					test.probe.staged, test.probe.verified, test.wantStage, test.wantVerify)
			}
			ready, witnessErr := witness.CommunicationStoreReady(ctx)
			if ready || !errors.Is(witnessErr, test.wantBase) {
				t.Fatalf("failed witness = (%v,%v), want false/%v", ready, witnessErr, test.wantBase)
			}
		})
	}
}

func TestCommunicationGuardStoreWitnessRegionScopedRepinAndClearRemainOff(t *testing.T) {
	ctx := context.Background()
	registry, err := residency.NewRegistry("eu", []string{"us"})
	if err != nil {
		t.Fatal(err)
	}
	tenant := model.TenantID(model.NewID())
	probe := &communicationGuardReconcilerProbe{}
	orgs := []model.Org{communicationGuardTestOrg(tenant, model.StatusActive, "us")}
	listCalls := 0
	witness := newCommunicationGuardStoreWitness(
		func(context.Context) ([]model.Org, error) {
			listCalls++
			return append([]model.Org(nil), orgs...), nil
		},
		registry, probe, func() bool { return true },
	)
	for _, pin := range []string{"us", "eu", ""} {
		orgs[0].DataRegion = pin
		if err := witness.ReconcileAndVerify(ctx); !errors.Is(err, store.ErrEnumerationNotAuthoritative) {
			t.Fatalf("region pin %q reconcile error = %v, want unavailable ceremony", pin, err)
		}
		ready, witnessErr := witness.CommunicationStoreReady(ctx)
		if ready || !errors.Is(witnessErr, store.ErrEnumerationNotAuthoritative) {
			t.Fatalf("region pin %q witness = (%v,%v), want OFF/unknown", pin, ready, witnessErr)
		}
	}
	if listCalls != 0 || len(probe.staged) != 0 || len(probe.verified) != 0 {
		t.Fatalf("regional witness did work before ceremony: lists=%d staged=%v verified=%v",
			listCalls, probe.staged, probe.verified)
	}
}

func TestCommunicationGuardStoreWitnessRejectsDuplicateInventoryBeforeWrites(t *testing.T) {
	ctx := context.Background()
	tenant := model.TenantID(model.NewID())
	org := communicationGuardTestOrg(tenant, model.StatusActive, "")
	duplicate := []model.Org{org, org}
	duplicateProbe := &communicationGuardReconcilerProbe{}
	duplicateWitness := newCommunicationGuardStoreWitness(
		func(context.Context) ([]model.Org, error) { return duplicate, nil },
		nil, duplicateProbe, func() bool { return true },
	)
	if err := duplicateWitness.ReconcileAndVerify(ctx); !errors.Is(err, store.ErrEnumerationNotAuthoritative) {
		t.Fatalf("duplicate tenant inventory error = %v", err)
	}
	if len(duplicateProbe.staged) != 0 || len(duplicateProbe.verified) != 0 {
		t.Fatalf("duplicate inventory produced partial work: staged=%v verified=%v",
			duplicateProbe.staged, duplicateProbe.verified)
	}
}

func TestBootWiresCommunicationStoreWitnessButKeepsWP3OffSQLite(t *testing.T) {
	t.Setenv(envCommunicationContentKeyringFile, "")
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test", NoIngest: true,
	})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if eng.sessionsMod == nil {
		t.Fatal("boot did not construct sessions module")
	}

	readiness, err := eng.sessionsMod.EvaluateCommunicationReadiness(ctx)
	if err != nil {
		t.Fatalf("evaluate communication readiness: %v", err)
	}
	// The exact request-authority bundle became the PermissionsReady term in 34adc27bb.
	// Boot wires that bundle deliberately; WP3 remains off because sealer, resolver and pump are
	// still absent. Assert both halves so neither a missing binder nor an accidentally live gate
	// can hide behind the other.
	if !readiness.StoreReady || !readiness.Components.IssuerReady ||
		!readiness.Components.PermissionsReady || readiness.Effective ||
		readiness.Components.SealerReady ||
		readiness.Components.ResolverReady || readiness.Components.PumpReady {
		t.Fatalf("K3 readiness = %+v, want store, issuer and permissions ready with WP3 off", readiness)
	}
	if eng.sessionsMod.CommunicationSessionCredentialsEnabled() {
		t.Fatal("WP2 store witness activated communication credentials")
	}

	var tenant model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{
			Name: "Fresh K3", Slug: "fresh-k3", Status: model.StatusActive,
		})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("create fresh tenant after attestation: %v", err)
	}
	if err := eng.sessionsMod.VerifyCommunicationGuards(ctx, tenant); err != nil {
		t.Fatalf("fresh tenant initializer did not preserve attested invariant: %v", err)
	}
}
