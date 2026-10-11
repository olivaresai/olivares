// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// PlainRemote is one validated plain-git remote: a git repository reachable
// over SSH or HTTPS, with no pull-request or merge API. It is the operator's
// approved destination; nothing here discovers one.
type PlainRemote struct {
	URL    string // the remote URL as validated
	Scheme string // "ssh" or "https"
	Host   string // lowercase host, without port
	Path   string // repository path without the leading slash
	User   string // ssh only: the remote user
}

// ParsePlainRemote validates one plain-git remote URL and returns it. SSH
// takes an explicit user, any port and a DNS name or IP literal (the pinned
// host key is the trust anchor); HTTPS keeps the endpoint rules (no userinfo,
// a DNS name, the default port). file:// is not a publication remote.
func ParsePlainRemote(raw string) (PlainRemote, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return PlainRemote{}, fmt.Errorf("%w: unparsable remote", ErrEndpoint)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return PlainRemote{}, fmt.Errorf("%w: query or fragment", ErrEndpoint)
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == ".." || seg == "." {
			return PlainRemote{}, fmt.Errorf("%w: dot segment", ErrEndpoint)
		}
	}
	host := strings.ToLower(u.Hostname())
	path := strings.TrimPrefix(u.Path, "/")
	if !ValidRepoPath(path) {
		return PlainRemote{}, fmt.Errorf("%w: repository path", ErrEndpoint)
	}
	switch u.Scheme {
	case "ssh", "https":
		// The executor admits the same raw lowercase prefix; agree with it
		// here so an approved remote always publishes.
		if !strings.HasPrefix(raw, u.Scheme+"://") {
			return PlainRemote{}, fmt.Errorf("%w: scheme must be ssh or https", ErrEndpoint)
		}
	default:
		return PlainRemote{}, fmt.Errorf("%w: scheme must be ssh or https", ErrEndpoint)
	}
	if u.Scheme == "ssh" {
		var password string
		if u.User != nil {
			password, _ = u.User.Password()
		}
		if u.User == nil || !admittedSSHUser(u.User.Username()) || password != "" {
			return PlainRemote{}, fmt.Errorf("%w: ssh needs user@ and no password", ErrEndpoint)
		}
		if p := u.Port(); p != "" {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				return PlainRemote{}, fmt.Errorf("%w: port", ErrEndpoint)
			}
		}
		if host == "" || net.ParseIP(host) == nil && !isDNSName(host) {
			return PlainRemote{}, fmt.Errorf("%w: host", ErrEndpoint)
		}
		return PlainRemote{URL: raw, Scheme: "ssh", Host: host, Path: path, User: u.User.Username()}, nil
	}
	if u.User != nil {
		return PlainRemote{}, fmt.Errorf("%w: userinfo", ErrEndpoint)
	}
	if err := ValidateEndpoint(raw, []string{host}); err != nil {
		return PlainRemote{}, err
	}
	return PlainRemote{URL: raw, Scheme: "https", Host: host, Path: path}, nil
}

// ValidRepoPath is the one rule for a repository path without its leading
// slash: owner/name (GitLab: group/…/project), at least two plain segments of
// letters, digits, dot, underscore and hyphen. Custody and the plain-git
// remote parser share it, so a row that opens is a row that publishes.
func ValidRepoPath(path string) bool {
	segments := strings.Split(path, "/")
	if len(path) > 512 || len(segments) < 2 {
		return false
	}
	for _, s := range segments {
		if s == "" || s == "." || s == ".." {
			return false
		}
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			default:
				return false
			}
		}
	}
	return true
}

// isDNSName accepts the labels a plain-git host may have: letters, digits and
// hyphens, each label non-empty (the URL parser already rejects spaces and
// most delimiters).
func isDNSName(host string) bool {
	if len(host) > 253 || host == "" || strings.HasPrefix(host, "-") || strings.HasSuffix(host, "-") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			default:
				return false
			}
		}
	}
	return true
}

// PlainGitConfig is one approved plain-git binding: one remote and the
// credential the sealed store holds for it. For SSH the credential is the
// private key (no passphrase); for HTTPS it is "<user>:<token-or-password>"
// sent as one Basic header.
type PlainGitConfig struct {
	Remote     string
	Credential Secret
}

