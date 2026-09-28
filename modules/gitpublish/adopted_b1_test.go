// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

// Tests adopted from the independent construction read (READ-J10-S3-
// CONSTRUCTION.md, be056547), renamed so the reader's own overlay files can
// still be loaded beside them.

import (
	"context"
	"net/http"
	"testing"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Root §1 OD2/OD7: push:write (editor tier, API tokens, no AAL floor) must not
// substitute for merge:admin + AAL3. A push to a merge base is exactly that.
func TestAdoptedTokenCannotPushToMergeBase(t *testing.T) {
	h := newHarness(t)
	tok := h.caller(auth.KindToken, "", model.NewID(), 0)
	openPR(h, 11, shaCommit)
	if _, err := h.m.Merge(context.Background(), tok, MergeInput{Target: h.target.ID, OperationID: "m", Number: 11, ExpectedHead: shaCommit, Method: "merge"}); codeOf(err) != "step_up_required" {
		t.Fatalf("token merge = %v", err)
	}
	r, err := h.push(tok, "op-main", "refs/heads/main", shaBase)
	t.Logf("AAL0 token push to merge base main: state=%q err=%v host main=%s", r.Intent.State, err, h.host.refs["main"])
	if codeOf(err) != "ref_not_allowed" {
		t.Fatalf("an AAL0 token updated the merge base 'main' by push (state %s): merge step-up bypassed", r.Intent.State)
	}
}

// Same defect through the real api.Server, Authenticator and Authorizer: an
// editor session at AAL1 cannot merge, but pushes straight onto main.
func TestAdoptedEditorCannotPushToMergeBaseProduction(t *testing.T) {
	s := newServer(t)
	if code, _, raw := s.do("POST", "/v1/setup", "", map[string]any{"token": s.setup, "email": "root@x.io", "password": "supersecret1"}, ""); code != http.StatusCreated {
		t.Fatalf("setup = %d %s", code, raw)
	}
	root := s.login("root@x.io", "supersecret1")
	code, out, raw := s.do("POST", "/v1/system/orgs", root, map[string]any{"name": "acme", "slug": "acme"}, "")
	if code != http.StatusCreated {
		t.Fatalf("org = %d %s", code, raw)
	}
	s.tenant = model.TenantID(out["tenant_id"].(string))
	editor := s.member(root, "editor")
	var target model.ID
	if err := s.m.data.Mutate(context.Background(), s.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kindTarget)
		if err != nil {
			return err
		}
		rec, err := repo.Create(context.Background(), model.Record{"workspace_id": model.NewID().String(), "credential_binding": "cb1", "repository_binding": "rb1", "push_prefix": "olivares/", "merge_bases": "main", "created_by": "user:seed"})
		target = model.ID(rec.String(model.ColID))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	base := "/v1/m/gitpublish/targets/" + target.String()
	s.host.changes = append(s.host.changes, gp.Change{Number: 1, Open: true, HeadRef: "olivares/pr", HeadSHA: shaCommit, BaseRef: "main"})
	mc, _, mraw := s.do("POST", base+"/merges", editor, map[string]any{"operation_id": "m", "number": 1, "expected_head": shaCommit, "method": "merge"}, s.tenant)
	pc, pout, praw := s.do("POST", base+"/pushes", editor, map[string]any{"operation_id": "p", "ref": "refs/heads/main", "expected_old": shaBase, "commit": shaCommit, "tree": shaTree}, s.tenant)
	t.Logf("editor merge = %d %s", mc, mraw)
	t.Logf("editor push to main = %d %s; host main now %s", pc, praw, s.host.refs["main"])
	if mc < 400 {
		t.Fatalf("editor merge unexpectedly allowed")
	}
	if pc == http.StatusOK && pout["state"] == StateApplied {
		t.Fatal("an editor without merge:admin/AAL3 landed a commit on the merge base 'main' by push")
	}
}
