// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/sdk/gitlayout"
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
	// ErrSource: the session folder is not a local repository the server
	// repository may fetch from. Nothing was fetched.
	ErrSource = errors.New("gitpublish: session folder is not an admitted local repository")
	// ErrTransportStart: the transport could not even start (for ssh, no
	// client or no key file). Nothing was dispatched; this is a refusal, not
	// an ambiguous outcome.
	ErrTransportStart = errors.New("gitpublish: the transport did not start")
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
	// A run killed between writing and removing an ssh key file leaves it
	// behind; no invocation of this executor may outlive it.
	entries, _ := os.ReadDir(emptyHome)
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "key-") {
			_ = os.Remove(filepath.Join(emptyHome, e.Name()))
		}
	}
	return &Executor{git: gitPath, home: emptyHome}, nil
}

// PushRequest is one admitted push. Every field comes from the module's
// admitted intent and bindings, never from a request.
type PushRequest struct {
	RepoPath    string // the server repository binding (bare, managed)
	URL         string // the admitted push URL
	Scheme      string // the admitted scheme ("https" or "ssh"; tests use "file")
	Header      Secret // https: the git authorization header line, or zero
	Key         Secret // ssh: the private key, or zero
	Ref         string // refs/heads/...
	ExpectedOld string // "" = the ref must not exist
	Commit      string
}

// RemoteRef is one admitted remote read (ls-remote) with its transport
// credential: an HTTPS authorization header line, or the SSH private key.
type RemoteRef struct {
	URL    string
	Scheme string // "https" or "ssh" (tests use "file")
	Header Secret
	Key    Secret
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

// sshPath resolves the ssh client once. A host without ssh leaves ssh remotes
// unusable (reported as a refused destination, never a broken engine).
var sshPath = sync.OnceValues(func() (string, error) {
	p, err := exec.LookPath("ssh")
	if err != nil {
		return "", err
	}
	return filepath.Abs(p)
})

// sshEnv materializes one SSH private key for ONE invocation: a 0600 file
// under the engine-owned home, removed when the invocation ends, plus the
// pinned ssh command line that uses it — batch mode, the public key only (no
// agent, no password), and host keys recorded on first use and refused on
// change afterwards (OpenSSH accept-new).
func (x *Executor) sshEnv(key Secret) ([]string, func(), error) {
	ssh, err := sshPath()
	if err != nil {
		return nil, nil, ErrDestination
	}
	f, err := os.CreateTemp(x.home, "key-")
	if err != nil {
		return nil, nil, err
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name) }
	// `secrets set --value-file` trims the trailing newline and OpenSSH rejects
	// a private key without one, so write the key in its one accepted form.
	if _, err := f.WriteString(strings.TrimRight(key.Reveal(), "\r\n") + "\n"); err != nil {
		f.Close()
		cleanup()
		return nil, nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return nil, nil, err
	}
	known := filepath.Join(x.home, "known_hosts")
	// git runs this line through sh -c, and ssh then reads each -o value
	// itself: single-quote for sh, double-quote and %-escape for ssh. (-i is
	// not used: ssh checks its raw name, then expands it, so a % cannot pass;
	// a ${VAR} in the home still fails closed as an unreadable remote.)
	line := fmt.Sprintf("%s -F /dev/null -o %s -o IdentitiesOnly=yes -o BatchMode=yes -o PreferredAuthentications=publickey -o PasswordAuthentication=no -o StrictHostKeyChecking=accept-new -o %s", shellQuote(ssh), shellQuote(sshPathOption("IdentityFile", name)), shellQuote(sshPathOption("UserKnownHostsFile", known)))
	return []string{"GIT_SSH_COMMAND=" + line}, cleanup, nil
}

// shellQuote single-quotes s for sh: nothing inside is expanded.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sshPathOption is one `-o` path option for ssh's own parser: the value is
// double-quoted, with backslash and double quote escaped, and a percent sign
// doubled so ssh does not read it as a token.
func sshPathOption(keyword, p string) string {
	return keyword + `="` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(p) + `"`
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
	switch scheme {
	case "https":
		g = append(g, "-c", "protocol.https.allow=always")
	case "ssh":
		g = append(g, "-c", "protocol.ssh.allow=always")
	case "file":
		g = append(g, "-c", "protocol.file.allow=always")
	}
	return g
}

