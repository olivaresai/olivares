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

	"github.com/olivaresai/olivares/core/model"
)

// A plain replay prepares nothing, so a joined work command that makes a user
// the owner cannot read that account's standing without opening a second store
// transaction beside the owning one, which on SQLite waits for the connection
// the owning transaction holds. The command refuses at once instead, and the
// replay writes nothing.
func TestAPlainJoinedOwnerFenceRefusesInsteadOfWaiting(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, filepath.Join(t.TempDir(), "plain-joined-owner.db"), nil)
	defer f.st.Close()
	claim := ProtocolReplayClaim{
		WorkspaceID: f.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: "https://plain-joined-owner.example", Kind: ProtocolReplayJTI,
		ReplayID: "plain-joined-owner-" + model.NewID().String(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommunicationTimeout)
	defer cancel()
	started := time.Now()
	_, err := f.m.ApplyProtocolReplay(ctx, f.tenant, claim,
		func(joined context.Context) (ProtocolReplaySettlement, error) {
			_, applyErr := f.m.Apply(joined, f.tenant, f.principal, baseCreateCommand(f, "plain joined owner"))
			return ProtocolReplaySettlement{}, applyErr
		})
	elapsed := time.Since(started)
	if !errors.Is(err, ErrJoinedEvidenceUnprepared) || elapsed >= joinedReplayBound {
		t.Fatalf("a plain joined user-owned create answered %v after %s, want %v within %s",
			err, elapsed.Round(time.Millisecond), ErrJoinedEvidenceUnprepared, joinedReplayBound)
	}
	if items := workCount(t, f, workItemKind); items != 0 {
		t.Fatalf("the refused replay left %d work items, want none", items)
	}
}
