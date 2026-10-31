// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHubAPIVersion is the pinned REST API version sent on every call
// (X-GitHub-Api-Version), as the host documents it.
const GitHubAPIVersion = "2026-03-10"

// maxPages bounds a list walk; more pages make the lookup incomplete.
const maxPages = 10

// Host is the one-effect host surface the module drives. Every method is a
// single bounded call; nothing is retried here.
type Host interface {
	// Mint returns a capability for one effect. Release it on every path.
	Mint(ctx context.Context, e Effect) (Token, error)
	// Release gives the capability back (GitHub revokes; GitLab no-op).
	Release(ctx context.Context, t Token) error
	Ref(ctx context.Context, t Token, branch string) (RefObservation, error)
	// Branch reports the default branch and whether branch is protected.
	Branch(ctx context.Context, t Token, branch string) (BranchInfo, error)
	CommitTree(ctx context.Context, t Token, sha string) (string, error)
	// OpenChanges lists OPEN changes for head and base, completely, or fails
	// with ErrLookupIncomplete.
	OpenChanges(ctx context.Context, t Token, head, base string) ([]Change, error)
	GetChange(ctx context.Context, t Token, number int) (Change, error)
	CreateChange(ctx context.Context, t Token, spec ChangeSpec) (Change, Result)
	MergeChange(ctx context.Context, t Token, number int, sha, method string) (MergeOutcome, Result)
	// PushTarget returns the admitted push URL, its scheme and the git
	// authorization header line for t.
	PushTarget(t Token) (pushURL, scheme string, header Secret)
}

// CommentHost is the additive issue-comment surface. Host implementations
// that predate comments remain valid Host values.
type CommentHost interface {
	CreateComment(ctx context.Context, t Token, spec CommentSpec) (Comment, Result)
	ListComments(ctx context.Context, t Token, number int) ([]Comment, error)
}

// CreateComment dispatches through a host that offers comments.
func CreateComment(ctx context.Context, host Host, t Token, spec CommentSpec) (Comment, Result) {
	h, ok := host.(CommentHost)
	if !ok {
		return Comment{}, Result{Class: Rejected, Reason: "unsupported_effect", Host: HostError{Status: 422, Code: "validation_failed"}}
	}
	return h.CreateComment(ctx, t, spec)
}

// ListComments reads through a host that offers comments.
func ListComments(ctx context.Context, host Host, t Token, number int) ([]Comment, error) {
	h, ok := host.(CommentHost)
	if !ok {
		return nil, ErrNotSupported
	}
	return h.ListComments(ctx, t, number)
}

// GitHubConfig is one approved credential binding for one repository.
type GitHubConfig struct {
	APIBase        string
	AllowedHosts   []string
	AppID          string
	InstallationID string
	Key            Secret
	Owner, Repo    string
	Now            func() time.Time
}

// GitHub is the GitHub App write adapter for one repository.
type GitHub struct {
	cfg GitHubConfig
	d   Doer
}

var _ Host = (*GitHub)(nil)

// NewGitHub validates the binding and returns the adapter.
func NewGitHub(cfg GitHubConfig, d Doer) (*GitHub, error) {
	cfg.APIBase = strings.TrimRight(cfg.APIBase, "/")
	if err := ValidateEndpoint(cfg.APIBase, cfg.AllowedHosts); err != nil {
		return nil, err
	}
	if cfg.AppID == "" || cfg.InstallationID == "" || cfg.Key.IsZero() || cfg.Owner == "" || cfg.Repo == "" || d == nil {
		return nil, errors.New("gitpublish: incomplete GitHub binding")
	}
	if _, err := strconv.ParseUint(cfg.InstallationID, 10, 64); err != nil {
		return nil, errors.New("gitpublish: installation id must be numeric")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &GitHub{cfg: cfg, d: d}, nil
}

func (g *GitHub) headers(bearer string) map[string]string {
	return map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": GitHubAPIVersion,
		"Authorization":        "Bearer " + bearer,
	}
}

