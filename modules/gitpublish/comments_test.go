// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func commentCost(v int64) *int64 { return &v }

func TestRepositoryBindingKeepsPositionalLiteralCompatibility(t *testing.T) {
	binding := RepositoryBinding{"rb1", 2, "api.github.com/acme/widgets", "acme", "widgets", "/srv/olivares/gitpublish/r1.git"}
	if binding.ID != "rb1" || binding.Version != 2 || binding.RepoID != "api.github.com/acme/widgets" || binding.Owner != "acme" || binding.Name != "widgets" || binding.LocalPath != "/srv/olivares/gitpublish/r1.git" {
		t.Fatalf("binding = %+v", binding)
	}
}

func TestCommentBodyExact(t *testing.T) {
	for name, tc := range map[string]struct {
		req  Requested
		want string
	}{
		"completed": {
			req:  Requested{CommentState: "completed", PullRequestURL: "https://github.com/acme/widgets/pull/42", CostMicroUSD: commentCost(1234567)},
			want: "Olivares run result: completed\nPull request: https://github.com/acme/widgets/pull/42\nCost: USD 1.234567\n<!-- olivares-gitpublish:digest -->\n",
		},
		"failed": {
			req:  Requested{CommentState: "failed"},
			want: "Olivares run result: failed\n<!-- olivares-gitpublish:digest -->\n",
		},
		"stopped zero cost": {
			req:  Requested{CommentState: "stopped", CostMicroUSD: commentCost(0)},
			want: "Olivares run result: stopped\nCost: USD 0.000000\n<!-- olivares-gitpublish:digest -->\n",
		},
		"cost at the bound": {
			req:  Requested{CommentState: "completed", CostMicroUSD: commentCost(maxCommentCostMicroUSD)},
			want: "Olivares run result: completed\nCost: USD 100000.000000\n<!-- olivares-gitpublish:digest -->\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := commentBody(tc.req, "digest"); got != tc.want {
				t.Fatalf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

func issueCommentInput(h *harness, op string) IssueCommentInput {
	h.custody.repoID = "api.github.com/acme/widgets"
	return IssueCommentInput{
		Target: h.target.ID, OperationID: op, Number: 17, State: "completed",
		PullRequestURL: "https://github.com/acme/widgets/pull/42", CostMicroUSD: commentCost(1234567),
	}
}

func TestIssueCommentAppliedWithFixedResultAndReplay(t *testing.T) {
	h := newHarness(t)
	in := issueCommentInput(h, "comment-1")
	r, err := h.m.Comment(context.Background(), h.user(), in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Intent.State != StateApplied || r.Intent.Receipt != ReceiptCaused || !r.Intent.Acknowledged.Acknowledged {
		t.Fatalf("intent = %+v", r.Intent)
	}
	if h.host.commentCreates != 1 || len(h.host.commentBodies) != 1 {
		t.Fatalf("creates = %d bodies = %v", h.host.commentCreates, h.host.commentBodies)
	}
	body := h.host.commentBodies[0]
	for _, want := range []string{
		"Olivares run result: completed\n",
		"Pull request: https://github.com/acme/widgets/pull/42\n",
		"Cost: USD 1.234567\n",
		"<!-- olivares-gitpublish:" + r.Intent.Digest + " -->",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %q lacks %q", body, want)
		}
	}
	if strings.Contains(strings.ToLower(body), "diff") || len(body) > 1024 {
		t.Fatalf("body is not the bounded result: %q", body)
	}
	if r.Intent.Observed.CommentID != "901" || r.Intent.Observed.CommentURL == "" || !r.Intent.Observed.Present {
		t.Fatalf("observed = %+v", r.Intent.Observed)
	}
	if r.Intent.Requested.CommentState != "completed" || r.Intent.Requested.PullRequestURL != in.PullRequestURL || r.Intent.Requested.CostMicroUSD == nil || *r.Intent.Requested.CostMicroUSD != 1234567 {
		t.Fatalf("requested = %+v", r.Intent.Requested)
	}
	if _, err := h.m.Comment(context.Background(), h.user(), in); err != nil || h.host.commentCreates != 1 {
		t.Fatalf("replay: err=%v creates=%d", err, h.host.commentCreates)
	}
	for name, mutate := range map[string]func(*IssueCommentInput){
		"cost":  func(in *IssueCommentInput) { in.CostMicroUSD = commentCost(1234568) },
		"state": func(in *IssueCommentInput) { in.State = "failed" },
		"link":  func(in *IssueCommentInput) { in.PullRequestURL = "https://github.com/acme/widgets/pull/43" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := in
			mutate(&changed)
			if _, err := h.m.Comment(context.Background(), h.user(), changed); codeOf(err) != "operation_conflict" {
				t.Fatalf("changed replay: %v", err)
			}
		})
	}
	h.balanced()
}

func TestIssueCommentMintReleaseFailureIsReported(t *testing.T) {
	t.Run("publish", func(t *testing.T) {
		h := newHarness(t)
		h.host.mintErr = errors.Join(gp.ErrTokenScope, gp.ErrHostUnavailable)
		var failedTarget model.ID
		var failure string
		h.m.opts.OnReleaseFailure = func(target model.ID, code string) { failedTarget, failure = target, code }
		if _, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "mint-release")); codeOf(err) != "host_credential_refused" {
			t.Fatalf("mint refusal = %v", err)
		}
		if failedTarget != h.target.ID || failure != "unavailable" {
			t.Fatalf("release failure = target %q code %q", failedTarget, failure)
		}
	})
	t.Run("reconcile", func(t *testing.T) {
		h := newHarness(t)
		ambiguous := gp.Result{Class: gp.Ambiguous, Reason: "transport"}
		h.host.commentRes = &ambiguous
		r, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "mint-release-reconcile"))
		if err != nil || r.Intent.State != StateUncertain {
			t.Fatalf("setup = %+v, %v", r.Intent, err)
		}
		h.host.mintErr = errors.Join(gp.ErrTokenScope, gp.ErrHostUnavailable)
		if _, err := h.m.Reconcile(context.Background(), h.user(), r.Intent.ID); err == nil {
			t.Fatal("mint refusal was not returned")
		}
		if got := stateOf(t, h, r.Intent.ID).ReleaseFailure; got != "unavailable" {
			t.Fatalf("release failure = %q", got)
		}
	})
}

