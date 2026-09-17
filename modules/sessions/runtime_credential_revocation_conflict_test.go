// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// Concurrency regressions for the runtime credential revocation.
//
// The mechanism, the engine asymmetry and the measurement are stated ONCE, at
// retryOnceOnVersionConflict in runtime_communication_credential.go; this file
// does not restate them. What it pins is what the runtime owes when the store
// answers a revocation with a version conflict: resolve it, bound the resolving,
// and keep reporting the conflicts it cannot resolve.
//
// The symptom these came from: CI run 35004444187, job 104501030543, cmd/olivares
// TestBootWorkLaunchAuthorityPostgres — `StopForWork: work_stop_ambiguous:
// sessions: compensation revoke work-session credential: sessions: work credential
// revoke failed`, for a credential that WAS revoked by the other in-process caller
// microseconds earlier.

// TestRuntimeWorkSessionCredentialRevokeAbsorbsOneVersionConflict is the red the
// CI failure earned: one lost compare-and-swap must resolve into a revoked
// credential, not into a compensation error.
func TestRuntimeWorkSessionCredentialRevokeAbsorbsOneVersionConflict(t *testing.T) {
	t.Parallel()

	m := New()
	spy := &workSessionCredentialSpy{revokeErrs: []error{store.ErrConflict}}
	m.UseWorkSessionCredentialSource(spy)
	lr := &liveRun{
		tenant: "tenant-revoke-conflict", runRef: "run-revoke-conflict", agentRef: "agent:driver",
		claim:            Lease{SID: "sid-revoke-conflict", Holder: "agent:driver", Fence: 3},
		workCredentialID: "work-cred-conflict", workCredentialNotAfter: farFuture,
	}

	if err := m.revokeLiveWorkSessionCredential(context.Background(), lr); err != nil {
		t.Fatalf("a single lost version CAS was reported as a revocation failure: %v", err)
	}
	lr.mu.Lock()
	id, deadline := lr.workCredentialID, lr.workCredentialNotAfter
	lr.mu.Unlock()
	if !id.IsZero() || !deadline.IsZero() {
		t.Fatalf("absorbed conflict retained the handle/deadline: %q / %s", id, deadline)
	}
	calls := spy.snapshot()
	if len(calls.revokeIDs) != 2 || calls.revokeIDs[0] != "work-cred-conflict" ||
		calls.revokeIDs[1] != "work-cred-conflict" {
		t.Fatalf("revoke attempts = %v, want the same handle exactly twice", calls.revokeIDs)
	}
	// The retry must re-present the EXACT binding: a second attempt that relaxed
	// the session/run/fence facts would be a wider revoker than the first.
	if len(calls.revokeRequests) != 2 || calls.revokeRequests[0] != calls.revokeRequests[1] {
		t.Fatalf("revoke binding changed across the retry: %+v", calls.revokeRequests)
	}
}

// TestRuntimeWorkSessionCredentialRevokeReportsPersistentConflict is the other
// half, and the one that keeps the retry honest: contention this runtime cannot
// resolve is still an error, the handle is still retained for a later attempt, and
// the retry is bounded at exactly one.
func TestRuntimeWorkSessionCredentialRevokeReportsPersistentConflict(t *testing.T) {
	t.Parallel()

	m := New()
	spy := &workSessionCredentialSpy{revokeErrs: []error{store.ErrConflict, store.ErrConflict, nil}}
	m.UseWorkSessionCredentialSource(spy)
	lr := &liveRun{
		tenant: "tenant-revoke-stuck", runRef: "run-revoke-stuck", agentRef: "agent:driver",
		claim:            Lease{SID: "sid-revoke-stuck", Holder: "agent:driver", Fence: 4},
		workCredentialID: "work-cred-stuck", workCredentialNotAfter: farFuture,
	}

	err := m.revokeLiveWorkSessionCredential(context.Background(), lr)
	if err == nil {
		t.Fatal("a conflict that survived the retry was reported as success")
	}
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("persistent conflict = %v, want it to remain a store conflict", err)
	}
	lr.mu.Lock()
	id := lr.workCredentialID
	lr.mu.Unlock()
	if id != "work-cred-stuck" {
		t.Fatalf("a failed revocation discarded the retry handle: %q", id)
	}
	if calls := spy.snapshot(); len(calls.revokeIDs) != 2 {
		t.Fatalf("revoke attempts = %d, want exactly two (one retry, never a loop)", len(calls.revokeIDs))
	}
}

// TestRuntimeCredentialSetRevocationAbsorbsOneVersionConflict drives the call the
// terminal stop actually makes — the dual K3 set — and proves BOTH halves absorb
// their own lost CAS independently. The work half is the one CI caught; the
// communication half revokes the same way against the same store and would have
// produced the same false ambiguity on a different interleaving.
func TestRuntimeCredentialSetRevocationAbsorbsOneVersionConflict(t *testing.T) {
	t.Parallel()

	for _, half := range []struct {
		name               string
		arm                func(*dualCredentialProbe)
		wantWork, wantComm int
	}{
		{"work", func(p *dualCredentialProbe) { p.workRevokeErrs = []error{store.ErrConflict} }, 2, 1},
		{"communication", func(p *dualCredentialProbe) { p.commRevokeErrs = []error{store.ErrConflict} }, 1, 2},
	} {
		t.Run(half.name, func(t *testing.T) {
			t.Parallel()

			m := New()
			probe := &dualCredentialProbe{}
			half.arm(probe)
			m.UseWorkSessionCredentialSource(dualWorkSource{probe})
			m.UseCommunicationSessionCredentialSource(dualCommunicationSource{probe})
			m.EnableCommunicationSessionCredentials()
			lr := &liveRun{
				tenant: "tenant-set-conflict", runRef: "run-set-conflict", agentRef: "agent:driver",
				claim:                           Lease{SID: "sid-set-conflict", Holder: "agent:driver", Fence: 5},
				workCredentialID:                "work-cred-set",
				workCredentialNotAfter:          farFuture,
				communicationCredentialID:       "communication-cred-set",
				communicationCredentialNotAfter: farFuture,
				communicationWorkspaceID:        "workspace-set",
			}

			if err := m.revokeLiveRuntimeCredentials(context.Background(), lr); err != nil {
				t.Fatalf("one lost CAS on the %s half failed the whole revocation: %v", half.name, err)
			}
			lr.mu.Lock()
			workID, commID := lr.workCredentialID, lr.communicationCredentialID
			lr.mu.Unlock()
			if !workID.IsZero() || !commID.IsZero() {
				t.Fatalf("handles retained after an absorbed conflict: work %q communication %q", workID, commID)
			}
			calls := probe.snapshot()
			if calls.workRevoke != half.wantWork || calls.commRevoke != half.wantComm {
				t.Fatalf("revocations = work %d communication %d, want %d/%d",
					calls.workRevoke, calls.commRevoke, half.wantWork, half.wantComm)
			}
		})
	}
}
