// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// A SESSION STARTED FROM A GITHUB ISSUE OR PULL REQUEST.
//
// A person gives a start the page of an issue or pull request. The engine reads
// that item with the same approved repository binding git_read uses for a clone,
// through the composition root's custody, and the start sends its title and body
// as the session's first message. Reading changes nothing on the host.

// SourceItem is the issue or pull request a session starts from.
type SourceItem struct {
	Number      int
	PullRequest bool
	Title, Body string
	URL         string
}

// SessionItemSource reads an item with the approved binding that lists the
// repository for this workspace. The composition root binds it; the custody that
// implements SessionGitReadSource implements it too, and a node whose custody
// does not has no item reads (deny closed).
type SessionItemSource interface {
	ReadSessionItem(ctx context.Context, tenant model.TenantID, workspace model.ID, owner, repo string, number int) (SourceItem, error)
}

// ErrSourceItemNotFound: the repository has no such issue or pull request, or
// the binding's credential cannot see it.
var ErrSourceItemNotFound = errors.New("sessions: the repository has no such issue or pull request")

// sourceItemHostTimeout bounds one item read: a token mint, a read and a revocation.
const sourceItemHostTimeout = 30 * time.Second

type sourceItemDTO struct {
	URL    string `json:"url"`
	Kind   string `json:"kind"` // issue or pull_request
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// handleSourceItem reads the issue or pull request a start names, for a registered
// workspace, with the approved repository binding that lists it. It reads what the
// person would read on the page, with a token that can read only that, so it asks
// what giving a session a repository credential asks: tenant administration.
func (m *Module) handleSourceItem(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.mayUseSecretEnv(r.Context(), mc) {
		writeRunErr(w, forbiddenErr("reading an issue or pull request with a repository credential needs tenant administration"))
		return
	}
	ref, err := parseSourceItemURL(r.URL.Query().Get("url"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	src, ok := m.rt.GitRead.(SessionItemSource)
	if !ok {
		writeRunErr(w, &runErr{http.StatusServiceUnavailable,
			"no Git-host custody is wired on this node, so an issue or pull request cannot be read"})
		return
	}
	rec, err := m.loadWorkspaceRec(r.Context(), mc.Tenant, r.URL.Query().Get("workspace_ref"))
	if err != nil {
		var re *runErr
		if errors.As(err, &re) && re.status == http.StatusNotFound {
			err = badRequest("workspace_ref is not a registered workspace")
		}
		writeRunErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sourceItemHostTimeout)
	defer cancel()
	item, err := src.ReadSessionItem(ctx, mc.Tenant, model.ID(rec.String(model.ColID)), ref.Owner, ref.Repo, ref.Number)
	switch {
	case errors.Is(err, ErrGitReadNotApproved):
		writeRunErr(w, &runErr{http.StatusUnprocessableEntity, fmt.Sprintf(
			"no approved GitHub repository binding lists %s/%s for this workspace; an administrator adds it to the repository credential's repositories and workspaces",
			ref.Owner, ref.Repo)})
	case errors.Is(err, ErrGitReadUnsupported):
		writeRunErr(w, &runErr{http.StatusUnprocessableEntity, "only GitHub repositories can be read for a session"})
	case errors.Is(err, ErrSourceItemNotFound):
		writeRunErr(w, &runErr{http.StatusNotFound, fmt.Sprintf(
			"%s/%s has no issue or pull request #%d, or the registered credential cannot see it", ref.Owner, ref.Repo, ref.Number)})
	case err != nil:
		m.warnf("sessions: an issue or pull request could not be read", "repository", ref.Owner+"/"+ref.Repo, "err", redactErr(err))
		writeRunErr(w, &runErr{http.StatusServiceUnavailable, "the issue or pull request could not be read; try again"})
	default:
		kind := "issue"
		if item.PullRequest {
			kind = "pull_request"
		}
		writeJSON(w, http.StatusOK, sourceItemDTO{URL: item.URL, Kind: kind, Number: item.Number, Title: item.Title, Body: item.Body})
	}
}

// sourceItemRef is the item a page URL names. Only github.com pages are taken;
// the approved binding decides whether this node may read the repository.
//
// ponytail: github.com only, not a GitHub Enterprise Server host. Upgrade trigger:
// a binding whose web base is not https://github.com (check the URL's host
// against the row's WebBase instead of the constant).
type sourceItemRef struct {
	Owner, Repo string
	Number      int
	PullRequest bool
}

const (
	sourceItemHost   = "github.com"
	maxSourceItemURL = 2048
	maxSourceItemNum = 1<<31 - 1
)

func badSourceItemURL() error {
	return badRequest("source_url must be the page of a GitHub issue or pull request (https://github.com/<owner>/<repo>/issues/<number> or /pull/<number>)")
}

// parseSourceItemURL reads the page URL of an issue or pull request. A query or
// fragment is ignored, a pull request page may end in /files or /commits, and
// nothing else is taken.
func parseSourceItemURL(raw string) (sourceItemRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxSourceItemURL {
		return sourceItemRef{}, badSourceItemURL()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, sourceItemHost) || u.User != nil {
		return sourceItemRef{}, badSourceItemURL()
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || (parts[2] != "issues" && parts[2] != "pull") || (parts[2] == "issues" && len(parts) != 4) {
		return sourceItemRef{}, badSourceItemURL()
	}
	ref := sourceItemRef{Owner: parts[0], Repo: parts[1], PullRequest: parts[2] == "pull"}
	if !sourceItemName(ref.Owner) || !sourceItemName(ref.Repo) || strings.Trim(parts[3], "0123456789") != "" {
		return sourceItemRef{}, badSourceItemURL()
	}
	n, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || n < 1 || n > maxSourceItemNum {
		return sourceItemRef{}, badSourceItemURL()
	}
	ref.Number = int(n)
	return ref, nil
}

// sourceItemName admits what GitHub allows in an owner or repository name.
func sourceItemName(s string) bool {
	if s == "" || len(s) > 100 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func (r sourceItemRef) kind() string {
	if r.PullRequest {
		return "pull"
	}
	return "issues"
}

// URL is the item's page in one form, whatever form it was given in.
func (r sourceItemRef) URL() string {
	return "https://" + sourceItemHost + "/" + r.Owner + "/" + r.Repo + "/" + r.kind() + "/" + strconv.Itoa(r.Number)
}

// branchStem names the item in its session's branch: issue-17- or pr-17-.
func (r sourceItemRef) branchStem() string {
	if r.PullRequest {
		return "pr-" + strconv.Itoa(r.Number) + "-"
	}
	return "issue-" + strconv.Itoa(r.Number) + "-"
}
