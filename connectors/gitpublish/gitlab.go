// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitLabConfig is one approved bot-token binding for one project.
type GitLabConfig struct {
	APIBase      string
	AllowedHosts []string
	ProjectPath  string
	Token        Secret
}

// GitLab is the GitLab write adapter for one project. GitLab mints nothing:
// the approved credential is a project or group bot token, checked at each
// Mint to belong to a bot user.
type GitLab struct {
	cfg GitLabConfig
	d   Doer
}

var _ Host = (*GitLab)(nil)

// NewGitLab validates the binding and returns the adapter.
func NewGitLab(cfg GitLabConfig, d Doer) (*GitLab, error) {
	cfg.APIBase = strings.TrimRight(cfg.APIBase, "/")
	if err := ValidateEndpoint(cfg.APIBase, cfg.AllowedHosts); err != nil {
		return nil, err
	}
	if cfg.ProjectPath == "" || cfg.Token.IsZero() || d == nil || strings.Contains(cfg.ProjectPath, "..") {
		return nil, errors.New("gitpublish: incomplete GitLab binding")
	}
	return &GitLab{cfg: cfg, d: d}, nil
}

func (g *GitLab) headers(t Token) map[string]string {
	return map[string]string{"PRIVATE-TOKEN": t.secret.Reveal(), "Accept": "application/json"}
}

func (g *GitLab) projectURL(suffix string) string {
	return g.cfg.APIBase + "/api/v4/projects/" + url.PathEscape(g.cfg.ProjectPath) + suffix
}

// Mint checks that the bot token belongs to a bot user and returns it.
func (g *GitLab) Mint(ctx context.Context, _ Effect) (Token, error) {
	tok := Token{secret: g.cfg.Token}
	c, err := do(ctx, g.d, http.MethodGet, g.cfg.APIBase+"/api/v4/user", g.headers(tok), nil)
	if err != nil {
		he := hostError(0, nil, ErrHostUnavailable)
		return Token{}, &he
	}
	if c.status != http.StatusOK {
		kind := ErrHostUnavailable
		if c.status >= 400 && c.status < 500 {
			kind = ErrCredentialRefused
		}
		he := hostError(c.status, c.header, kind)
		return Token{}, &he
	}
	var u struct {
		Bot bool `json:"bot"`
	}
	if json.Unmarshal(c.body, &u) != nil || !u.Bot {
		he := HostError{Status: c.status, Code: "not_bot", RequestID: requestID(c.header), kind: ErrCredentialRefused}
		return Token{}, &he
	}
	return tok, nil
}

// Release is a no-op: the bot token is the operator's approved credential.
func (g *GitLab) Release(context.Context, Token) error { return nil }

