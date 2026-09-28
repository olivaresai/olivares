// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// movingStanding answers its first moves reads with every existing account's
// authority version one ahead of the stored one, so the barrier that pins the
// answer conflicts, as it does when an account's authority moves between the
// standing read and the barrier.
type movingStanding struct {
	next  auth.StandingReader
	moves int

	mu    sync.Mutex
	reads int
}

func (s *movingStanding) Standing(
	ctx context.Context, tenant model.TenantID, users []model.ID,
) (map[model.ID]auth.Standing, error) {
	standing, err := s.next.Standing(ctx, tenant, users)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if err == nil && s.reads <= s.moves {
		for id, current := range standing {
			if current.Exists {
				current.AuthorityVersion++
				standing[id] = current
			}
		}
	}
	return standing, err
}

func (s *movingStanding) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// T7 before CM-10 (TestAJoinedInterruptRetriesAMovedFenceOnce). An interrupt
// is an A2A path, so it is now refused while its publish is prepared, before
// its fence is read or pinned, whether the fence moves once or twice. The
// retry-once-then-commit and the typed move on a second move are measured on
// a non-A2A mutation by TestAPreparedReplayRetriesAMovedAccountFenceOnceAndCommits.
func TestAJoinedInterruptIsRefusedBeforeAMovingFence(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		moves int
	}{
		{name: "moved once", moves: 1},
		{name: "moved twice", moves: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newWorkflowCommunicationFixture(t, false)
			makeProtocolInterruptRecipientWriter(t, fixture)
			binding := protocolInterruptBindingForTest(t, fixture, BindingProtocolA2A)
			calls := installJoinedPortChecks(fixture.m, fixture.tenant)
			moving := &movingStanding{next: fixture.m.standing, moves: tc.moves}
			fixture.m.standing = moving
			replayID := "moved-fence-" + strings.ReplaceAll(tc.name, " ", "-")
			_, elapsed, err := applyJoinedInterrupt(fixture,
				joinedInterruptClaim(fixture, binding, replayID),
				joinedInterruptCommand(fixture, binding, replayID+"-request"))
			if inside := calls.insideCalls(); len(inside) != 0 {
				t.Fatalf("ports called inside the owning replay transaction: %v (the replay answered %v after %s)",
					inside, err, elapsed.Round(time.Millisecond))
			}
			if elapsed >= joinedReplayBound {
				t.Fatalf("the replay answered %v after %s, want an answer within %s",
					err, elapsed.Round(time.Millisecond), joinedReplayBound)
			}
			// CM-10: the interrupt is an A2A path. It is refused while its publish
			// is prepared, before the owning replay's transaction, so no attempt
			// commits and nothing is written, whatever the fence does.
			requireA2ARefused(t, err, "a joined interrupt with a moving fence")
			wantJoinedInterruptRows(t, fixture, 0, "after the refusal")
		})
	}
}
