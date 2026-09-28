// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// workAccountOwner makes the work fixture's principal a real account admitted
// to the tenant, so a fenced write naming it pins a real authority version.
// The fixture's default principal names no account, which a fence skips.
func workAccountOwner(t *testing.T, f *workFixture) model.ID {
	t.Helper()
	ctx := context.Background()
	var user model.User
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		user, err = as.Users().Create(ctx, model.User{
			Email: "prepared-fence-" + model.NewID().String()[:8] + "@work.test", DisplayName: "Owner",
			Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		_, err = as.Memberships().Create(ctx, model.Membership{
			UserID: user.ID, TargetTenantID: f.tenant, Role: auth.RoleEditor,
		})
		return err
	}); err != nil {
		t.Fatalf("seed the work owner account: %v", err)
	}
	f.principal.ActorRef, f.principal.Actor = user.ID.String(), "user:"+user.ID.String()
	return user.ID
}

// The owning prepared replay's fence, retry and settled-replay machinery,
// measured positively on a mutation that is not an A2A path: a WorkItem
// created for its owner inside the replay, whose fenced write consumes the
// owner standing the replay read before its transaction (work_service.go's
// joinedFence). No A2A authority and no legacy operation port is involved.
//
// One move between that read and the transaction is read again once and the
// second attempt commits; a second move answers the typed authority move and
// writes nothing. Standing is read once per attempt and never inside the
// owning transaction. An exact replay of the committed claim is settled: it
// reads no standing and creates nothing.
func TestAPreparedReplayRetriesAMovedAccountFenceOnceAndCommits(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		moves int
		items int
	}{
		{name: "moved once commits on the second attempt", moves: 1, items: 1},
		{name: "moved twice answers the typed move", moves: 2, items: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newWorkFixture(t, filepath.Join(t.TempDir(), "prepared-fence.db"), nil)
			defer f.st.Close()
			calls := &joinedPortCalls{tenant: f.tenant}
			f.m.workIdentity = joinedCheckedParticipants{calls: calls}
			moving := &movingStanding{next: &joinedCheckedStanding{next: f.m.standing, calls: calls}, moves: tc.moves}
			f.m.standing = moving
			owner := workAccountOwner(t, &f)
			claim := ProtocolReplayClaim{
				WorkspaceID: f.workspace, Protocol: BindingProtocolA2A,
				PeerAuthority: "https://prepared-fence.example", Kind: ProtocolReplayMessageID,
				ReplayID:  "prepared-fence-" + strings.ReplaceAll(tc.name, " ", "-"),
				ExpiresAt: time.Now().UTC().Add(time.Hour),
			}
			plan := ProtocolReplayPlan{
				Accounts:     []model.ID{owner},
				Participants: []ProtocolReplayParticipant{{WorkspaceID: f.workspace, Kind: "user", Ref: f.principal.ActorRef}},
			}
			create := baseCreateCommand(f, "Prepared fence")
			apply := func() (ProtocolReplayResult, time.Duration, error) {
				ctx, cancel := context.WithTimeout(context.Background(), workflowCommunicationTimeout)
				defer cancel()
				started := time.Now()
				result, err := f.m.ApplyPreparedProtocolReplay(ctx, f.tenant, claim, plan,
					func(joined context.Context) (ProtocolReplaySettlement, error) {
						_, err := f.m.Apply(joined, f.tenant, f.principal, create)
						return ProtocolReplaySettlement{}, err
					})
				return result, time.Since(started), err
			}

			result, elapsed, err := apply()
			if inside := calls.insideCalls(); len(inside) != 0 {
				t.Fatalf("ports called inside the owning replay transaction: %v (answered %v)", inside, err)
			}
			if elapsed >= joinedReplayBound {
				t.Fatalf("the replay answered %v after %s, want an answer within %s",
					err, elapsed.Round(time.Millisecond), joinedReplayBound)
			}
			if reads := moving.readCount(); reads != 2 {
				t.Fatalf("standing read %d times, want once per attempt (2)", reads)
			}
			if items := workCount(t, f, workItemKind); items != tc.items {
				t.Fatalf("work items = %d, want %d", items, tc.items)
			}
			if tc.items == 0 {
				if !errors.Is(err, ErrProtocolReplayAuthorityMoved) {
					t.Fatalf("a second move answered %v, want ErrProtocolReplayAuthorityMoved", err)
				}
				return
			}
			if err != nil || result.Replayed || result.Guard.ID.IsZero() {
				t.Fatalf("one move answered %#v, %v, want a commit with its guard on the second attempt", result, err)
			}

			again, _, err := apply()
			if err != nil || !again.Replayed || again.Guard.ID != result.Guard.ID {
				t.Fatalf("exact replay = %#v, %v, want the settled guard %s", again, err, result.Guard.ID)
			}
			if reads := moving.readCount(); reads != 2 {
				t.Fatalf("a settled replay read standing: %d reads, want 2", reads)
			}
			if items := workCount(t, f, workItemKind); items != 1 {
				t.Fatalf("work items after the exact replay = %d, want 1", items)
			}
		})
	}
}
