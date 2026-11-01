// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/model"
)

// maxCommentCostMicroUSD bounds the cost a caller may report: USD 100,000. The cost is a
// fact the caller states, so the comment never carries a number past what one run can be.
const maxCommentCostMicroUSD = 100_000 * 1_000_000

// IssueCommentInput publishes one bounded run result to an issue. There is no
// free-form body: state, an optional pull-request link and optional cost are
// the only content a caller can name.
type IssueCommentInput struct {
	Target            model.ID
	OperationID       string
	Number            int
	State             string
	PullRequestURL    string
	CostMicroUSD      *int64
	AcknowledgeIntent model.ID
	Proposal          Proposal
}

// check reports the request's own shape refusal before any target is read.
func (in IssueCommentInput) check() *Error {
	switch in.State {
	case "completed", "failed", "stopped":
	default:
		return errInvalid
	}
	if in.Number <= 0 || in.CostMicroUSD != nil && (*in.CostMicroUSD < 0 || *in.CostMicroUSD > maxCommentCostMicroUSD) || len(in.PullRequestURL) > 2048 {
		return errInvalid
	}
	if in.PullRequestURL != "" {
		u, err := url.Parse(in.PullRequestURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errInvalid
		}
	}
	return nil
}

// Comment publishes a run result through the same admission, claim, authority
// recheck, dispatch and reconciliation pipeline as every other effect.
func (m *Module) Comment(ctx context.Context, c Caller, in IssueCommentInput) (Receipt, error) {
	if e := in.check(); e != nil {
		return Receipt{}, e
	}
	s := spec{effect: effectIssueComment, perm: permIssueComment, action: actionIssueComment, host: gp.EffectIssueComment,
		target: in.Target, op: in.OperationID, ack: in.AcknowledgeIntent, proposal: in.Proposal,
		req: Requested{Number: in.Number, CommentState: in.State, PullRequestURL: in.PullRequestURL, CostMicroUSD: in.CostMicroUSD}}
	s.scope = "issue_comment:" + strconv.Itoa(in.Number)
	return m.publish(ctx, c, s)
}

// commentBody is the complete external content. The operation digest makes an
// ambiguous create reconcilable without accepting a caller-supplied marker.
func commentBody(req Requested, digest string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Olivares run result: %s\n", req.CommentState)
	if req.PullRequestURL != "" {
		fmt.Fprintf(&b, "Pull request: %s\n", req.PullRequestURL)
	}
	if req.CostMicroUSD != nil {
		fmt.Fprintf(&b, "Cost: USD %d.%06d\n", *req.CostMicroUSD/1_000_000, *req.CostMicroUSD%1_000_000)
	}
	fmt.Fprintf(&b, "<!-- olivares-gitpublish:%s -->\n", digest)
	return b.String()
}

func commentCostDigest(cost *int64) string {
	if cost == nil {
		return ""
	}
	return strconv.FormatInt(*cost, 10)
}

// commentLinkAllowed binds a PR link to the approved repository and the host's
// own change URL shape. A comment cannot point its result at another host or
// repository, nor at an arbitrary path.
func commentLinkAllowed(raw string, rb RepositoryBinding, webBase, hostKind string) bool {
	if raw == "" {
		return true
	}
	if hostKind != "github" && hostKind != "gitlab" || rb.Owner == "" || rb.Name == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || !commentHTTPSURL(u) {
		return false
	}
	base, err := url.Parse(webBase)
	if err != nil || !commentHTTPSURL(base) || !strings.EqualFold(u.Hostname(), base.Hostname()) || commentURLPort(u) != commentURLPort(base) {
		return false
	}
	prefix := strings.TrimRight(base.Path, "/") + "/" + rb.Owner + "/" + rb.Name + "/"
	var rest string
	if hostKind == "github" {
		if len(u.Path) < len(prefix) || !strings.EqualFold(u.Path[:len(prefix)], prefix) {
			return false
		}
		rest = u.Path[len(prefix):]
	} else {
		var ok bool
		rest, ok = strings.CutPrefix(u.Path, prefix)
		if !ok {
			return false
		}
	}
	parts := strings.Split(rest, "/")
	var number string
	switch hostKind {
	case "github":
		if len(parts) != 2 || parts[0] != "pull" {
			return false
		}
		number = parts[1]
	case "gitlab":
		if len(parts) != 3 || parts[0] != "-" || parts[1] != "merge_requests" {
			return false
		}
		number = parts[2]
	}
	n, err := strconv.Atoi(number)
	return err == nil && n > 0 && strconv.Itoa(n) == number
}

func commentHTTPSURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Port() == "" || u.Port() == "443")
}

func commentURLPort(u *url.URL) string {
	if u.Port() == "" {
		return "443"
	}
	return u.Port()
}

// findComment matches our own comment by its full body. A host may store it with CRLF
// line endings or without the trailing newline, so both sides are compared after that
// normalization only; any other difference is another operation's comment.
func findComment(comments []gp.Comment, body string) (gp.Comment, bool) {
	want := normalizeCommentBody(body)
	for _, comment := range comments {
		if normalizeCommentBody(comment.Body) == want {
			return comment, true
		}
	}
	return gp.Comment{}, false
}

func normalizeCommentBody(s string) string {
	return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\r\n\t ")
}
