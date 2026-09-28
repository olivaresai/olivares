// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrRepositoryConfig: the server repository's config is not the managed
	// config. Nothing was dispatched.
	ErrRepositoryConfig = errors.New("gitpublish: server repository config is not the managed config")
	// ErrDestination: the push destination, scheme or ref is not the admitted
	// one. Nothing was dispatched.
	ErrDestination = errors.New("gitpublish: push destination not admitted")
	// ErrContent: the commit is not in the server repository.
	ErrContent = errors.New("gitpublish: commit not in the server repository")
)

// managedKeys is the complete allowlist of the server repository's local
// config. Any other key (url.*, credential.*, core.hooksPath, include.*,
// http.*, remote.*, …) refuses the push before git runs it.
var managedKeys = map[string]bool{
	"core.repositoryformatversion": true,
	"core.filemode":                true,
	"core.bare":                    true,
	"core.ignorecase":              true,
	"core.precomposeunicode":       true,
	"core.logallrefupdates":        true,
	"gc.auto":                      true,
	"maintenance.auto":             true,
	"extensions.objectformat":      true,
}

var (
	shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	refRe = regexp.MustCompile(`^refs/heads/[A-Za-z0-9._/-]+$`)
)

// Executor runs git for one push in a closed environment: a pinned absolute
// git, an environment built from nothing, no system or global config, a
// managed local config, no hooks, no credential helper, no askpass, no
// redirects and only the admitted protocol.
type Executor struct {
	git  string
	home string
}

// NewExecutor pins the git executable and an empty engine-owned home.
func NewExecutor(gitPath, emptyHome string) (*Executor, error) {
	if !filepath.IsAbs(gitPath) || !filepath.IsAbs(emptyHome) {
		return nil, errors.New("gitpublish: git and home must be absolute paths")
	}
	if st, err := os.Stat(gitPath); err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		return nil, errors.New("gitpublish: git is not an executable file")
	}
	if st, err := os.Stat(emptyHome); err != nil || !st.IsDir() {
		return nil, errors.New("gitpublish: home is not a directory")
	}
	return &Executor{git: gitPath, home: emptyHome}, nil
}

// PushRequest is one admitted push. Every field comes from the module's
// admitted intent and bindings, never from a request.
type PushRequest struct {
	RepoPath    string // the server repository binding (bare, managed)
	URL         string // the admitted push URL
	Scheme      string // the admitted scheme ("https"; tests use "file")
	Header      Secret // the git authorization header line, or zero
	Ref         string // refs/heads/...
	ExpectedOld string // "" = the ref must not exist
	Commit      string
}

// closedEnv is the complete child environment. Nothing is inherited. When
// repo is set, git is pinned to it (GIT_DIR, no discovery above it) and
// replace refs are disabled, so every read sees the object that is pushed.
func (x *Executor) closedEnv(header Secret, repo string) []string {
	env := []string{
		"PATH=" + filepath.Dir(x.git),
		"HOME=" + x.home,
		"XDG_CONFIG_HOME=" + x.home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PROTOCOL_FROM_USER=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"LC_ALL=C",
	}
	if repo != "" {
		env = append(env, "GIT_DIR="+repo, "GIT_CEILING_DIRECTORIES="+filepath.Dir(repo))
	}
	if !header.IsZero() {
		env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0="+header.Reveal())
	}
	return env
}

// guard is the command-scope policy passed on every invocation.
func guard(scheme string) []string {
	g := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "credential.helper=",
		"-c", "core.askPass=",
		"-c", "http.followRedirects=false",
		"-c", "protocol.allow=never",
	}
	if scheme != "" {
		g = append(g, "-c", "protocol."+scheme+".allow=always")
	}
	return g
}

// run runs git pinned to the managed repository repo.
func (x *Executor) run(ctx context.Context, repo string, header Secret, scheme string, args ...string) (string, int, error) {
	return x.exec(ctx, repo, repo, header, scheme, args...)
}

