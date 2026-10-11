// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// joinedCheckedParticipants answers participants as allowWorkIdentity does,
// and records and refuses a resolution made inside an owning replay
// transaction.
type joinedCheckedParticipants struct {
	allowWorkIdentity
	calls *joinedPortCalls
}

func (p joinedCheckedParticipants) ResolveParticipant(
	ctx context.Context, tenant model.TenantID, workspace model.ID, kind, ref string,
) (Participant, error) {
	if p.calls.called(ctx, "ResolveParticipant") {
		return Participant{}, errPortCalledInsideReplay
	}
	return p.allowWorkIdentity.ResolveParticipant(ctx, tenant, workspace, kind, ref)
}

// T8: a user-owned WorkItem created and made ready inside an owning replay, as
// the inbound A2A router does, commits within the bound without resolving its
// owner or reading standing inside the owning transaction.
func TestAJoinedWorkItemResolvesNoParticipantInsideTheOwningTransaction(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, filepath.Join(t.TempDir(), "joined-participant.db"), nil)
	defer f.st.Close()
	calls := &joinedPortCalls{tenant: f.tenant}
	f.m.WorkIdentity = joinedCheckedParticipants{calls: calls}
	f.m.Standing = &joinedCheckedStanding{next: f.m.Standing, calls: calls}
	claim := ProtocolReplayClaim{
		WorkspaceID: f.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: "https://joined-participant.example", Kind: ProtocolReplayMessageID,
		ReplayID: "joined-participant-1", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommunicationTimeout)
	defer cancel()
	owner, err := model.ParseID(f.principal.ActorRef)
	if err != nil {
		t.Fatalf("parse the work owner: %v", err)
	}
	plan := ProtocolReplayPlan{
		Accounts:     []model.ID{owner},
		Participants: []ProtocolReplayParticipant{{WorkspaceID: f.workspace, Kind: "user", Ref: f.principal.ActorRef}},
	}
	started := time.Now()
	_, err = f.m.ApplyPreparedProtocolReplay(ctx, f.tenant, claim, plan,
		func(joined context.Context) (ProtocolReplaySettlement, error) {
			created, err := f.m.Apply(joined, f.tenant, f.principal, baseCreateCommand(f, "Joined participant"))
			if err != nil {
				return ProtocolReplaySettlement{}, err
			}
			_, err = f.m.Apply(joined, f.tenant, f.principal, WorkCommand{
				Command: "item.ready", WorkItemID: created.ResultID,
				ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(),
				HTTPMethod: http.MethodPost, CommandScope: "workflow:joined:ready",
			})
			return ProtocolReplaySettlement{}, err
		})
	elapsed := time.Since(started)
	if inside := calls.insideCalls(); len(inside) != 0 {
		t.Fatalf("ports called inside the owning replay transaction: %v (the replay answered %v after %s)",
			inside, err, elapsed.Round(time.Millisecond))
	}
	if err != nil || elapsed >= joinedReplayBound {
		t.Fatalf("joined work item answered %v after %s, want it committed within %s",
			err, elapsed.Round(time.Millisecond), joinedReplayBound)
	}
	if items := workCount(t, f, workItemKind); items != 1 {
		t.Fatalf("work items after the joined create = %d, want 1", items)
	}
}
