// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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

// createCommunicationGuardTestTenant creates one business tenant through the
// booted engine's System seam, which is the production path whose initializer
// seeds the communication workspace guards atomically with the org.
func createCommunicationGuardTestTenant(t *testing.T, eng *engine, slug string) model.TenantID {
	t.Helper()
	ctx := context.Background()
	var tenant model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("create tenant %q: %v", slug, err)
	}
	return tenant
}

// bootCommunicationStoreProof asserts that boot bound the composite store
// proof without any activation request and returns the proof it measured at
// the promotion barrier.
func bootCommunicationStoreProof(t *testing.T, phase string, eng *engine) communicationStoreProof {
	t.Helper()
	if eng.sessionsMod == nil {
		t.Fatalf("%s: boot did not construct the sessions module", phase)
	}
	if eng.communicationComposition == nil || eng.communicationComposition.store == nil {
		t.Fatalf("%s: boot did not bind the composite communication store proof", phase)
	}
	if eng.communicationComposition.activation.Requested ||
		len(eng.communicationComposition.custodyBlockers()) != 0 {
		t.Fatalf("%s: activation was not requested, yet the composition recorded %+v",
			phase, eng.communicationComposition.activation)
	}
	return eng.communicationComposition.store.Proof()
}

// assertCommunicationWP3OffTerms asserts every K3 readiness term separately for
// a boot with activation and custody unset. The store term is the only one that
// changes between the staged and the enforced phase; the rest are fixed by the
// composition contract (bindCommunicationComposition): issuer, resolver and the
// request-authority bundle are bound on every boot, while credentials, the
// sealer, the pump witness and the cursor keyring stay OFF because nothing
// requested them. Each binder-level fact is also read directly from the
// module and the engine so a missing binder or an accidental activation cannot
// hide behind the projected Effective=false.
func assertCommunicationWP3OffTerms(t *testing.T, phase string, eng *engine, wantStore bool) {
	t.Helper()
	ctx := context.Background()
	readiness, err := eng.sessionsMod.EvaluateCommunicationReadiness(ctx)
	if err != nil {
		t.Fatalf("%s: evaluate communication readiness: %v", phase, err)
	}
	wantTerms := sessions.CommunicationReadinessComponents{
		StoreReady: wantStore, IssuerReady: true, SealerReady: false,
		ResolverReady: true, PermissionsReady: true, PumpReady: false,
	}
	if readiness.Components != wantTerms {
		t.Fatalf("%s: K3 readiness terms = %+v, want %+v", phase, readiness.Components, wantTerms)
	}
	wantMissing := []sessions.CommunicationReadinessDependency{
		sessions.CommunicationReadinessSealer, sessions.CommunicationReadinessPump,
	}
	if !wantStore {
		wantMissing = append([]sessions.CommunicationReadinessDependency{sessions.CommunicationReadinessStore}, wantMissing...)
	}
	if readiness.StoreReady != wantStore || readiness.Effective || readiness.CompositionReady ||
		readiness.Verdict != sessions.VerdictUnknown || readiness.Code != "communication_not_ready" ||
		!slices.Equal(readiness.Missing, wantMissing) || len(readiness.Unavailable) != 0 {
		t.Fatalf("%s: K3 readiness = %+v, want store=%t, WP3 off with missing=%v and nothing unavailable",
			phase, readiness, wantStore, wantMissing)
	}
	if eng.sessionsMod.CommunicationSessionCredentialsEnabled() {
		t.Fatalf("%s: communication credentials were enabled without an activation request", phase)
	}
	if eng.sessionsMod.CommunicationCursorTokenKeyringBound() {
		t.Fatalf("%s: a cursor keyring is bound although none was configured", phase)
	}
	if !eng.sessionsMod.WorkOutboxClaimAuthorityBound() {
		t.Fatalf("%s: the outbox claim authority is not bound on the module", phase)
	}
	if eng.communicationComposition.pumpWitness() != nil {
		t.Fatalf("%s: a K3 pump witness was composed without an activation request", phase)
	}
	if eng.communicationPump == nil || eng.communicationPump.communication != nil ||
		eng.communicationPump.authority == nil {
		t.Fatalf("%s: the registered pump must carry the authority and no K3 witness", phase)
	}
	if verdict := eng.communicationPump.authority.current(ctx); verdict.allow || verdict.reason != "communication_pump_unbound" {
		t.Fatalf("%s: outbox authority verdict = %+v, want the unbound K3 lane refused", phase, verdict)
	}
}