func (g *GitHub) repoURL(suffix string) string {
	return g.cfg.APIBase + "/repos/" + url.PathEscape(g.cfg.Owner) + "/" + url.PathEscape(g.cfg.Repo) + suffix
}

func githubWebBase(apiBase string) string {
	u, err := url.Parse(apiBase)
	if err != nil || u.Host == "" {
		return ""
	}
	if strings.EqualFold(u.Hostname(), "api.github.com") {
		return "https://github.com"
	}
	return "https://" + strings.ToLower(u.Host)
}

func permissionsFor(e Effect) map[string]string {
	switch e {
	case EffectPush:
		return map[string]string{"contents": "write"}
	case EffectPullRequest:
		return map[string]string{"pull_requests": "write", "contents": "read"}
	case EffectMerge:
		return map[string]string{"contents": "write", "pull_requests": "read"}
	case EffectRead:
		return map[string]string{"contents": "read"}
	case EffectItemRead:
		return map[string]string{"issues": "read", "pull_requests": "read"}
	case EffectIssueComment:
		return map[string]string{"issues": "write"}
	case EffectIssueCommentObserve:
		return map[string]string{"issues": "read"}
	}
	return map[string]string{"contents": "read", "pull_requests": "read"}
}

// Mint signs an App JWT and exchanges it for an installation token narrowed to
// this repository and the effect's permissions.
func (g *GitHub) Mint(ctx context.Context, e Effect) (Token, error) {
	jwt, err := appJWT(g.cfg.Key, g.cfg.AppID, g.cfg.Now())
	if err != nil {
		return Token{}, fmt.Errorf("%w: app key", ErrCredentialRefused)
	}
	body := map[string]any{"repositories": []string{g.cfg.Repo}, "permissions": permissionsFor(e)}
	c, err := do(ctx, g.d, http.MethodPost, g.cfg.APIBase+"/app/installations/"+g.cfg.InstallationID+"/access_tokens", g.headers(jwt.Reveal()), body)
	if err != nil {
		he := hostError(0, nil, ErrHostUnavailable)
		return Token{}, &he
	}
	if c.status != http.StatusCreated {
		kind := ErrHostUnavailable
		if c.status >= 400 && c.status < 500 {
			kind = ErrCredentialRefused
		}
		he := hostError(c.status, c.header, kind)
		return Token{}, &he
	}
	var out struct {
		Token        string    `json:"token"`
		ExpiresAt    time.Time `json:"expires_at"`
		Repositories []struct {
			Name string `json:"name"`
		} `json:"repositories"`
	}
	if json.Unmarshal(c.body, &out) != nil || out.Token == "" {
		return Token{}, fmt.Errorf("%w: malformed token response", ErrCredentialRefused)
	}
	tok := Token{secret: NewSecret(out.Token), ExpiresAt: out.ExpiresAt}
	for _, r := range out.Repositories {
		tok.Repositories = append(tok.Repositories, r.Name)
	}
	if len(tok.Repositories) != 1 || !strings.EqualFold(tok.Repositories[0], g.cfg.Repo) {
		if err := g.Release(ctx, tok); err != nil {
			return Token{}, errors.Join(ErrTokenScope, err)
		}
		return Token{}, ErrTokenScope
	}
	return tok, nil
}

// Release revokes the installation token.
func (g *GitHub) Release(ctx context.Context, t Token) error {
	if t.IsZero() {
		return nil
	}
	c, err := do(ctx, g.d, http.MethodDelete, g.cfg.APIBase+"/installation/token", g.headers(t.secret.Reveal()), nil)
	if err != nil {
		he := hostError(0, nil, ErrHostUnavailable)
		return &he
	}
	if c.status != http.StatusNoContent {
		he := hostError(c.status, c.header, ErrHostUnavailable)
		return &he
	}
	return nil
}