func TestIssueCommentReconcileAdoptsLateEffectWithoutSecondWrite(t *testing.T) {
	h := newHarness(t)
	ambiguous := gp.Result{Class: gp.Ambiguous, Reason: "transport"}
	h.host.commentRes = &ambiguous
	r, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-late"))
	if err != nil || r.Intent.State != StateUncertain {
		t.Fatalf("create = %+v %v", r.Intent, err)
	}
	body := h.host.commentBodies[0]
	h.host.comments = append(h.host.comments, gp.Comment{ID: "998", URL: "https://git.example/comment/998", Body: body + "different operation"})
	miss, err := h.m.Reconcile(context.Background(), h.user(), r.Intent.ID)
	if err != nil || miss.Intent.State != StateUncertain || miss.Intent.Observed.Present {
		t.Fatalf("near match = %+v %v", miss.Intent, err)
	}
	h.host.comments = append(h.host.comments, gp.Comment{ID: "999", URL: "https://git.example/comment/999", Body: body})
	rc, err := h.m.Reconcile(context.Background(), h.user(), r.Intent.ID)
	if err != nil || rc.Intent.State != StateAdopted || rc.Intent.Receipt != ReceiptExistingEffect || rc.Intent.Observed.CommentID != "999" {
		t.Fatalf("reconcile = %+v %v", rc.Intent, err)
	}
	if len(h.host.mintEffects) == 0 || h.host.mintEffects[len(h.host.mintEffects)-1] != gp.EffectIssueCommentObserve {
		t.Fatalf("reconciliation minted %v, want issue-comment read", h.host.mintEffects)
	}
	if _, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-late")); err != nil || h.host.commentCreates != 1 {
		t.Fatalf("retry: err=%v creates=%d", err, h.host.commentCreates)
	}
	h.balanced()
}