func (x *Executor) exec(ctx context.Context, dir, repo string, header Secret, scheme string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, x.git, append(guard(scheme), args...)...)
	cmd.Dir = dir
	cmd.Env = x.closedEnv(header, repo)
	cmd.WaitDelay = 5 * time.Second
	// Only stdout is read (porcelain status, config names, object ids). stderr
	// is discarded: it is never parsed and never returned.
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	err := cmd.Run()
	if cerr := ctx.Err(); cerr != nil {
		// Killed by the deadline or cancellation: whatever git printed is
		// not a status.
		return "", 0, cerr
	}
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code, err = ee.ExitCode(), nil
	}
	return out.String(), code, err
}

// InitManaged creates a bare server repository with only the managed config.
func (x *Executor) InitManaged(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return ErrDestination
	}
	if _, code, err := x.exec(ctx, filepath.Dir(path), "", Secret{}, "", "init", "-q", "--bare", "--template=", path); err != nil || code != 0 {
		return fmt.Errorf("gitpublish: init server repository failed (exit %d)", code)
	}
	if _, code, err := x.run(ctx, path, Secret{}, "", "config", "gc.auto", "0"); err != nil || code != 0 {
		return fmt.Errorf("gitpublish: configure server repository failed (exit %d)", code)
	}
	return nil
}

// redirections are repository entries that make git read another
// repository, other objects or substituted history.
var redirections = []string{
	".git", "config.worktree", "commondir", "gitdir",
	"objects/info/alternates", "objects/info/http-alternates", "info/grafts",
}

// checkRepoState refuses a managed repository that is not a plain bare
// directory, or that holds any redirection: a .git file or directory,
// commondir/gitdir, alternates, grafts or replace refs (loose or packed).
// Reads (CommitTree, WorkflowsChanged) tolerate replace refs because
// GIT_NO_REPLACE_OBJECTS makes them read the real object; a push refuses
// them.
func checkRepoState(repo string) error {
	if err := checkObjectState(repo); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(repo, "refs", "replace")); err == nil {
		return ErrRepositoryConfig
	}
	if b, err := os.ReadFile(filepath.Join(repo, "packed-refs")); err == nil && bytes.Contains(b, []byte("refs/replace/")) {
		return ErrRepositoryConfig
	}
	return nil
}

// checkObjectState refuses what would make object reads leave the repository.
func checkObjectState(repo string) error {
	if !filepath.IsAbs(repo) {
		return ErrRepositoryConfig
	}
	for _, p := range []string{"", "config", "objects", "refs", "HEAD"} {
		st, err := os.Lstat(filepath.Join(repo, p))
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return ErrRepositoryConfig
		}
	}
	for _, r := range redirections {
		if _, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(r))); err == nil {
			return ErrRepositoryConfig
		}
	}
	return nil
}

// CheckManagedConfig refuses a server repository whose state redirects git
// (checkRepoState), whose config is not a regular file, or whose config holds
// any key outside the managed allowlist.
func (x *Executor) CheckManagedConfig(ctx context.Context, repo string) error {
	if err := checkRepoState(repo); err != nil {
		return err
	}
	cfg := filepath.Join(repo, "config")
	st, err := os.Lstat(cfg)
	if err != nil || !st.Mode().IsRegular() {
		return ErrRepositoryConfig
	}
	out, code, err := x.run(ctx, repo, Secret{}, "", "config", "--file", cfg, "--no-includes", "--list", "--name-only")
	if err != nil || code != 0 {
		return ErrRepositoryConfig
	}
	for _, k := range strings.Fields(out) {
		if !managedKeys[strings.ToLower(k)] {
			return ErrRepositoryConfig
		}
	}
	return nil
}

// CommitTree returns the tree of commit in repo, or ErrContent.
func (x *Executor) CommitTree(ctx context.Context, repo, commit string) (string, error) {
	if !shaRe.MatchString(commit) {
		return "", ErrContent
	}
	if err := checkObjectState(repo); err != nil {
		return "", err
	}
	out, code, err := x.run(ctx, repo, Secret{}, "", "rev-parse", "--verify", "--quiet", commit+"^{tree}")
	if err != nil || code != 0 {
		return "", ErrContent
	}
	return strings.TrimSpace(out), nil
}

// WorkflowsChanged reports whether .github/workflows differs between from and
// to. Both commits must be in the server repository.
func (x *Executor) WorkflowsChanged(ctx context.Context, repo, from, to string) (bool, error) {
	return x.PathsChanged(ctx, repo, from, to, []string{".github/workflows"})
}