// Ref reads refs/heads/<branch>.
func (g *GitHub) Ref(ctx context.Context, t Token, branch string) (RefObservation, error) {
	c, err := do(ctx, g.d, http.MethodGet, g.repoURL("/git/ref/heads/"+escapeRef(branch)), g.headers(t.secret.Reveal()), nil)
	if err != nil {
		he := hostError(0, nil, ErrHostUnavailable)
		return RefObservation{}, &he
	}
	switch c.status {
	case http.StatusOK:
		var out struct {
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}
		if json.Unmarshal(c.body, &out) != nil || out.Object.SHA == "" {
			return RefObservation{}, ErrHostUnavailable
		}
		return RefObservation{State: RefPresent, SHA: out.Object.SHA}, nil
	case http.StatusNotFound:
		return RefObservation{State: RefNotFoundOrHidden}, nil
	}
	he := hostError(c.status, c.header, ErrHostUnavailable)
	return RefObservation{}, &he
}

// Branch reads the repository's default branch and the branch's protection.
func (g *GitHub) Branch(ctx context.Context, t Token, branch string) (BranchInfo, error) {
	c, err := do(ctx, g.d, http.MethodGet, g.repoURL(""), g.headers(t.secret.Reveal()), nil)
	if err != nil || c.status != http.StatusOK {
		he := hostError(c.status, c.header, ErrHostUnavailable)
		return BranchInfo{}, &he
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if json.Unmarshal(c.body, &repo) != nil || repo.DefaultBranch == "" {
		return BranchInfo{}, ErrHostUnavailable
	}
	info := BranchInfo{Default: repo.DefaultBranch}
	c, err = do(ctx, g.d, http.MethodGet, g.repoURL("/branches/"+escapeRef(branch)), g.headers(t.secret.Reveal()), nil)
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
	}
	if json.Unmarshal(c.body, &b) != nil {
		return BranchInfo{}, ErrHostUnavailable
	}
	info.Exists, info.Protected = true, b.Protected
	return info, nil
}

// CommitTree reads a commit's tree id.
func (g *GitHub) CommitTree(ctx context.Context, t Token, sha string) (string, error) {
	c, err := do(ctx, g.d, http.MethodGet, g.repoURL("/git/commits/"+url.PathEscape(sha)), g.headers(t.secret.Reveal()), nil)
	if err != nil || c.status != http.StatusOK {
		he := hostError(c.status, c.header, ErrHostUnavailable)
		return "", &he
	}
	var out struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if json.Unmarshal(c.body, &out) != nil || out.Tree.SHA == "" {
		return "", ErrHostUnavailable
	}
	return out.Tree.SHA, nil
}