func TestIssueCommentCrossTenantAndCrossWorkspaceTargetsConcealed(t *testing.T) {
	h := newHarness(t)
	other := h.user()
	other.Tenant = h.other
	if _, err := h.m.Comment(context.Background(), other, issueCommentInput(h, "comment-ct")); codeOf(err) != "not_found" {
		t.Fatalf("cross-tenant = %v", err)
	}
	h.authz.deny[h.ws] = auth.ErrRouteDenied
	if _, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-cw")); codeOf(err) != "not_found" {
		t.Fatalf("cross-workspace = %v", err)
	}
	if h.host.mints != 0 || h.host.commentCreates != 0 {
		t.Fatalf("a concealed target minted %d tokens and wrote %d comments", h.host.mints, h.host.commentCreates)
	}
}

// The module asks the authority for the issue-comment permission and nothing wider: a
// caller who holds every other gitpublish permission is refused, and a caller who holds
// only this one is admitted.
func TestIssueCommentRequiresItsOwnPermission(t *testing.T) {
	h := newHarness(t)
	h.authz.denyPermission = map[auth.Permission]error{permIssueComment: auth.ErrRouteDenied}
	if _, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-perm")); codeOf(err) != "not_found" {
		t.Fatalf("comment without its permission = %v, want the concealed not_found", err)
	}
	if h.host.mints != 0 || h.host.commentCreates != 0 {
		t.Fatalf("a refused caller minted %d tokens and wrote %d comments", h.host.mints, h.host.commentCreates)
	}
	h.authz.denyPermission = map[auth.Permission]error{permPush: auth.ErrRouteDenied, permPullRequest: auth.ErrRouteDenied, permMerge: auth.ErrRouteDenied, permTargetRead: auth.ErrRouteDenied}
	if _, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-perm-only")); err != nil || h.host.commentCreates != 1 {
		t.Fatalf("comment with only its permission = %v creates=%d", err, h.host.commentCreates)
	}
}

// A host may store our comment with CRLF line endings or without the trailing newline.
// Matching our own comment must survive that, or a replay writes a second comment.
func TestFindCommentSurvivesHostLineEndingNormalization(t *testing.T) {
	want := commentBody(Requested{Number: 7, CommentState: "completed"}, "abc123")
	stored := strings.TrimRight(strings.ReplaceAll(want, "\n", "\r\n"), "\r\n")
	if c, ok := findComment([]gp.Comment{{ID: "1", Body: stored}}, want); !ok || c.ID != "1" {
		t.Fatalf("normalized own comment not found: %q vs %q", stored, want)
	}
	other := commentBody(Requested{Number: 7, CommentState: "failed"}, "abc123")
	if _, ok := findComment([]gp.Comment{{ID: "2", Body: strings.ReplaceAll(other, "\n", "\r\n")}}, want); ok {
		t.Fatal("a comment with a different state was adopted")
	}
}

func TestIssueCommentRearmAdoptsLateEffectWithoutSecondWrite(t *testing.T) {
	h := newHarness(t)
	h.authz.recheck = func() error { return auth.ErrRouteDenied }
	in := issueCommentInput(h, "comment-rearm")
	r, err := h.m.Comment(context.Background(), h.user(), in)
	if codeOf(err) != "forbidden" || r.Intent.State != StateNotDispatched {
		t.Fatalf("first attempt = %+v %v", r.Intent, err)
	}
	h.authz.recheck = nil
	h.host.comments = append(h.host.comments, gp.Comment{ID: "999", URL: "https://git.example/comment/999", Body: commentBody(r.Intent.Requested, r.Intent.Digest)})
	r, err = h.m.Comment(context.Background(), h.user(), in)
	if err != nil || r.Intent.State != StateAdopted || r.Intent.Receipt != ReceiptExistingEffect || r.Intent.Observed.CommentID != "999" || h.host.commentCreates != 0 {
		t.Fatalf("re-arm = %+v %v creates=%d", r.Intent, err, h.host.commentCreates)
	}
	h.balanced()
}