// TestBootWiresCommunicationStoreWitnessButKeepsWP3OffSQLite proves the two
// store phases boot actually declares on a fresh SQLite estate with activation
// and every K3 custody setting unset:
//
//   - staged: a fresh estate starts with the directory writer control staged
//     at generation 1, so the composite proof (communicationstoreproof.go)
//     names exactly the two activation blockers and StoreReady is false, even
//     though the guard estate ceremony and the schema proof already passed at
//     the promotion barrier;
//   - enforced: after the operator ceremony `olivares db
//     activate-directory-writer` on the CLOSED store and a reopen, the same
//     proof is ready with no blockers and StoreReady is true.
//
// WP3 stays OFF in both phases and is asserted term by term: the resolver and
// the request-authority bundle are bound deliberately on every boot
// (ea711842e5 composed K3 lot A; 34adc27bb made the bundle the PermissionsReady
// term), while credentials, the sealer, the pump witness and the cursor keyring
// stay unbound because nothing requested them. The fresh-tenant initializer
// invariant is proved in both phases and the promotion proof of the enforced
// phase covers the tenant created while staged.
func TestBootWiresCommunicationStoreWitnessButKeepsWP3OffSQLite(t *testing.T) {
	t.Setenv(envKeyWrap, "")
	t.Setenv(envCommunicationActivation, "")
	t.Setenv(envCommunicationContentKeyringFile, "")
	t.Setenv(envCommunicationCursorKeyringFile, "")
	ctx := context.Background()
	dir := t.TempDir()

	// Phase 1: fresh estate, writer control staged at generation 1.
	var eng *engine
	t.Cleanup(func() {
		if eng != nil {
			_ = eng.Close()
		}
	})
	eng = bootForComposition(t, dir)
	proof := bootCommunicationStoreProof(t, "staged", eng)
	wantStagedBlockers := []string{
		"writer_control_not_enforced: mode=\"staged\" (run `olivares db activate-directory-writer` and reopen)",
		"expected_generation_below_activation: 1",
	}
	// The VERY FIRST boot of a fresh estate opens the store before SYSTEM genesis.
	// The directory status is a boot witness taken at that point, so even though
	// promotion then provisions SYSTEM and the composite proof runs after it, the
	// proof honestly reports the inventory coverage it could attest at boot:
	// incomplete, awaiting the SYSTEM bootstrap, on top of the two activation
	// blockers. Coverage becomes a fact of the NEXT boot; it is proved below.
	wantFirstBootBlockers := []string{
		wantStagedBlockers[0],
		"directory_epoch_coverage_incomplete",
		wantStagedBlockers[1],
	}
	if proof.Ready || !proof.Supported || proof.ControlMode != store.DirectoryControlStaged ||
		proof.WriterPosture != store.DirectoryWriterSQLiteCapability || proof.ExpectedGeneration != 1 ||
		proof.EpochCoverageComplete || !proof.GuardVerified || !proof.SchemaVerified || proof.TenantsProved != 0 ||
		!slices.Equal(proof.Blockers, wantFirstBootBlockers) {
		t.Fatalf("first-boot staged store proof = %+v, want guard and schema verified but unready with exactly %q",
			proof, wantFirstBootBlockers)
	}
	assertCommunicationWP3OffTerms(t, "staged", eng, false)
	// The initializer seeds the guards of a tenant created on the staged estate
	// so the enforced, verify-only pass holds without a repair.
	stagedTenant := createCommunicationGuardTestTenant(t, eng, "fresh-k3-staged")
	if err := eng.sessionsMod.VerifyCommunicationGuards(ctx, stagedTenant); err != nil {
		t.Fatalf("staged: fresh tenant initializer did not preserve the guard invariant: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close first staged engine: %v", err)
	}
	eng = nil

	// Phase 1b: the staged estate reopened after genesis. No ceremony has run, so
	// the control is still staged at generation 1 — but the inventory is now
	// complete and the composite proof names EXACTLY the two activation blockers,
	// with the one staged-phase tenant proved. This is the assertion the previous
	// revision made on the first boot; it holds from the second boot on.
	eng = bootForComposition(t, dir)
	proof = bootCommunicationStoreProof(t, "staged-reopened", eng)
	if proof.Ready || !proof.Supported || proof.ControlMode != store.DirectoryControlStaged ||
		proof.WriterPosture != store.DirectoryWriterSQLiteCapability || proof.ExpectedGeneration != 1 ||
		!proof.EpochCoverageComplete || !proof.GuardVerified || !proof.SchemaVerified || proof.TenantsProved != 1 ||
		!slices.Equal(proof.Blockers, wantStagedBlockers) {
		t.Fatalf("reopened staged store proof = %+v, want coverage complete over the one tenant but unready with exactly %q",
			proof, wantStagedBlockers)
	}
	assertCommunicationWP3OffTerms(t, "staged-reopened", eng, false)
	if err := eng.sessionsMod.VerifyCommunicationGuards(ctx, stagedTenant); err != nil {
		t.Fatalf("staged reopen: the staged-phase tenant no longer verifies: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close staged engine: %v", err)
	}
	eng = nil

	// The only production path from staged to enforced: the explicit ceremony
	// on the stopped store. Its result is a boot witness, so the enforced
	// control is observable only after a reopen.
	out, err := runDB(t, "activate-directory-writer", "--data-dir", dir, "--expected-generation", "1",
		"--actor", "boot-readiness-test", "--reason", "serve stopped for activation",
		"--writers-upgraded", "--writers-drained", "--format", "json")
	if err != nil {
		t.Fatalf("activation ceremony: %v\n%s", err, out)
	}
	var ceremony directoryActivationResult
	if err := json.Unmarshal([]byte(out), &ceremony); err != nil {
		t.Fatalf("activation ceremony JSON: %v\n%s", err, out)
	}
	if !ceremony.Changed || !ceremony.ReopenRequired || ceremony.Error != "" ||
		ceremony.Before.ControlMode != string(store.DirectoryControlStaged) || ceremony.Before.ExpectedGeneration != 1 ||
		ceremony.After.ControlMode != string(store.DirectoryControlEnforced) || ceremony.After.ExpectedGeneration != 2 {
		t.Fatalf("activation ceremony result = %+v, want staged 1 -> enforced 2 with reopen required", ceremony)
	}

	// Phase 2: reopen on the enforced control.
	eng = bootForComposition(t, dir)
	proof = bootCommunicationStoreProof(t, "enforced", eng)
	if !proof.Ready || len(proof.Blockers) != 0 || !proof.Supported ||
		proof.ControlMode != store.DirectoryControlEnforced || proof.ExpectedGeneration != 2 ||
		proof.WriterPosture != store.DirectoryWriterSQLiteCapability || !proof.EpochCoverageComplete ||
		!proof.GuardVerified || !proof.SchemaVerified || proof.TenantsProved != 1 {
		t.Fatalf("enforced store proof = %+v, want ready over the one staged-phase tenant with no blockers", proof)
	}
	assertCommunicationWP3OffTerms(t, "enforced", eng, true)
	// The attested phase: the store proof is established, and a tenant created
	// now must still satisfy the enforced, verify-only pass through its
	// initializer alone, while the tenant covered by the promotion proof keeps
	// verifying.
	enforcedTenant := createCommunicationGuardTestTenant(t, eng, "fresh-k3-enforced")
	if err := eng.sessionsMod.VerifyCommunicationGuards(ctx, enforcedTenant); err != nil {
		t.Fatalf("enforced: fresh tenant initializer did not preserve the attested invariant: %v", err)
	}
	if err := eng.sessionsMod.VerifyCommunicationGuards(ctx, stagedTenant); err != nil {
		t.Fatalf("enforced: tenant covered by the promotion proof failed verification: %v", err)
	}
}