type ghPull struct {
	Number         int       `json:"number"`
	State          string    `json:"state"`
	Merged         bool      `json:"merged"`
	MergeCommitSHA string    `json:"merge_commit_sha"`
	CreatedAt      time.Time `json:"created_at"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p ghPull) change() Change {
	return Change{Number: p.Number, Open: p.State == "open", Merged: p.Merged, HeadRef: p.Head.Ref, HeadSHA: p.Head.SHA, BaseRef: p.Base.Ref, MergeCommitSHA: p.MergeCommitSHA, CreatedAt: p.CreatedAt}
}

// OpenChanges lists open pull requests for owner:head into base, all pages.
func (g *GitHub) OpenChanges(ctx context.Context, t Token, head, base string) ([]Change, error) {
	q := url.Values{"state": {"open"}, "head": {g.cfg.Owner + ":" + head}, "base": {base}, "per_page": {"100"}}
	next := g.repoURL("/pulls?" + q.Encode())
	var out []Change
	for page := 0; next != ""; page++ {
		if page == maxPages || !sameOrigin(g.cfg.APIBase, next) {
			return nil, ErrLookupIncomplete
		}
		c, err := do(ctx, g.d, http.MethodGet, next, g.headers(t.secret.Reveal()), nil)
		if err != nil || c.status != http.StatusOK {
			he := hostError(c.status, c.header, ErrLookupIncomplete)
			return nil, &he
		}
		var pulls []ghPull
		if json.Unmarshal(c.body, &pulls) != nil {
			return nil, ErrLookupIncomplete
		}
		for _, p := range pulls {
			out = append(out, p.change())
		}
		next = nextLink(c.header)
	}
	return out, nil
}

// GetChange reads one pull request.
func (g *GitHub) GetChange(ctx context.Context, t Token, number int) (Change, error) {
	c, err := do(ctx, g.d, http.MethodGet, g.repoURL("/pulls/"+strconv.Itoa(number)), g.headers(t.secret.Reveal()), nil)
	if err != nil || c.status != http.StatusOK {
		he := hostError(c.status, c.header, ErrHostUnavailable)
		return Change{}, &he
	}
	var p ghPull
	if json.Unmarshal(c.body, &p) != nil {
		return Change{}, ErrHostUnavailable
	}
	return p.change(), nil
}

// CreateChange opens a pull request. The host binds branches, not SHAs.
func (g *GitHub) CreateChange(ctx context.Context, t Token, spec ChangeSpec) (Change, Result) {
	body := map[string]any{"title": spec.Title, "head": spec.Head, "base": spec.Base, "body": spec.Body, "draft": spec.Draft}
	c, err := do(ctx, g.d, http.MethodPost, g.repoURL("/pulls"), g.headers(t.secret.Reveal()), body)
	if err != nil {
		return Change{}, Result{Class: Ambiguous, Reason: "transport", Host: hostError(c.status, c.header, nil)}
	}
	res := classifyWrite(c.status, c.header, http.StatusCreated, map[int]string{http.StatusUnprocessableEntity: "validation_failed"})
	if res.Class != Applied {
		return Change{}, res
	}
	var p ghPull
	if json.Unmarshal(c.body, &p) != nil || p.Number == 0 {
		return Change{}, Result{Class: Ambiguous, Reason: "malformed_response", Host: res.Host}
	}
	return p.change(), res
}

// MergeChange merges with sha as the required source head.
func (g *GitHub) MergeChange(ctx context.Context, t Token, number int, sha, method string) (MergeOutcome, Result) {
	body := map[string]any{"sha": sha, "merge_method": method}
	c, err := do(ctx, g.d, http.MethodPut, g.repoURL("/pulls/"+strconv.Itoa(number)+"/merge"), g.headers(t.secret.Reveal()), body)
	if err != nil {
		return MergeOutcome{}, Result{Class: Ambiguous, Reason: "transport", Host: hostError(c.status, c.header, nil)}
	}
	res := classifyWrite(c.status, c.header, http.StatusOK, map[int]string{
		http.StatusConflict: "head_mismatch", http.StatusMethodNotAllowed: "not_mergeable", http.StatusUnprocessableEntity: "validation_failed",
	})
	if res.Class != Applied {
		return MergeOutcome{}, res
	}
	var out struct {
		Merged bool   `json:"merged"`
		SHA    string `json:"sha"`
	}
	if json.Unmarshal(c.body, &out) != nil || !out.Merged || out.SHA == "" {
		return MergeOutcome{}, Result{Class: Ambiguous, Reason: "malformed_response", Host: res.Host}
	}
	return MergeOutcome{Merged: true, MergeCommitSHA: out.SHA}, res
}

type ghComment struct {
	ID        int64     `json:"id"`
	HTMLURL   string    `json:"html_url"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func (c ghComment) comment() Comment {
	return Comment{ID: strconv.FormatInt(c.ID, 10), URL: c.HTMLURL, Body: c.Body, CreatedAt: c.CreatedAt}
}

func (g *GitHub) validCommentURL(raw string, number int, id int64) bool {
	for _, kind := range []string{"issues", "pull"} {
		expected := githubWebBase(g.cfg.APIBase) + "/" + g.cfg.Owner + "/" + g.cfg.Repo + "/" + kind + "/" + strconv.Itoa(number) + "#issuecomment-" + strconv.FormatInt(id, 10)
		if strings.EqualFold(canonicalCommentURL(raw), canonicalCommentURL(expected)) {
			return true
		}
	}
	return false
}

func canonicalCommentURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Port() == "443" {
		u.Host = strings.TrimSuffix(u.Host, ":443")
	}
	return u.String()
}

