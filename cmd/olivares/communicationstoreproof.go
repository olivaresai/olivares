// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// communicationStoreProof is the complete, non-secret K3 store-phase witness
// the composition root logs and the readiness term consumes. It names each
// fact separately so an operator can see WHICH one is missing instead of a
// bare false: the directory status capability and its enforced control, the
// activation generation, the accurately named writer posture, the guard
// estate proof, the schema reachability per tenant and every tenant epoch.
type communicationStoreProof struct {
	Supported             bool                         `json:"supported"`
	ControlMode           store.DirectoryControlMode   `json:"control_mode,omitempty"`
	WriterPosture         store.DirectoryWriterPosture `json:"writer_posture,omitempty"`
	EpochCoverageComplete bool                         `json:"epoch_coverage_complete"`
	ExpectedGeneration    int64                        `json:"expected_generation"`
	// HistoricalEnabled is DirectoryStatus.Enabled, the boot witness flag the
	// store keeps deliberately false. It is reported, never required: making it
	// a prerequisite of the readiness that is supposed to earn it would be
	// circular, and redefining it here would lie about the store.
	HistoricalEnabled bool      `json:"historical_enabled"`
	GuardVerified     bool      `json:"guard_verified"`
	SchemaVerified    bool      `json:"schema_verified"`
	TenantsProved     int       `json:"tenants_proved"`
	Ready             bool      `json:"ready"`
	Blockers          []string  `json:"blockers,omitempty"`
	ObservedAt        time.Time `json:"observed_at"`
}

// communicationStoreProofWitness composes the historical session-guard
// witness with the directory status, schema and epoch proofs into ONE store
// readiness term. It is rebuilt at every leadership promotion and, like the
// guard witness it wraps, answers false the moment this node is not the
// published leader.
type communicationStoreProofWitness struct {
	st       store.Store
	guard    *communicationGuardStoreWitness
	sessions *sessions.Module
	listOrgs sessionOrgLister
	isLeader func() bool
	now      func() time.Time

	runMu   sync.Mutex
	mu      sync.RWMutex
	proof   communicationStoreProof
	lastErr error
}

func newCommunicationStoreProofWitness(
	st store.Store,
	guard *communicationGuardStoreWitness,
	sm *sessions.Module,
	listOrgs sessionOrgLister,
	isLeader func() bool,
	now func() time.Time,
) *communicationStoreProofWitness {
	if now == nil {
		now = time.Now
	}
	return &communicationStoreProofWitness{
		st: st, guard: guard, sessions: sm, listOrgs: listOrgs, isLeader: isLeader, now: now,
	}
}

// ReconcileAndVerify runs the guard estate ceremony and then proves the
// remaining store facts. A guard failure is retained as its own blocker and
// does not stop the other proofs from being measured and reported; the
// combined verdict is ready only when nothing blocks.
func (w *communicationStoreProofWitness) ReconcileAndVerify(ctx context.Context) error {
	if w == nil || w.st == nil || w.sessions == nil || w.listOrgs == nil {
		return store.ErrStoreUnavailable
	}
	w.runMu.Lock()
	defer w.runMu.Unlock()
	w.mu.Lock()
	w.proof, w.lastErr = communicationStoreProof{}, nil
	w.mu.Unlock()

	var guardErr error
	if w.guard != nil {
		guardErr = w.guard.ReconcileAndVerify(ctx)
	} else {
		guardErr = errors.New("communication guard witness is not bound")
	}
	proof, proveErr := w.prove(ctx, guardErr == nil)
	if guardErr != nil {
		proof.Blockers = append(proof.Blockers, "guard: "+guardErr.Error())
		proof.Ready = false
	}
	err := errors.Join(guardErr, proveErr)
	w.mu.Lock()
	w.proof, w.lastErr = proof, err
	w.mu.Unlock()
	if err != nil {
		return err
	}
	if !proof.Ready {
		return fmt.Errorf("communication store proof is not ready: %v", proof.Blockers)
	}
	return nil
}