func TestIssueCommentEvidenceFailureKeepsAcknowledgement(t *testing.T) {
	h := newHarness(t)
	h.host.onComment = func(fh *fakeHost) { fh.lookupErr = gp.ErrLookupIncomplete }
	r, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-evidence"))
	if err != nil || r.Intent.State != StateApplied || !r.Intent.Acknowledged.Acknowledged || r.Intent.Reason != "evidence_unavailable" || r.Intent.Observed.CommentID != "901" || r.Intent.Observed.CommentURL == "" {
		t.Fatalf("evidence failure = %+v %v", r.Intent, err)
	}
	if _, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-evidence")); err != nil || h.host.commentCreates != 1 {
		t.Fatalf("replay: err=%v creates=%d", err, h.host.commentCreates)
	}
	h.balanced()
}

func TestIssueCommentConflictScope(t *testing.T) {
	h := newHarness(t)
	ambiguous := gp.Result{Class: gp.Ambiguous, Reason: "transport"}
	h.host.commentRes = &ambiguous
	r, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-scope-17"))
	if err != nil || r.Intent.State != StateUncertain || h.host.commentCreates != 1 {
		t.Fatalf("first = %+v %v", r.Intent, err)
	}
	if _, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-other-17")); codeOf(err) != "unresolved_intent" || h.host.commentCreates != 1 {
		t.Fatalf("same issue = %v creates=%d", err, h.host.commentCreates)
	}
	h.host.commentRes = nil
	other := issueCommentInput(h, "comment-scope-18")
	other.Number = 18
	if _, err := h.m.Comment(context.Background(), h.user(), other); err != nil || h.host.commentCreates != 2 {
		t.Fatalf("other issue = %v creates=%d", err, h.host.commentCreates)
	}
	h.balanced()
}

func TestIssueCommentValidationAndTargetLink(t *testing.T) {
	h := newHarness(t)
	valid := issueCommentInput(h, "comment-validation")
	for name, mutate := range map[string]func(*IssueCommentInput){
		"state":         func(in *IssueCommentInput) { in.State = "unknown" },
		"issue":         func(in *IssueCommentInput) { in.Number = 0 },
		"negative cost": func(in *IssueCommentInput) { in.CostMicroUSD = commentCost(-1) },
		"absurd cost":   func(in *IssueCommentInput) { in.CostMicroUSD = commentCost(maxCommentCostMicroUSD + 1) },
		"http link":     func(in *IssueCommentInput) { in.PullRequestURL = "http://git.example/acme/widgets/pull/42" },
		"userinfo":      func(in *IssueCommentInput) { in.PullRequestURL = "https://user@git.example/acme/widgets/pull/42" },
	} {
		t.Run(name, func(t *testing.T) {
			in := valid
			mutate(&in)
			if _, err := h.m.Comment(context.Background(), h.user(), in); codeOf(err) != "invalid_request" {
				t.Fatalf("Comment = %v", err)
			}
			if h.host.commentCreates != 0 || h.host.mints != 0 {
				t.Fatalf("a refused request reached the host: creates=%d mints=%d", h.host.commentCreates, h.host.mints)
			}
		})
	}
	in := valid
	in.PullRequestURL = "https://evil.example/acme/widgets/pull/42"
	if _, err := h.m.Comment(context.Background(), h.user(), in); codeOf(err) != "link_not_allowed" {
		t.Fatalf("foreign link: %v", err)
	}
	if h.host.commentCreates != 0 {
		t.Fatal("a refused request dispatched a comment")
	}
	in = issueCommentInput(h, "comment-cost-bound")
	in.CostMicroUSD = commentCost(maxCommentCostMicroUSD)
	if _, err := h.m.Comment(context.Background(), h.user(), in); err != nil {
		t.Fatalf("the cost bound itself is refused: %v", err)
	}
	h.balanced()
}