// Ref reads a branch head.
func (g *GitLab) Ref(ctx context.Context, t Token, branch string) (RefObservation, error) {
	c, err := do(ctx, g.d, http.MethodGet, g.projectURL("/repository/branches/"+url.PathEscape(branch)), g.headers(t), nil)
	if err != nil {
		he := hostError(0, nil, ErrHostUnavailable)
		return RefObservation{}, &he
	}
	switch c.status {
	case http.StatusOK:
		var out struct {
			Commit struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		if json.Unmarshal(c.body, &out) != nil || out.Commit.ID == "" {
			return RefObservation{}, ErrHostUnavailable
		}
		return RefObservation{State: RefPresent, SHA: out.Commit.ID}, nil
	case http.StatusNotFound:
		return RefObservation{State: RefNotFoundOrHidden}, nil
	}
	he := hostError(c.status, c.header, ErrHostUnavailable)
	return RefObservation{}, &he
}

// Branch reads the project's default branch and the branch's protection.
func (g *GitLab) Branch(ctx context.Context, t Token, branch string) (BranchInfo, error) {
	c, err := do(ctx, g.d, http.MethodGet, g.projectURL(""), g.headers(t), nil)
	if err != nil || c.status != http.StatusOK {
		he := hostError(c.status, c.header, ErrHostUnavailable)
		return BranchInfo{}, &he
	}
	var p struct {
		DefaultBranch string `json:"default_branch"`
	}
	if json.Unmarshal(c.body, &p) != nil || p.DefaultBranch == "" {
		return BranchInfo{}, ErrHostUnavailable
	}
	info := BranchInfo{Default: p.DefaultBranch}
	c, err = do(ctx, g.d, http.MethodGet, g.projectURL("/repository/branches/"+url.PathEscape(branch)), g.headers(t), nil)
	switch {
	case err != nil:
		he := hostError(0, nil, ErrHostUnavailable)
		return BranchInfo{}, &he
	case c.status == http.StatusNotFound:
		return info, nil
	case c.status != http.StatusOK:
		he := hostError(c.status, c.header, ErrHostUnavailable)
		return BranchInfo{}, &he
	}
	var b struct {
		Protected bool `json:"protected"`
		Default   bool `json:"default"`
	}
	if json.Unmarshal(c.body, &b) != nil {
		return BranchInfo{}, ErrHostUnavailable
	}
	info.Exists, info.Protected = true, b.Protected
	if b.Default {
		info.Default = branch
	}
	return info, nil
}

// CommitTree is not offered by the GitLab commits API; the push check on GitLab
// relies on the content-addressed commit id.
func (g *GitLab) CommitTree(context.Context, Token, string) (string, error) {
	return "", ErrNotSupported
}

type glMR struct {
	IID            int       `json:"iid"`
	State          string    `json:"state"`
	SHA            string    `json:"sha"`
	MergeCommitSHA string    `json:"merge_commit_sha"`
	SourceBranch   string    `json:"source_branch"`
	TargetBranch   string    `json:"target_branch"`
	CreatedAt      time.Time `json:"created_at"`
}

func (m glMR) change() Change {
	return Change{Number: m.IID, Open: m.State == "opened", Merged: m.State == "merged", HeadRef: m.SourceBranch, HeadSHA: m.SHA, BaseRef: m.TargetBranch, MergeCommitSHA: m.MergeCommitSHA, CreatedAt: m.CreatedAt}
}

// OpenChanges lists opened merge requests for head into base, all pages.
func (g *GitLab) OpenChanges(ctx context.Context, t Token, head, base string) ([]Change, error) {
	var out []Change
	page := "1"
	for n := 0; page != ""; n++ {
		if n == maxPages {
			return nil, ErrLookupIncomplete
		}
		q := url.Values{"state": {"opened"}, "source_branch": {head}, "target_branch": {base}, "per_page": {"100"}, "page": {page}}
		c, err := do(ctx, g.d, http.MethodGet, g.projectURL("/merge_requests?"+q.Encode()), g.headers(t), nil)
		if err != nil || c.status != http.StatusOK {
			he := hostError(c.status, c.header, ErrLookupIncomplete)
			return nil, &he
		}
		var mrs []glMR
		if json.Unmarshal(c.body, &mrs) != nil {
			return nil, ErrLookupIncomplete
		}
		for _, m := range mrs {
			out = append(out, m.change())
		}
		page = c.header.Get("X-Next-Page")
		if page != "" {
			if _, err := strconv.Atoi(page); err != nil {
				return nil, ErrLookupIncomplete
			}
		}
	}
	return out, nil
}

// GetChange reads one merge request.
func (g *GitLab) GetChange(ctx context.Context, t Token, number int) (Change, error) {
	c, err := do(ctx, g.d, http.MethodGet, g.projectURL("/merge_requests/"+strconv.Itoa(number)), g.headers(t), nil)
	if err != nil || c.status != http.StatusOK {
		he := hostError(c.status, c.header, ErrHostUnavailable)
		return Change{}, &he
	}
	var m glMR
	if json.Unmarshal(c.body, &m) != nil {
		return Change{}, ErrHostUnavailable
	}
	return m.change(), nil
}

// CreateChange opens a merge request. The host binds branches, not SHAs.
func (g *GitLab) CreateChange(ctx context.Context, t Token, spec ChangeSpec) (Change, Result) {
	title := spec.Title
	if spec.Draft {
		title = "Draft: " + title
	}
	body := map[string]any{"source_branch": spec.Head, "target_branch": spec.Base, "title": title, "description": spec.Body}
	c, err := do(ctx, g.d, http.MethodPost, g.projectURL("/merge_requests"), g.headers(t), body)
	if err != nil {
		return Change{}, Result{Class: Ambiguous, Reason: "transport", Host: hostError(c.status, c.header, nil)}
	}
	res := classifyWrite(c.status, c.header, http.StatusCreated, map[int]string{
		http.StatusConflict: "pull_request_exists", http.StatusBadRequest: "validation_failed", http.StatusUnprocessableEntity: "validation_failed",
	})
	if res.Class != Applied {
		return Change{}, res
	}
	var m glMR
	if json.Unmarshal(c.body, &m) != nil || m.IID == 0 {
		return Change{}, Result{Class: Ambiguous, Reason: "malformed_response", Host: res.Host}
	}
	return m.change(), res
}

// MergeChange accepts a merge request with sha as the required source head.
// auto_merge is never sent: v1 has no deferred host merge.
func (g *GitLab) MergeChange(ctx context.Context, t Token, number int, sha, method string) (MergeOutcome, Result) {
	// The merge call cannot express a rebase: refuse it rather than send a
	// plain merge in its place.
	if method != "merge" && method != "squash" {
		return MergeOutcome{}, Result{Class: Rejected, Reason: "unsupported_method"}
	}
	body := map[string]any{"sha": sha}
	if method == "squash" {
		body["squash"] = true
	}
	c, err := do(ctx, g.d, http.MethodPut, g.projectURL("/merge_requests/"+strconv.Itoa(number)+"/merge"), g.headers(t), body)
	if err != nil {
		return MergeOutcome{}, Result{Class: Ambiguous, Reason: "transport", Host: hostError(c.status, c.header, nil)}
	}
	res := classifyWrite(c.status, c.header, http.StatusOK, map[int]string{
		http.StatusConflict: "head_mismatch", http.StatusMethodNotAllowed: "not_mergeable", http.StatusUnprocessableEntity: "not_mergeable", http.StatusBadRequest: "validation_failed",
	})
	if res.Class != Applied {
		return MergeOutcome{}, res
	}
	var m glMR
	_ = json.Unmarshal(c.body, &m)
	return MergeOutcome{Merged: m.State == "merged", MergeCommitSHA: m.MergeCommitSHA}, res
}

// PushTarget returns the git URL of the project on the binding's host.
func (g *GitLab) PushTarget(t Token) (string, string, Secret) {
	basic := base64.StdEncoding.EncodeToString([]byte("oauth2:" + t.secret.Reveal()))
	return g.cfg.APIBase + "/" + g.cfg.ProjectPath + ".git", "https", NewSecret("Authorization: Basic " + basic)
}
