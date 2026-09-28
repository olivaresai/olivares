// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// A prepared replay with an empty plan prepares nothing, so an evidence read
// inside its transaction refuses at once with ErrJoinedEvidenceUnprepared. It
// is not a miss of a prepared record, which would count as a move: the replay
// would then run its mutation twice and answer ErrProtocolReplayAuthorityMoved,
// a code the inbound A2A adapter tells its peer to retry.
func TestAnEmptyPlanRefusesAJoinedReadInsteadOfMoving(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, filepath.Join(t.TempDir(), "empty-plan.db"), nil)
	defer f.st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommunicationTimeout)
	defer cancel()
	runs := 0
	var readErr error
	_, err := f.m.ApplyPreparedProtocolReplay(ctx, f.tenant, ProtocolReplayClaim{
		WorkspaceID: f.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: "https://empty-plan.example", Kind: ProtocolReplayJTI,
		ReplayID: "empty-plan-read", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}, ProtocolReplayPlan{}, func(joined context.Context) (ProtocolReplaySettlement, error) {
		runs++
		_, _, readErr = f.m.communicationEvidencePorts(joined, f.tenant, true).attestor.AttestPublicationAudience(
			joined, PublicationAudienceRequest{},
		)
		return ProtocolReplaySettlement{}, readErr
	})
	if runs != 1 || !errors.Is(readErr, ErrJoinedEvidenceUnprepared) {
		t.Fatalf("an empty plan's joined read ran %d times and answered %v, want one run refused with %v",
			runs, readErr, ErrJoinedEvidenceUnprepared)
	}
	if err == nil || errors.Is(err, ErrProtocolReplayAuthorityMoved) {
		t.Fatalf("the replay answered %v, want the refusal, not a move", err)
	}
}