func TestCommentLinkAllowed(t *testing.T) {
	for name, tc := range map[string]struct {
		url, repoID, owner, name, webBase, host string
		want                                    bool
	}{
		"github cloud":         {"https://github.com/Acme/Widgets/pull/42", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", true},
		"github port 443":      {"https://github.com:443/Acme/Widgets/pull/42", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", true},
		"github base port 443": {"https://git.example/Acme/Tools/pull/42", "git.example/acme/tools", "Acme", "Tools", "https://git.example:443", "github", true},
		"github enterprise":    {"https://git.example/Acme/Tools/pull/42", "git.example/acme/tools", "Acme", "Tools", "https://git.example", "github", true},
		"gitlab subpath":       {"https://git.example/gitlab/acme/widgets/-/merge_requests/42", "git.example/acme/widgets", "acme", "widgets", "https://git.example/gitlab", "gitlab", true},
		"off port":             {"https://github.com:8443/Acme/Widgets/pull/42", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", false},
		"other repository":     {"https://github.com/Acme/other/pull/42", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", false},
		"gitlab case":          {"https://gitlab.com/acme/widgets/-/merge_requests/42", "gitlab.com/acme/widgets", "Acme", "Widgets", "https://gitlab.com", "gitlab", false},
		"wrong gitlab shape":   {"https://gitlab.com/acme/widgets/merge_requests/42", "gitlab.com/acme/widgets", "acme", "widgets", "https://gitlab.com", "gitlab", false},
		"query":                {"https://github.com/Acme/Widgets/pull/42?x=1", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", false},
		"fragment":             {"https://github.com/Acme/Widgets/pull/42#result", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", false},
		"dot segment":          {"https://github.com/Acme/Widgets/../other/pull/42", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", false},
		"encoded dot segment":  {"https://github.com/Acme/Widgets/%2e%2e/other/pull/42", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", false},
		"nonnumeric change":    {"https://github.com/Acme/Widgets/pull/current", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", false},
		"leading zero":         {"https://github.com/Acme/Widgets/pull/042", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", false},
		"issue not pull":       {"https://github.com/Acme/Widgets/issues/42", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", false},
		"missing owner":        {"https://github.com/Acme/Widgets/pull/42", "api.github.com/acme/widgets", "", "Widgets", "https://github.com", "github", false},
		"missing web base":     {"https://github.com/Acme/Widgets/pull/42", "api.github.com/acme/widgets", "Acme", "Widgets", "", "github", false},
		"other host":           {"https://evil.example/Acme/Widgets/pull/42", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", false},
		"lookalike host":       {"https://github.com.evil.example/Acme/Widgets/pull/42", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", false},
		"userinfo":             {"https://user@github.com/Acme/Widgets/pull/42", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", false},
		"http scheme":          {"http://github.com/Acme/Widgets/pull/42", "api.github.com/acme/widgets", "Acme", "Widgets", "https://github.com", "github", false},
		"gitlab issue":         {"https://gitlab.com/acme/widgets/-/issues/42", "gitlab.com/acme/widgets", "acme", "widgets", "https://gitlab.com", "gitlab", false},
		"gitlab leading zero":  {"https://gitlab.com/acme/widgets/-/merge_requests/042", "gitlab.com/acme/widgets", "acme", "widgets", "https://gitlab.com", "gitlab", false},
		"unknown host kind":    {"https://git.example/Acme/Widgets/pull/42", "git.example/acme/widgets", "Acme", "Widgets", "https://git.example", "plain", false},
	} {
		t.Run(name, func(t *testing.T) {
			rb := RepositoryBinding{RepoID: tc.repoID, Owner: tc.owner, Name: tc.name}
			if got := commentLinkAllowed(tc.url, rb, tc.webBase, tc.host); got != tc.want {
				t.Fatalf("commentLinkAllowed(%q, %+v, %q) = %t, want %t", tc.url, rb, tc.host, got, tc.want)
			}
		})
	}
}

type legacyCommentCustody struct{ base *fakeCustody }

func (c legacyCommentCustody) CredentialBinding(ctx context.Context, tenant model.TenantID, workspace model.ID, id string) (CredentialBinding, error) {
	return c.base.CredentialBinding(ctx, tenant, workspace, id)
}
func (c legacyCommentCustody) RepositoryBinding(ctx context.Context, tenant model.TenantID, workspace model.ID, id string) (RepositoryBinding, error) {
	return c.base.RepositoryBinding(ctx, tenant, workspace, id)
}
func (c legacyCommentCustody) OpenHost(ctx context.Context, tenant model.TenantID, cb CredentialBinding, rb RepositoryBinding) (gp.Host, error) {
	return c.base.OpenHost(ctx, tenant, cb, rb)
}

func TestIssueCommentLegacyCustodyCompatibility(t *testing.T) {
	for _, withLink := range []bool{false, true} {
		t.Run(strconv.FormatBool(withLink), func(t *testing.T) {
			h := newHarness(t)
			h.m.UseCustody(legacyCommentCustody{base: h.custody})
			in := issueCommentInput(h, "legacy-custody")
			if !withLink {
				in.PullRequestURL = ""
			}
			r, err := h.m.Comment(context.Background(), h.user(), in)
			if withLink {
				if codeOf(err) != "link_not_allowed" || h.host.commentCreates != 0 || h.host.mints != 0 {
					t.Fatalf("linked comment = %+v, %v; creates=%d mints=%d", r.Intent, err, h.host.commentCreates, h.host.mints)
				}
				return
			}
			if err != nil || r.Intent.State != StateApplied || h.host.commentCreates != 1 {
				t.Fatalf("unlinked comment = %+v, %v; creates=%d", r.Intent, err, h.host.commentCreates)
			}
		})
	}
}

func TestIssueCommentWebBaseResolverFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"not approved": {err: ErrBindingNotApproved, want: "binding_not_approved"},
		"unavailable":  {err: errors.New("injected resolver failure"), want: "authority_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.custody.webBaseErr = tc.err
			if _, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "resolver-failure")); codeOf(err) != tc.want {
				t.Fatalf("resolver failure = %q, want %q", codeOf(err), tc.want)
			}
			if h.host.commentCreates != 0 || h.host.mints != 0 {
				t.Fatalf("resolver failure dispatched: creates=%d mints=%d", h.host.commentCreates, h.host.mints)
			}
		})
	}
}

func TestIssueCommentAuthorityRevocationStopsWrite(t *testing.T) {
	h := newHarness(t)
	h.authz.recheck = func() error { return auth.ErrRouteDenied }
	r, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-revoked"))
	if codeOf(err) != "forbidden" || r.Intent.State != StateNotDispatched || h.host.commentCreates != 0 {
		t.Fatalf("revoked = %+v %v creates=%d", r.Intent, err, h.host.commentCreates)
	}
	h.balanced()
}

type failingModuleData struct{ api.ModuleData }

func (failingModuleData) Mutate(context.Context, model.TenantID, func(store.Scope) error) error {
	return errors.New("injected mutate failure")
}

func TestIssueCommentReconcileReportsFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		prepare func(*harness)
		want    string
	}{
		"mint":  {prepare: func(h *harness) { h.host.mintErr = gp.ErrHostUnavailable }, want: "host_unavailable"},
		"store": {prepare: func(h *harness) { h.m.data = failingModuleData{h.m.data} }, want: "authority_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			ambiguous := gp.Result{Class: gp.Ambiguous, Reason: "transport"}
			h.host.commentRes = &ambiguous
			r, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-reconcile-failure"))
			if err != nil || r.Intent.State != StateUncertain {
				t.Fatalf("create = %+v %v", r.Intent, err)
			}
			tc.prepare(h)
			_, err = h.m.Reconcile(context.Background(), h.user(), r.Intent.ID)
			if codeOf(err) != tc.want {
				t.Fatalf("reconciliation failure = %q, want %q", codeOf(err), tc.want)
			}
			var e *Error
			if !errors.As(err, &e) || e.Intent != r.Intent.ID {
				t.Fatalf("error does not name the intent: %v", err)
			}
			if got := stateOf(t, h, r.Intent.ID).State; got != StateUncertain || h.host.commentCreates != 1 {
				t.Fatalf("failure changed state to %q or wrote %d comments", got, h.host.commentCreates)
			}
		})
	}
}

type toggleFailureData struct {
	api.ModuleData
	fail *bool
}

func (d toggleFailureData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if *d.fail {
		return errors.New("injected settlement failure")
	}
	return d.ModuleData.Mutate(ctx, tenant, fn)
}

type failingViewData struct{ api.ModuleData }

func (failingViewData) View(context.Context, model.TenantID, func(store.Scope) error) error {
	return errors.New("injected view failure")
}

func TestIssueCommentReconcileLoadFailureIsNotAbsence(t *testing.T) {
	h := newHarness(t)
	ambiguous := gp.Result{Class: gp.Ambiguous, Reason: "transport"}
	h.host.commentRes = &ambiguous
	r, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-load-failure"))
	if err != nil || r.Intent.State != StateUncertain {
		t.Fatalf("create = %+v %v", r.Intent, err)
	}
	h.m.data = failingViewData{h.m.data}
	if _, err := h.m.Reconcile(context.Background(), h.user(), r.Intent.ID); codeOf(err) != "authority_unavailable" {
		t.Fatalf("load failure = %v, want authority_unavailable rather than not_found", err)
	}
}

func TestIssueCommentReleaseRecordFailureIsReported(t *testing.T) {
	h := newHarness(t)
	r, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-release-log"))
	if err != nil {
		t.Fatal(err)
	}
	h.m.data = failingModuleData{h.m.data}
	var logged []string
	h.m.opts.OnReleaseFailure = func(_ model.ID, code string) { logged = append(logged, code) }
	if _, ok := h.m.recordReleaseFailure(context.Background(), h.user(), r.Intent.ID, r.Intent.Target, "server_error"); ok || len(logged) != 1 || logged[0] != "server_error" {
		t.Fatalf("unrecorded release failure: ok=%t logged=%v", ok, logged)
	}
}

func TestIssueCommentSettlementFailureIsReturned(t *testing.T) {
	for name, notDispatched := range map[string]bool{"applied": false, "not dispatched": true} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			failed := false
			h.m.data = toggleFailureData{ModuleData: h.m.data, fail: &failed}
			if notDispatched {
				h.authz.recheck = func() error { failed = true; return auth.ErrRouteDenied }
			} else {
				h.host.onComment = func(*fakeHost) { failed = true }
			}
			r, err := h.m.Comment(context.Background(), h.user(), issueCommentInput(h, "comment-settlement"))
			if codeOf(err) != "authority_unavailable" {
				t.Fatalf("settlement failure = %+v %v", r.Intent, err)
			}
			var e *Error
			if !errors.As(err, &e) || e.Intent != r.Intent.ID {
				t.Fatalf("error does not name the intent: %v", err)
			}
		})
	}
}

func TestIssueCommentRouteUsesSealedEffectAndProjectsResult(t *testing.T) {
	h := newHarness(t)
	handler := handlerFor(t, h.m, http.MethodPost, "/targets/{id}/issue-comments")
	in := issueCommentInput(h, "comment-route")
	rec := call(handler, h.user(), http.MethodPost, h.target.ID.String(), map[string]any{
		"operation_id": in.OperationID, "number": in.Number, "state": in.State,
		"pull_request_url": in.PullRequestURL, "cost_micro_usd": 1234567,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("route = %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["effect"] != "issue_comment" {
		t.Fatalf("effect = %v", body["effect"])
	}
	requested := body["requested"].(map[string]any)
	observed := body["observed"].(map[string]any)
	if requested["state"] != "completed" || requested["pull_request_url"] != in.PullRequestURL || requested["cost_micro_usd"] != float64(1234567) || observed["comment_id"] != "901" {
		t.Fatalf("route DTO = %+v", body)
	}
	bad := call(handler, h.user(), http.MethodPost, h.target.ID.String(), map[string]any{
		"operation_id": "comment-with-diff", "number": 17, "state": "completed", "body": "untrusted diff content",
	})
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "field_not_accepted") || h.host.commentCreates != 1 {
		t.Fatalf("free-form body = %d %s creates=%d", bad.Code, bad.Body.String(), h.host.commentCreates)
	}
	h.balanced()
}
