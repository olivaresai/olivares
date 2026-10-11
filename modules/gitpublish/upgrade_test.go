// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// withoutProposalColumns registers the intent table as 26.10.0 created it,
// before the two proposal columns.
type withoutProposalColumns struct{ store.ExtensionRegistry }

func (r withoutProposalColumns) Register(d model.EntityDescriptor) error {
	if d.Kind == kindIntent {
		d.Fields = slices.DeleteFunc(slices.Clone(d.Fields), func(f model.FieldSpec) bool {
			return f.Name == "proposal_session_run" || f.Name == "proposal_approval"
		})
	}
	return r.ExtensionRegistry.Register(d)
}

// A store whose intents table predates proposals gains the two columns when
// this binary opens it: the earlier intent reads unchanged with no proposal,
// and a proposal is stored and read back on the same table.
func TestProposalColumnsReachAnExistingIntentsTable(t *testing.T) {
	ctx := context.Background()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "olivares.db")}
	h := newScopeHarnessOn(t, cfg, func(reg store.ExtensionRegistry) store.ExtensionRegistry { return withoutProposalColumns{reg} })
	before, err := h.push(h.user(), "op-before", "refs/heads/olivares/before", "")
	if err != nil || before.Intent.State != StateApplied {
		t.Fatalf("push on the earlier schema = %+v %v", before.Intent, err)
	}
	if err := h.st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := engine.Open(ctx, cfg, h.m.RegisterSchema)
	if err != nil {
		t.Fatalf("reopen with the proposal columns: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h.m.UseData(api.NewModuleData(st))
	h.git.sessionTrees = map[string]string{shaSession: shaTree}
	h.m.UseSessions(&fakeSessions{workspace: h.ws, run: runOne, dir: "/srv/work/run-1"})
	proposed, err := h.m.Push(ctx, h.user(), PushInput{Target: h.target.ID, OperationID: "proposal:" + approvalOne, Ref: "refs/heads/olivares/run-1",
		Commit: shaSession, Tree: shaTree, SessionRun: runOne, Proposal: h.proposal()})
	if err != nil || proposed.Intent.State != StateApplied {
		t.Fatalf("proposed push after the upgrade = %+v %v", proposed.Intent, err)
	}
	stored, err := h.m.Intents(ctx, h.user(), h.target.ID)
	if err != nil || len(stored) != 2 {
		t.Fatalf("intents after the upgrade = %+v %v", stored, err)
	}
	for _, in := range stored {
		want := Proposal{}
		if in.ID == proposed.Intent.ID {
			want = h.proposal()
		} else if in.ID != before.Intent.ID || in.State != StateApplied {
			t.Fatalf("the earlier intent changed: %+v", in)
		}
		if in.Proposal != want {
			t.Fatalf("intent %s proposal = %+v, want %+v", in.ID, in.Proposal, want)
		}
	}
}