// CreateComment adds one comment to an issue.
func (g *GitHub) CreateComment(ctx context.Context, t Token, spec CommentSpec) (Comment, Result) {
	c, err := do(ctx, g.d, http.MethodPost, g.repoURL("/issues/"+strconv.Itoa(spec.Number)+"/comments"), g.headers(t.secret.Reveal()), map[string]string{"body": spec.Body})
	if err != nil {
		return Comment{}, Result{Class: Ambiguous, Reason: "transport", Host: hostError(c.status, c.header, nil)}
	}
	res := classifyWrite(c.status, c.header, http.StatusCreated, map[int]string{http.StatusBadRequest: "validation_failed", http.StatusUnprocessableEntity: "validation_failed"})
	if res.Class != Applied {
		return Comment{}, res
	}
	var out ghComment
	if json.Unmarshal(c.body, &out) != nil || out.ID == 0 || !g.validCommentURL(out.HTMLURL, spec.Number, out.ID) {
		return Comment{}, Result{Class: Ambiguous, Reason: "malformed_response", Host: res.Host}
	}
	return out.comment(), res
}

// ListComments lists every comment on an issue, up to the shared page bound.
func (g *GitHub) ListComments(ctx context.Context, t Token, number int) ([]Comment, error) {
	q := url.Values{"per_page": {"100"}}
	next := g.repoURL("/issues/" + strconv.Itoa(number) + "/comments?" + q.Encode())
	var out []Comment
	for page := 0; next != ""; page++ {
		if page == maxPages || !sameOrigin(g.cfg.APIBase, next) {
			return nil, ErrLookupIncomplete
		}
		c, err := do(ctx, g.d, http.MethodGet, next, g.headers(t.secret.Reveal()), nil)
		if err != nil || c.status != http.StatusOK {
			he := hostError(c.status, c.header, ErrLookupIncomplete)
			return nil, &he
		}
		var comments []ghComment
		if json.Unmarshal(c.body, &comments) != nil {
			return nil, ErrLookupIncomplete
		}
		for _, comment := range comments {
			if comment.ID == 0 || !g.validCommentURL(comment.HTMLURL, number, comment.ID) {
				return nil, ErrLookupIncomplete
			}
			out = append(out, comment.comment())
		}
		next = nextLink(c.header)
	}
	return out, nil
}

// PushTarget returns the git URL of the repository on the binding's host.
func (g *GitHub) PushTarget(t Token) (string, string, Secret) {
	gitBase := githubWebBase(g.cfg.APIBase)
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + t.secret.Reveal()))
	return gitBase + "/" + g.cfg.Owner + "/" + g.cfg.Repo + ".git", "https", NewSecret("Authorization: Basic " + basic)
}

// appJWT signs the App JWT: RS256, iat = now-60s, exp = now+9m, iss = App ID.
func appJWT(key Secret, appID string, now time.Time) (Secret, error) {
	block, _ := pem.Decode([]byte(key.Reveal()))
	if block == nil {
		return Secret{}, errors.New("no pem")
	}
	var priv *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		priv = k
	} else if k8, err8 := x509.ParsePKCS8PrivateKey(block.Bytes); err8 == nil {
		rk, ok := k8.(*rsa.PrivateKey)
		if !ok {
			return Secret{}, errors.New("not rsa")
		}
		priv = rk
	} else {
		return Secret{}, errors.New("unparsable key")
	}
	enc := base64.RawURLEncoding
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": appID})
	signing := enc.EncodeToString(hdr) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	if err != nil {
		return Secret{}, err
	}
	return NewSecret(signing + "." + enc.EncodeToString(sig)), nil
}