// run runs git pinned to the managed repository repo.
func (x *Executor) run(ctx context.Context, repo string, header Secret, scheme string, args ...string) (string, int, error) {
	return x.exec(ctx, repo, repo, header, Secret{}, scheme, args...)
}

func (x *Executor) exec(ctx context.Context, dir, repo string, header, key Secret, scheme string, args ...string) (string, int, error) {
	// Private callers supply literal verbs/flags and validated operands: absolute
	// paths, SHA-prefixed revisions, branch refs and SSH/HTTPS/file URLs. guard keeps
	// all other transports disabled; in particular, ext cannot run commands.
	// #nosec G204 -- NewExecutor pins an absolute executable; private callers validate operands; no shell.
	cmd := exec.CommandContext(ctx, x.git, append(guard(scheme), args...)...)
	cmd.Dir = dir
	cmd.Env = x.closedEnv(header, repo)
	if !key.IsZero() {
		if scheme != "ssh" {
			return "", 0, ErrDestination
		}
		env, cleanup, err := x.sshEnv(key)
		if err != nil {
			// Nothing was started: a refusal, never an ambiguous dispatch.
			return "", 0, ErrTransportStart
		}
		cmd.Env = append(cmd.Env, env...)
		defer cleanup()
	}
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

// admittedRemote checks one remote URL against its scheme's admitted shape:
// https keeps today's rules (no userinfo, a host), file stays a test
// transport (no userinfo, an absolute path), and ssh names its user
// explicitly, takes no password in the URL and allows any port. Every scheme
// refuses a query, a fragment and dot segments.
func admittedRemote(scheme, raw string) error {
	if scheme != "https" && scheme != "ssh" && scheme != "file" {
		return ErrDestination
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != scheme || !strings.HasPrefix(raw, scheme+"://") || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return ErrDestination
	}
	switch scheme {
	case "ssh":
		var password string
		if u.User != nil {
			password, _ = u.User.Password()
		}
		if u.User == nil || !admittedSSHUser(u.User.Username()) || password != "" || !strings.HasPrefix(u.Path, "/") {
			return ErrDestination
		}
		// A name or an IP literal, never an ssh option: this boundary does
		// not rely on the adapter having parsed the remote first.
		if host := strings.ToLower(u.Hostname()); net.ParseIP(host) == nil && !isDNSName(host) {
			return ErrDestination
		}
		if p := u.Port(); p != "" {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				return ErrDestination
			}
		}
	case "https":
		if u.User != nil || u.Hostname() == "" {
			return ErrDestination
		}
	case "file":
		if u.User != nil || !strings.HasPrefix(u.Path, "/") {
			return ErrDestination
		}
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == ".." || seg == "." {
			return ErrDestination
		}
	}
	return nil
}

// sshUserRe is the charset a remote ssh user may carry; a leading dash is
// refused separately, so the destination can never be read as an ssh option.
var sshUserRe = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

// admittedSSHUser reports whether user is a plain ssh username.
func admittedSSHUser(user string) bool {
	return user != "" && !strings.HasPrefix(user, "-") && sshUserRe.MatchString(user)
}

// LsRemote runs `git ls-remote` against one admitted remote in the closed
// environment and returns its raw stdout ("<sha>\t<ref>" lines, with a
// "ref: <ref>\tHEAD" symref line first when symref is set). It only reads the
// remote; nothing is written. A failed read returns ErrHostUnavailable; an
// unadmitted destination returns ErrDestination before git runs.
func (x *Executor) LsRemote(ctx context.Context, r RemoteRef, symref bool, patterns ...string) (string, error) {
	if err := admittedRemote(r.Scheme, r.URL); err != nil {
		return "", err
	}
	key := r.Key
	if r.Scheme != "ssh" {
		key = Secret{}
	}
	if r.Scheme == "ssh" && key.IsZero() {
		return "", ErrDestination
	}
	args := []string{"ls-remote"}
	if symref {
		args = append(args, "--symref")
	}
	args = append(args, r.URL)
	args = append(args, patterns...)
	out, code, err := x.exec(ctx, x.home, "", r.Header, key, r.Scheme, args...)
	if err != nil || code != 0 {
		return "", ErrHostUnavailable
	}
	return out, nil
}