// PlainGit is the write adapter for one plain-git remote. Plain git offers no
// token, change or merge API: Mint returns the binding's own credential, the
// preflight read proves the remote, and every change effect is refused.
type PlainGit struct {
	cfg PlainGitConfig
	x   *Executor
}

var _ Host = (*PlainGit)(nil)

// NewPlainGit validates the binding and returns the adapter. x is the closed
// executor every git invocation runs through. A file:// remote is admitted as
// the test transport only (the custody seam never names one); its shape is
// still checked.
func NewPlainGit(cfg PlainGitConfig, x *Executor) (*PlainGit, error) {
	if x == nil {
		return nil, errors.New("gitpublish: plain git needs the closed executor")
	}
	raw := strings.TrimSpace(cfg.Remote)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: unparsable remote", ErrEndpoint)
	}
	switch u.Scheme {
	case "ssh", "https":
		if _, err := ParsePlainRemote(raw); err != nil {
			return nil, err
		}
	case "file":
		if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || !strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "..") {
			return nil, fmt.Errorf("%w: repository path", ErrEndpoint)
		}
	default:
		return nil, fmt.Errorf("%w: scheme must be ssh or https", ErrEndpoint)
	}
	if cfg.Credential.IsZero() {
		return nil, errors.New("gitpublish: incomplete plain-git binding")
	}
	if u.Scheme == "https" && strings.ContainsAny(cfg.Credential.Reveal(), "\x00\r\n") {
		// The https credential becomes one header line; a key is multiline
		// by design and only its marker is checked.
		return nil, errors.New("gitpublish: the https credential holds control characters")
	}
	if u.Scheme == "https" && !strings.Contains(cfg.Credential.Reveal(), ":") {
		return nil, errors.New("gitpublish: the https credential must be \"<user>:<token-or-password>\"")
	}
	if u.Scheme == "ssh" && !strings.Contains(cfg.Credential.Reveal(), "PRIVATE KEY") {
		return nil, errors.New("gitpublish: the ssh credential must be a private key")
	}
	return &PlainGit{cfg: PlainGitConfig{Remote: raw, Credential: cfg.Credential}, x: x}, nil
}

// remoteRef is the admitted remote with its credential, for reads.
func (p *PlainGit) remoteRef(t Token) RemoteRef {
	r := RemoteRef{URL: p.cfg.Remote, Scheme: plainScheme(p.cfg.Remote), Key: t.secret}
	if r.Scheme == "https" {
		r.Header, r.Key = httpsBasicHeader(t.secret), Secret{}
	}
	return r
}

// plainScheme is the remote's scheme as the executor admits it.
func plainScheme(remote string) string {
	if strings.HasPrefix(remote, "ssh://") {
		return "ssh"
	}
	if strings.HasPrefix(remote, "https://") {
		return "https"
	}
	return "file"
}

// httpsBasicHeader builds the one Basic header line a plain https remote
// authenticates with.
func httpsBasicHeader(cred Secret) Secret {
	basic := base64.StdEncoding.EncodeToString([]byte(cred.Reveal()))
	return NewSecret("Authorization: Basic " + basic)
}

// plainBranchRe bounds a branch name the adapter passes to ls-remote.
var plainBranchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// Mint returns the binding's own credential: plain git has nothing to mint.
// The preflight read (Branch, Ref) proves the remote and the credential
// together before anything is dispatched.
func (p *PlainGit) Mint(context.Context, Effect) (Token, error) {
	return Token{secret: p.cfg.Credential}, nil
}

// Release is a no-op: the credential is the operator's approved secret.
func (p *PlainGit) Release(context.Context, Token) error { return nil }

// Ref reads one branch head from the remote.
func (p *PlainGit) Ref(ctx context.Context, t Token, branch string) (RefObservation, error) {
	if branch == "" || !plainBranchRe.MatchString(branch) || strings.Contains(branch, "..") {
		return RefObservation{}, ErrNotSupported
	}
	out, err := p.x.LsRemote(ctx, p.remoteRef(t), false, "refs/heads/"+branch)
	if err != nil {
		return RefObservation{}, err
	}
	if sha := remoteRefSHA(out, "refs/heads/"+branch); sha != "" {
		return RefObservation{State: RefPresent, SHA: sha}, nil
	}
	return RefObservation{State: RefNotFoundOrHidden}, nil
}

