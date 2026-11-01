// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
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

// withoutIssueCommentColumns registers the intent table before issue comments.
type withoutIssueCommentColumns struct{ store.ExtensionRegistry }

func (r withoutIssueCommentColumns) Register(d model.EntityDescriptor) error {
	if d.Kind == kindIntent {
		d.Fields = slices.DeleteFunc(slices.Clone(d.Fields), func(f model.FieldSpec) bool {
			return slices.Contains([]string{"req_comment_state", "req_pull_request_url", "req_cost_micro_usd", "obs_comment_id", "obs_comment_url"}, f.Name)
		})
	}
	return r.ExtensionRegistry.Register(d)
}

func legacyPushDigest(h *harness, in PushInput) string {
	s := spec{effect: effectPush, target: in.Target, op: in.OperationID,
		req: Requested{Ref: in.Ref, ExpectedOld: in.ExpectedOld, Commit: in.Commit, Tree: in.Tree}}
	sub := SubjectOf(h.user().Principal)
	digest := sha256.New()
	digest.Write([]byte("olivares.gitpublish.intent.v1\x00"))
	for _, f := range []string{h.tenant.String(), s.target.String(), s.effect, s.op, sub.ActorKind, sub.Actor,
		s.req.Ref, s.req.ExpectedOld, s.req.Commit, s.req.Tree, s.req.HeadRef, s.req.Base, s.req.Title,
		strconv.Itoa(s.req.Number), s.req.ExpectedHead, s.req.Method, s.body, strconv.FormatBool(s.draft)} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		digest.Write(n[:])
		digest.Write([]byte(f))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func TestIssueCommentColumnsReachAnExistingIntentsTable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		older bool
	}{
		{name: "sqlite"},
		{name: "sqlite before proposals", older: true},
		{name: "postgres"},
		{name: "postgres before proposals", older: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "olivares.db")}
			catalogDSN := ""
			if strings.HasPrefix(tc.name, "postgres") {
				dsns := enginetest.IsolatedPostgres(t)
				catalogDSN = dsns.App
				cfg = store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 16}
			}
			testIssueCommentColumnsUpgrade(t, cfg, tc.older, catalogDSN)
		})
	}
}

func testIssueCommentColumnsUpgrade(t *testing.T, cfg store.Config, beforeProposals bool, catalogDSN string) {
	ctx := context.Background()
	commentColumns := []string{"req_comment_state", "req_pull_request_url", "req_cost_micro_usd", "obs_comment_id", "obs_comment_url"}
	upgradeColumns := commentColumns
	if beforeProposals {
		upgradeColumns = append(upgradeColumns, "proposal_session_run", "proposal_approval")
	}
	assertIntentColumns(t, ctx, catalogDSN, upgradeColumns, 0)
	h := newScopeHarnessOn(t, cfg, func(reg store.ExtensionRegistry) store.ExtensionRegistry {
		if beforeProposals {
			return withoutProposalColumns{withoutIssueCommentColumns{reg}}
		}
		return withoutIssueCommentColumns{reg}
	})
	before, err := h.push(h.user(), "op-before-comment", "refs/heads/olivares/before-comment", "")
	if err != nil || before.Intent.State != StateApplied {
		t.Fatalf("push on the earlier schema = %+v %v", before.Intent, err)
	}
	legacyInput := PushInput{Target: h.target.ID, OperationID: "op-before-comment", Ref: "refs/heads/olivares/before-comment", Commit: shaCommit, Tree: shaTree}
	if err := h.m.data.Mutate(ctx, h.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kindIntent)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, before.Intent.ID)
		if err != nil {
			return err
		}
		rec["request_digest"] = legacyPushDigest(h, legacyInput)
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatalf("store the pre-upgrade digest: %v", err)
	}
	if err := h.st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := engine.Open(ctx, cfg, h.m.RegisterSchema)
	if err != nil {
		t.Fatalf("reopen with issue-comment columns: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h.m.UseData(api.NewModuleData(st))
	assertIntentColumns(t, ctx, catalogDSN, upgradeColumns, len(upgradeColumns))
	commented, err := h.m.Comment(ctx, h.user(), issueCommentInput(h, "comment-after-upgrade"))
	if err != nil || commented.Intent.State != StateApplied {
		t.Fatalf("comment after the upgrade = %+v %v", commented.Intent, err)
	}
	zeroInput := issueCommentInput(h, "comment-zero-cost")
	zeroInput.CostMicroUSD = commentCost(0)
	zero, err := h.m.Comment(ctx, h.user(), zeroInput)
	if err != nil || zero.Intent.State != StateApplied {
		t.Fatalf("zero-cost comment = %+v %v", zero.Intent, err)
	}
	nilInput := issueCommentInput(h, "comment-nil-cost")
	nilInput.CostMicroUSD = nil
	withoutCost, err := h.m.Comment(ctx, h.user(), nilInput)
	if err != nil || withoutCost.Intent.State != StateApplied {
		t.Fatalf("comment without cost = %+v %v", withoutCost.Intent, err)
	}
	stored, err := h.m.Intents(ctx, h.user(), h.target.ID)
	if err != nil || len(stored) != 4 {
		t.Fatalf("intents after the upgrade = %+v %v", stored, err)
	}
	for _, in := range stored {
		if in.ID == before.Intent.ID {
			if in.State != StateApplied || in.Requested.CommentState != "" || in.Requested.PullRequestURL != "" || in.Requested.CostMicroUSD != nil || in.Observed.CommentID != "" || in.Observed.CommentURL != "" {
				t.Fatalf("the earlier intent changed: %+v", in)
			}
			continue
		}
		if in.Requested.CommentState != "completed" || in.Observed.CommentID == "" {
			t.Fatalf("the comment was not retained: %+v", in)
		}
		switch in.ID {
		case commented.Intent.ID:
			if in.Requested.CostMicroUSD == nil || *in.Requested.CostMicroUSD != 1234567 {
				t.Fatalf("nonzero cost = %+v", in.Requested.CostMicroUSD)
			}
		case zero.Intent.ID:
			if in.Requested.CostMicroUSD == nil || *in.Requested.CostMicroUSD != 0 {
				t.Fatalf("zero cost = %+v", in.Requested.CostMicroUSD)
			}
		case withoutCost.Intent.ID:
			if in.Requested.CostMicroUSD != nil {
				t.Fatalf("NULL cost = %+v", in.Requested.CostMicroUSD)
			}
		default:
			t.Fatalf("unexpected intent: %+v", in)
		}
	}
	replay, err := h.push(h.user(), "op-before-comment", "refs/heads/olivares/before-comment", "")
	if err != nil || replay.Intent.ID != before.Intent.ID || h.git.count() != 1 {
		t.Fatalf("legacy operation replay = %+v %v pushes=%d", replay.Intent, err, h.git.count())
	}
}

func assertIntentColumns(t *testing.T, ctx context.Context, dsn string, columns []string, want int) {
	t.Helper()
	if dsn == "" {
		return
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect for the schema catalog: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var got int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'gitpublish_intent' AND column_name = ANY($1)`, columns).Scan(&got); err != nil {
		t.Fatalf("read the schema catalog: %v", err)
	}
	if got != want {
		t.Fatalf("upgrade columns present = %d, want %d of %v", got, want, columns)
	}
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
