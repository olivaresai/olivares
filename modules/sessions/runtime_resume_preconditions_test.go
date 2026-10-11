// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SES-RESUME-005: none of these refusals may reserve even a temporary launch or
// rotate a claim. A failed revocation must retain just that handle for retry.
func TestRuntimeResumePreconditions(t *testing.T) {
	t.Parallel()
	for _, state := range []string{stateStopped, stateFailed} {
		for _, tc := range []struct {
			name                 string
			stop, noWork, noComm bool
			failWork, failComm   bool
			status               int
		}{
			{name: "emergency stop", stop: true, status: http.StatusForbidden},
			{name: "missing work issuer", noWork: true, status: http.StatusServiceUnavailable},
			{name: "missing communication issuer", noComm: true, status: http.StatusServiceUnavailable},
			{name: "work revoke fails", failWork: true},
			{name: "communication revoke fails", failComm: true},
			{name: "both revokes fail", failWork: true, failComm: true},
			{name: "both revokes succeed"},
		} {
			t.Run(state+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				runner := &fakeRunner{}
				stop := &flipStopGate{}
				gate := &recordingLaunchGate{dec: LaunchDecision{Allowed: true}}
				m, st, tenant, clk := newRuntimeHarness(t, WithRunner(runner),
					WithCredentialSource(staticCred()), WithStopGate(stop), WithLaunchGate(gate))
				probe := &dualCredentialProbe{now: clk.get}
				wireDualCredentialProbe(m, probe)
				created, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
					Transport: TransportStreamJSON, Isolation: IsolationNative,
					Actor: "agent:resume", ActorKind: model.ActorAgent, AgentRef: "agent:resume",
				})
				if err != nil {
					t.Fatal(err)
				}
				original, err := m.loadRun(ctx, tenant, created.RunRef)
				if err != nil {
					t.Fatal(err)
				}
				credentials := runtimeCredentialsFromRecord(tenant, original)
				if credentials.work.ID.IsZero() || credentials.communication.ID.IsZero() {
					t.Fatal("fixture did not persist both credential handles")
				}
				if _, err := m.stopRun(ctx, tenant, created.RunRef, "test", model.ActorUser); err != nil {
					t.Fatal(err)
				}
				// Model a terminated process whose credential storage was unavailable:
				// put its exact durable handles and binding back on the terminal row.
				mutateRunForCredentialTest(t, m.Data, tenant, created.RunRef, func(rec model.Record) {
					rec[colState] = state
					setRuntimeCredentialStamp(rec, Lease{SID: credentials.work.SessionRef}, credentials)
				})
				before, err := m.loadRun(ctx, tenant, created.RunRef)
				if err != nil {
					t.Fatal(err)
				}
				events := eventNames(listRunEvents(t, st, tenant, created.RunRef))
				probe.reset()
				gate.called = false
				if tc.stop {
					stop.flip()
				}
				if tc.noWork {
					m.rt.WorkSessionCreds = nil
				}
				if tc.noComm {
					m.CommunicationSessionCreds = nil
				}
				workErr, commErr := errors.New("work revoke unavailable"), errors.New("communication revoke unavailable")
				probe.mu.Lock()
				if tc.failWork {
					probe.workRevokeErrs = []error{workErr}
				}
				if tc.failComm {
					probe.commRevokeErrs = []error{commErr}
				}
				probe.mu.Unlock()
				resume := func() error {
					_, err := m.resumeRun(ctx, tenant, created.RunRef, "agent:resume", model.ActorAgent, "agent:resume")
					return err
				}
				err = resume()
				refused := tc.status != 0 || tc.failWork || tc.failComm
				if refused {
					if err == nil {
						t.Fatal("unsafe resume was accepted")
					}
					if tc.status != 0 {
						var re *runErr
						if !errors.As(err, &re) || re.status != tc.status {
							t.Fatalf("resume error = %v, want status %d", err, tc.status)
						}
					}
					if tc.failWork && !errors.Is(err, workErr) || tc.failComm && !errors.Is(err, commErr) {
						t.Fatalf("resume lost the revocation failure: %v", err)
					}
					after, loadErr := m.loadRun(ctx, tenant, created.RunRef)
					if loadErr != nil {
						t.Fatal(loadErr)
					}
					for _, column := range []string{colState, colRuntimeLaunchID, colClaimHolder, colClaimFence} {
						if after[column] != before[column] {
							t.Errorf("refusal changed %s: %v -> %v", column, before[column], after[column])
						}
					}
					if got := eventNames(listRunEvents(t, st, tenant, created.RunRef)); !slices.Equal(got, events) {
						t.Errorf("refusal wrote lifecycle events: %v -> %v", events, got)
					}
					if _, live, claimErr := m.ActiveClaim(ctx, tenant, credentials.work.SessionRef); claimErr != nil || live {
						t.Errorf("refusal took a claim: live=%v err=%v", live, claimErr)
					}
					if err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
						claim, found, err := findClaim(ctx, sc, credentials.work.SessionRef)
						if err == nil && (!found || claim.Int(colFence) != credentials.work.ClaimFence || claim.String(colClaimState) != claimReleased) {
							t.Error("refusal changed the released claim generation")
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
					calls := probe.snapshot()
					if gate.called || launchCount(runner) != 1 || calls.workMint != 0 || calls.commMint != 0 {
						t.Fatal("refusal reached admission, mint, or process launch")
					}
					for column, retained := range map[string]bool{
						colWorkCredentialID:          tc.status != 0 || tc.failWork,
						colCommunicationCredentialID: tc.status != 0 || tc.failComm,
					} {
						want := ""
						if retained {
							want = before.String(column)
						}
						if after.String(column) != want {
							t.Errorf("%s = %q, want %q", column, after.String(column), want)
						}
					}
					if tc.status != 0 {
						if calls.workRevoke != 0 || calls.commRevoke != 0 {
							t.Fatal("stop or missing wiring reached credential revocation")
						}
						return
					}
					if calls.workRevoke != 1 || calls.commRevoke != 1 {
						t.Fatal("a revocation failure prevented the other handle's cleanup")
					}
					// A second attempt must retry only failed handles and then resume.
					err = resume()
				}
				if err != nil {
					t.Fatalf("resume after cleanup: %v", err)
				}
				calls := probe.snapshot()
				wantWork, wantComm := 1, 1
				if tc.failWork {
					wantWork++
				}
				if tc.failComm {
					wantComm++
				}
				if calls.workRevoke != wantWork || calls.commRevoke != wantComm || calls.workMint != 1 || calls.commMint != 1 {
					t.Fatalf("cleanup/mint counts = %d/%d/%d/%d, want %d/%d/1/1",
						calls.workRevoke, calls.commRevoke, calls.workMint, calls.commMint, wantWork, wantComm)
				}
				wantEvents := []string{"work.revoke", "communication.revoke"}
				if tc.failWork {
					wantEvents = append(wantEvents, "work.revoke")
				}
				if tc.failComm {
					wantEvents = append(wantEvents, "communication.revoke")
				}
				wantEvents = append(wantEvents, "communication.mint", "work.mint")
				if !slices.Equal(calls.events, wantEvents) {
					t.Fatalf("mint preceded leftover cleanup: %v", calls.events)
				}
				probe.mu.Lock()
				workIDs, commIDs := slices.Clone(probe.workRevoke), slices.Clone(probe.commRevoke)
				probe.mu.Unlock()
				for _, id := range workIDs {
					if id != credentials.work.ID {
						t.Errorf("work revoke targeted %s, want the leftover handle %s", id, credentials.work.ID)
					}
				}
				for _, id := range commIDs {
					if id != credentials.communication.ID {
						t.Errorf("communication revoke targeted %s, want the leftover handle %s", id, credentials.communication.ID)
					}
				}
				for _, req := range calls.workRevokeRequests {
					if req.Tenant != tenant || req.RunRef != created.RunRef || req.SessionRef != credentials.work.SessionRef ||
						req.AgentRef != credentials.work.AgentRef || req.ClaimFence != credentials.work.ClaimFence {
						t.Errorf("work revoke lost the previous binding: %+v", req)
					}
				}
				for _, req := range calls.commRevokeRequests {
					if req != communicationCredentialRequest(credentials.communication) {
						t.Errorf("communication revoke lost the previous binding: %+v", req)
					}
				}
				after, err := m.loadRun(ctx, tenant, created.RunRef)
				if err != nil {
					t.Fatal(err)
				}
				if launchCount(runner) != 2 || !gate.called || after.Int(colClaimFence) <= before.Int(colClaimFence) {
					t.Fatal("successful cleanup did not admit one successor with a new claim generation")
				}
				for _, column := range []string{colRuntimeLaunchID, colWorkCredentialID, colCommunicationCredentialID} {
					if after.String(column) == "" || after.String(column) == before.String(column) {
						t.Errorf("resume did not replace %s", column)
					}
				}
			})
		}
	}
}

func TestLifecycleHTTP_ResumeUnderEmergencyStop(t *testing.T) {
	m, runner, h, admin, tenant, ref := lifecycleHarness(t, "resume-emergency-stop")
	if got := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, nil, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("stop = %d", got.code)
	}
	WithStopGate(&flipStopGate{stopped: true})(m)
	before, err := m.loadRun(context.Background(), tenant, ref)
	if err != nil {
		t.Fatal(err)
	}
	got := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/resume", admin, nil, tenantHdr(tenant))
	if got.code != http.StatusForbidden {
		t.Fatalf("resume under emergency stop = %d, want 403", got.code)
	}
	after, err := m.loadRun(context.Background(), tenant, ref)
	if err != nil {
		t.Fatal(err)
	}
	if launchCount(runner) != 1 || after.String(colState) != stateStopped ||
		after.Int(colLastEventSeq) != before.Int(colLastEventSeq) ||
		after.String(colRuntimeLaunchID) != before.String(colRuntimeLaunchID) {
		t.Fatal("HTTP refusal launched a successor or reserved the stopped session")
	}
}