// PathsChanged reports whether any of paths (a file or a directory) differs
// between from and to: CI configuration such as .github/workflows on GitHub
// or .gitlab-ci.yml on GitLab. Both commits must be in the server repository.
func (x *Executor) PathsChanged(ctx context.Context, repo, from, to string, paths []string) (bool, error) {
	for _, p := range paths {
		a, err := x.object(ctx, repo, from, p)
		if err != nil {
			return false, err
		}
		b, err := x.object(ctx, repo, to, p)
		if err != nil {
			return false, err
		}
		if a != b {
			return true, nil
		}
	}
	return false, nil
}

// object returns the object id of path in commit, or "" when it is absent.
func (x *Executor) object(ctx context.Context, repo, commit, path string) (string, error) {
	if _, err := x.CommitTree(ctx, repo, commit); err != nil {
		return "", err
	}
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return "", ErrContent
	}
	out, code, err := x.run(ctx, repo, Secret{}, "", "rev-parse", "--verify", "--quiet", commit+":"+path)
	if err != nil {
		return "", ErrContent
	}
	if code != 0 {
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// Push checks the managed config, the admitted destination and the content,
// then runs one leased push. A returned error means NOTHING was dispatched;
// otherwise the Result classifies what the host said.
func (x *Executor) Push(ctx context.Context, r PushRequest) (Result, error) {
	u, err := url.Parse(r.URL)
	if err != nil || r.Scheme == "" || u.Scheme != r.Scheme || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Result{}, ErrDestination
	}
	if !refRe.MatchString(r.Ref) || strings.Contains(r.Ref, "..") || (r.ExpectedOld != "" && !shaRe.MatchString(r.ExpectedOld)) {
		return Result{}, ErrDestination
	}
	if err := x.CheckManagedConfig(ctx, r.RepoPath); err != nil {
		return Result{}, err
	}
	// The destination as git will resolve it AFTER loading every config.
	got, code, err := x.run(ctx, r.RepoPath, Secret{}, r.Scheme, "ls-remote", "--get-url", r.URL)
	if err != nil || code != 0 || strings.TrimSpace(got) != r.URL {
		return Result{}, ErrDestination
	}
	if _, err := x.CommitTree(ctx, r.RepoPath, r.Commit); err != nil {
		return Result{}, err
	}
	out, _, err := x.run(ctx, r.RepoPath, r.Header, r.Scheme,
		"push", "--porcelain", "--atomic", "--no-verify",
		"--force-with-lease="+r.Ref+":"+r.ExpectedOld,
		r.URL, r.Commit+":"+r.Ref)
	if err != nil {
		// Killed, timed out or could not wait: the request may have been sent.
		return Result{Class: Ambiguous, Reason: "transport"}, nil
	}
	return classifyPorcelain(out, r.Ref), nil
}

// classifyPorcelain reads the status line of ref from `git push --porcelain`.
// `!` with [rejected] or [remote rejected] is a definite refusal; [remote
// failure] or no status line is ambiguous.
func classifyPorcelain(out, ref string) Result {
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || len(parts[0]) != 1 || !strings.HasSuffix(parts[1], ":"+ref) {
			continue
		}
		flag, summary := parts[0], parts[2]
		switch flag {
		case " ", "+", "*":
			return Result{Class: Applied}
		case "=":
			return Result{Class: Applied, Reason: "up_to_date"}
		case "!":
			switch {
			case strings.HasPrefix(summary, "[rejected]") && strings.Contains(summary, "stale info"):
				return Result{Class: Rejected, Reason: "stale_lease"}
			case strings.HasPrefix(summary, "[rejected]"):
				return Result{Class: Rejected, Reason: "rejected"}
			case strings.HasPrefix(summary, "[remote rejected]") && strings.Contains(summary, "workflow"):
				return Result{Class: Rejected, Reason: "workflow_permission_required"}
			case strings.HasPrefix(summary, "[remote rejected]"):
				return Result{Class: Rejected, Reason: "remote_rejected"}
			case strings.HasPrefix(summary, "[remote failure]"):
				return Result{Class: Ambiguous, Reason: "remote_failure"}
			}
			return Result{Class: Ambiguous, Reason: "unrecognized_status"}
		}
	}
	return Result{Class: Ambiguous, Reason: "no_status"}
}