func (w *communicationStoreProofWitness) prove(ctx context.Context, guardVerified bool) (communicationStoreProof, error) {
	proof := communicationStoreProof{GuardVerified: guardVerified, ObservedAt: w.now().UTC()}
	statuser, ok := w.st.(store.DirectoryStatuser)
	if !ok {
		proof.Blockers = append(proof.Blockers, "directory_status_unsupported")
		return proof, nil
	}
	status, supported, err := statuser.DirectoryStatus(ctx)
	if err != nil {
		return proof, fmt.Errorf("directory status: %w", err)
	}
	proof.Supported = supported
	if !supported {
		proof.Blockers = append(proof.Blockers, "directory_status_unsupported")
		return proof, nil
	}
	proof.ControlMode, proof.WriterPosture = status.ControlMode, status.WriterPosture
	proof.EpochCoverageComplete, proof.ExpectedGeneration = status.EpochCoverageComplete, status.ExpectedGeneration
	proof.HistoricalEnabled = status.Enabled
	if status.ControlMode != store.DirectoryControlEnforced {
		proof.Blockers = append(proof.Blockers,
			fmt.Sprintf("writer_control_not_enforced: mode=%q (run `olivares db activate-directory-writer` and reopen)",
				status.ControlMode))
	}
	if !status.EpochCoverageComplete {
		proof.Blockers = append(proof.Blockers, "directory_epoch_coverage_incomplete")
	}
	if status.ExpectedGeneration < 2 {
		proof.Blockers = append(proof.Blockers,
			fmt.Sprintf("expected_generation_below_activation: %d", status.ExpectedGeneration))
	}
	switch status.WriterPosture {
	case store.DirectoryWriterSplitOwner, store.DirectoryWriterSingleRoleCapability,
		store.DirectoryWriterSQLiteCapability:
	default:
		proof.Blockers = append(proof.Blockers, fmt.Sprintf("writer_posture_unknown: %q", status.WriterPosture))
	}
	if !guardVerified {
		proof.Blockers = append(proof.Blockers, "guard_estate_unverified")
	}

	orgs, err := w.listOrgs(ctx)
	if err != nil {
		return proof, fmt.Errorf("enumerate tenants for communication store proof: %w", err)
	}
	schemaVerified := true
	for _, org := range orgs {
		tenant, parseErr := model.ParseTenantID(org.ID.String())
		if parseErr != nil || tenant.IsZero() || org.TenantID != tenant {
			return proof, fmt.Errorf("%w: communication store proof inventory contains invalid tenant lineage %q/%q",
				store.ErrEnumerationNotAuthoritative, org.ID, org.TenantID)
		}
		if tenant.IsSystem() {
			continue
		}
		if err := w.sessions.VerifyCommunicationSchema(ctx, tenant); err != nil {
			schemaVerified = false
			proof.Blockers = append(proof.Blockers, fmt.Sprintf("schema:%s: %v", tenant, err))
			continue
		}
		epoch, err := w.readTenantEpoch(ctx, tenant)
		if err != nil {
			proof.Blockers = append(proof.Blockers, fmt.Sprintf("epoch:%s: %v", tenant, err))
			continue
		}
		if epoch < 1 {
			proof.Blockers = append(proof.Blockers, fmt.Sprintf("epoch:%s: below one", tenant))
			continue
		}
		proof.TenantsProved++
	}
	proof.SchemaVerified = schemaVerified
	proof.Ready = len(proof.Blockers) == 0
	return proof, nil
}

func (w *communicationStoreProofWitness) readTenantEpoch(ctx context.Context, tenant model.TenantID) (int64, error) {
	var version int64
	err := w.st.View(ctx, tenant, func(sc store.Scope) error {
		reader, ok := sc.(store.DirectorySnapshotReader)
		if !ok {
			return fmt.Errorf("%w: scope lacks the directory snapshot reader", store.ErrDirectoryUnavailable)
		}
		epoch, err := reader.ReadDirectoryEpoch(ctx)
		if err != nil {
			return err
		}
		version = epoch.Version
		return nil
	})
	return version, err
}

// CommunicationStoreReady implements sessions.CommunicationStoreReadinessWitness.
func (w *communicationStoreProofWitness) CommunicationStoreReady(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if w == nil {
		return false, nil
	}
	if w.isLeader != nil && !w.isLeader() {
		return false, nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.proof.Ready && w.lastErr == nil, w.lastErr
}

// Proof returns the last measured proof for logs and reports.
func (w *communicationStoreProofWitness) Proof() communicationStoreProof {
	if w == nil {
		return communicationStoreProof{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	proof := w.proof
	proof.Blockers = append([]string(nil), proof.Blockers...)
	return proof
}

var _ sessions.CommunicationStoreReadinessWitness = (*communicationStoreProofWitness)(nil)