// InitManaged creates a bare server repository with only the managed config.
func (x *Executor) InitManaged(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return ErrDestination
	}
	if _, code, err := x.exec(ctx, filepath.Dir(path), "", Secret{}, Secret{}, "", "init", "-q", "--bare", "--template=", path); err != nil || code != 0 {
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

// Fetch feeds commit from a session folder into the server repository repo, so
// a commit made in a session can be pushed. source is the absolute, clean path
// of a worktree root: a repository or a linked worktree. A commit already in
// repo is not fetched again; the managed config is checked before and after.
//
// No git reads the session's config to serve the fetch: the session writes
// that config, and on a git that serves a repository's own settings (lazy
// fetch from a promisor remote, for one) it would run commands as the engine.
// Instead an engine-made bare repository with the managed config borrows the
// session's object directory as its only alternate, and the server repository
// fetches from it over the file protocol, writing objects and no ref or
// FETCH_HEAD. What the fetch takes from the session is objects, and object
// ids are content hashes: a session that redirects its object directory after
// the checks (see sessionObjects) reaches only objects whose ids it knows.
func (x *Executor) Fetch(ctx context.Context, repo, source, commit string) error {
	if !shaRe.MatchString(commit) {
		return ErrContent
	}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return ErrSource
	}
	if err := x.CheckManagedConfig(ctx, repo); err != nil {
		return err
	}
	if _, err := x.CommitTree(ctx, repo, commit); err == nil {
		return nil
	}
	objects, err := sessionObjects(ctx, source)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(repo), ".fetch-")
	if err != nil {
		return fmt.Errorf("gitpublish: stage the session objects: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := x.InitManaged(ctx, stage); err != nil {
		return err
	}
	info := filepath.Join(stage, "objects", "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		return fmt.Errorf("gitpublish: stage the session objects: %w", err)
	}
	if err := os.WriteFile(filepath.Join(info, "alternates"), []byte(objects+"\n"), 0o600); err != nil {
		return fmt.Errorf("gitpublish: stage the session objects: %w", err)
	}
	_, code, err := x.run(ctx, repo, Secret{}, "file",
		"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--no-auto-gc",
		stage, commit)
	if err != nil {
		return fmt.Errorf("gitpublish: fetch the session commit: %w", err)
	}
	if code != 0 {
		return ErrContent
	}
	return x.CheckManagedConfig(ctx, repo)
}

// sessionObjects returns the object directory of the session repository at
// source. The layout is git's own (gitlayout.Read: the repository itself, or a
// linked worktree whose entry sits in its common directory's worktrees/ and
// links back), so neither a rewritten .git file nor an entry forged inside the
// session folder can point the engine at another repository it can read. The
// object store must not redirect reads (checkObjectState) and holds no link at
// any depth.
func sessionObjects(ctx context.Context, source string) (string, error) {
	layout, ok := gitlayout.Read(source)
	if !ok || strings.ContainsAny(layout.CommonDir, "\r\n") || checkObjectState(layout.CommonDir) != nil {
		return "", ErrSource
	}
	objects := filepath.Join(layout.CommonDir, "objects")
	// git follows links below objects/ with the engine's rights, and a
	// confined session can link to files it cannot read itself: only plain
	// directories and regular files are admitted.
	err := filepath.WalkDir(objects, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return ErrSource
		}
		return nil
	})
	if errors.Is(err, ErrSource) {
		return "", ErrSource
	}
	if err != nil {
		return "", fmt.Errorf("gitpublish: read the session objects: %w", err)
	}
	return objects, nil
}

// Push checks the managed config, the admitted destination and the content,
// then runs one leased push. A returned error means NOTHING was dispatched;
// otherwise the Result classifies what the host said.
func (x *Executor) Push(ctx context.Context, r PushRequest) (Result, error) {
	if err := admittedRemote(r.Scheme, r.URL); err != nil {
		return Result{}, err
	}
	key := r.Key
	if r.Scheme != "ssh" {
		key = Secret{}
	}
	if r.Scheme == "ssh" && key.IsZero() {
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
	out, _, err := x.exec(ctx, r.RepoPath, r.RepoPath, r.Header, key, r.Scheme,
		"push", "--porcelain", "--atomic", "--no-verify",
		"--force-with-lease="+r.Ref+":"+r.ExpectedOld,
		r.URL, r.Commit+":"+r.Ref)
	if err != nil {
		if errors.Is(err, ErrTransportStart) {
			// The transport never started, so nothing was dispatched.
			return Result{}, err
		}
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