// Branch reads the remote's default branch (the HEAD symref) and whether
// branch exists, in one ls-remote. Plain git has no protection API, so
// Protected is always false: the refusal set is the default branch by name
// plus the target's own prefix and merge-base rules. A HEAD that advertises
// no symref (unborn or detached) names no default branch, and a read that
// cannot name it is refused, never taken as "unprotected".
func (p *PlainGit) Branch(ctx context.Context, t Token, branch string) (BranchInfo, error) {
	if branch == "" || !plainBranchRe.MatchString(branch) || strings.Contains(branch, "..") {
		return BranchInfo{}, ErrNotSupported
	}
	out, err := p.x.LsRemote(ctx, p.remoteRef(t), true, "HEAD", "refs/heads/"+branch)
	if err != nil {
		return BranchInfo{}, err
	}
	def := remoteSymref(out)
	if def == "" {
		return BranchInfo{}, &HostError{Status: 0, Code: "no_symref", kind: ErrLookupIncomplete}
	}
	return BranchInfo{Default: def, Exists: remoteRefSHA(out, "refs/heads/"+branch) != ""}, nil
}

// CommitTree is not offered: there is no plain-git tree API, and a merge on a
// plain-git target never reaches evidence.
func (p *PlainGit) CommitTree(context.Context, Token, string) (string, error) {
	return "", ErrNotSupported
}

// OpenChanges: a plain-git remote offers no change lookup.
func (p *PlainGit) OpenChanges(context.Context, Token, string, string) ([]Change, error) {
	return nil, ErrLookupIncomplete
}

// GetChange: a plain-git remote offers no change lookup.
func (p *PlainGit) GetChange(context.Context, Token, int) (Change, error) {
	return Change{}, ErrNotSupported
}

// CreateChange is not offered on a plain-git target.
func (p *PlainGit) CreateChange(context.Context, Token, ChangeSpec) (Change, Result) {
	return Change{}, Result{Class: Rejected, Reason: "unsupported_effect", Host: HostError{Status: 422, Code: "validation_failed"}}
}

// MergeChange is not offered on a plain-git target.
func (p *PlainGit) MergeChange(context.Context, Token, int, string, string) (MergeOutcome, Result) {
	return MergeOutcome{}, Result{Class: Rejected, Reason: "unsupported_effect", Host: HostError{Status: 422, Code: "validation_failed"}}
}

// PushTarget returns the remote, its scheme and the one authorization header
// (https). SSH authenticates with the binding's key, which the executor
// materializes; the header stays zero.
func (p *PlainGit) PushTarget(t Token) (string, string, Secret) {
	scheme := plainScheme(p.cfg.Remote)
	if scheme == "https" {
		return p.cfg.Remote, scheme, httpsBasicHeader(t.secret)
	}
	return p.cfg.Remote, scheme, Secret{}
}

// remoteRefSHA reads the object id of ref from `git ls-remote` output.
func remoteRefSHA(out, ref string) string {
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 && parts[1] == ref && shaRe.MatchString(strings.TrimSpace(parts[0])) {
			return strings.TrimSpace(parts[0])
		}
	}
	return ""
}

// remoteSymref reads the branch HEAD points at from `git ls-remote --symref`
// output ("ref: refs/heads/<branch>\tHEAD"), or "". Only HEAD's own symref
// line names the default branch; any other advertised symref is ignored.
func remoteSymref(out string) string {
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, "ref: ")
		if !ok || !strings.HasSuffix(rest, "\tHEAD") {
			continue
		}
		name := strings.TrimSpace(strings.TrimSuffix(rest, "\tHEAD"))
		if name, ok := strings.CutPrefix(name, "refs/heads/"); ok && name != "" && !strings.Contains(name, "..") {
			return name
		}
	}
	return ""
}
