// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// A session that needs to read a private GitHub
// repository gets a contents:read installation token through a git credential
// helper, never a PAT in its environment; the token is revoked when the session
// stops, the ledger records the mint, and a GitLab session gets no credential.

const (
	gitReadCanary  = "ghs_LEAKCANARY_session_read_0123456789"
	gitReadBinding = "gh:acme/widgets"
)

var gitReadExpiry = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// fakeGitRead mints numbered tokens and records what it minted and revoked.
type fakeGitRead struct {
	mu       sync.Mutex
	url      string
	err      error
	mints    []string // workspace|binding
	live     map[string]bool
	released chan string
}

func newFakeGitRead(url string) *fakeGitRead {
	return &fakeGitRead{url: url, live: map[string]bool{}, released: make(chan string, 8)}
}

func (f *fakeGitRead) MintSessionGitRead(_ context.Context, _ model.TenantID, workspace model.ID, binding string) (GitReadCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mints = append(f.mints, workspace.String()+"|"+binding)
	if f.err != nil {
		return GitReadCredential{}, f.err
	}
	tok := fmt.Sprintf("%s-%d", gitReadCanary, len(f.mints))
	f.live[tok] = true
	return GitReadCredential{
		RepoURL: f.url, Token: tok, ExpiresAt: gitReadExpiry,
		Release: func(context.Context) error {
			f.mu.Lock()
			delete(f.live, tok)
			f.mu.Unlock()
			f.released <- tok
			return nil
		},
	}, nil
}

func (f *fakeGitRead) valid(tok string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live[tok]
}

func (f *fakeGitRead) minted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.mints...)
}

func (f *fakeGitRead) awaitRelease(t *testing.T) string {
	t.Helper()
	select {
	case tok := <-f.released:
		return tok
	case <-time.After(10 * time.Second):
		t.Fatal("the read credential was not revoked when the session ended")
		return ""
	}
}

func gitReadHarness(t *testing.T, url string) (*Module, model.TenantID, *fakeRunner, *fakeGitRead, *spyGate, string) {
	t.Helper()
	fr := &fakeRunner{}
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()), WithLaunchGate(gate))
	stopModuleAtCleanup(t, m)
	src := newFakeGitRead(url)
	dataDir := t.TempDir()
	m.GitRead, m.GitReadDataDir = src, dataDir
	return m, tenant, fr, src, gate, dataDir
}

func gitReadParams(mayUse bool) CreateRunParams {
	return CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: actorU, ActorKind: actorKindU, GitRead: gitReadBinding, MayUseSecretEnv: mayUse,
	}
}

// childEnv is the launch environment as the child gets it, on a clean host
// base: what `env` prints inside the session.
func childEnv(spec LaunchSpec, home string, extra ...string) []string {
	env := append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}, extra...)
	for _, e := range spec.Env {
		env = append(env, e.Name+"="+e.Value)
	}
	return env
}

func requireGit(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed on this host")
	}
	return git
}

func runGit(t *testing.T, env []string, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func credentialFill(t *testing.T, env []string, url string) string {
	t.Helper()
	cmd := exec.Command("git", "credential", "fill")
	cmd.Env, cmd.Stdin = env, strings.NewReader("url="+url+"\n\n")
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func TestGitRead_TheTokenReachesGitThroughTheHelperAndNeverTheEnvironment(t *testing.T) {
	t.Parallel()
	requireGit(t)
	ctx := context.Background()
	m, tenant, fr, src, gate, dataDir := gitReadHarness(t, "https://github.com/acme/widgets.git")

	dto, err := createProfiledTestRun(t, m, ctx, tenant, gitReadParams(true))
	if err != nil {
		t.Fatalf("create with git_read: %v", err)
	}
	tok := gitReadCanary + "-1"
	spec := fr.lastSpec()
	home := t.TempDir()
	env := childEnv(spec, home)

	// `env` in the session shows no token; neither do argv, the program or the folder.
	cmd := exec.Command("env")
	cmd.Env = env
	printed, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(printed), gitReadCanary) {
		t.Fatalf("the token is in the session environment:\n%s", printed)
	}
	for _, where := range append([]string{spec.Program, spec.Dir}, spec.Args...) {
		if strings.Contains(where, gitReadCanary) {
			t.Fatalf("the token reached the program, folder or argv: %q", where)
		}
	}
	// git gets it from the helper, for that repository only, in both clone forms.
	for _, url := range []string{"https://github.com/acme/widgets", "https://github.com/acme/widgets.git"} {
		if out := credentialFill(t, env, url); !strings.Contains(out, "username=x-access-token\npassword="+tok+"\n") {
			t.Fatalf("git credential fill %s =\n%s\nwant the session's read token", url, out)
		}
	}
	for _, url := range []string{"https://github.com/acme/gears.git", "https://github.com/acme/widgetsx", "https://gitlab.com/acme/widgets.git"} {
		if out := credentialFill(t, env, url); strings.Contains(out, gitReadCanary) {
			t.Fatalf("the helper answered for another repository %s:\n%s", url, out)
		}
	}
	// The file is the engine's, readable only by its owner, and granted read-only.
	files, _ := filepath.Glob(filepath.Join(dataDir, "run", dto.RunRef, "git-credential-*"))
	if len(files) != 1 {
		t.Fatalf("credential files = %v, want one", files)
	}
	if st, err := os.Stat(files[0]); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode = %v %v, want 0600", st, err)
	}
	if spec.Confinement != nil && !strings.Contains(strings.Join(spec.Confinement.ReadOnly, "\n"), files[0]) {
		t.Fatalf("the confined child is not granted the credential file: %v", spec.Confinement.ReadOnly)
	}

	// Minted for the run's own authorization workspace; the gate saw the binding.
	if got := src.minted(); len(got) != 1 || got[0] != dto.AuthzWorkspaceID+"|"+gitReadBinding || dto.AuthzWorkspaceID == "" {
		t.Fatalf("mints = %v, want one for workspace %q", got, dto.AuthzWorkspaceID)
	}
	if got := gate.last(t).GitRead; got != gitReadBinding {
		t.Fatalf("launch intent git_read = %q", got)
	}
	// The API, the row and the ledger name the binding, never the token.
	raw, _ := json.Marshal(dto)
	if strings.Contains(string(raw), gitReadCanary) || !strings.Contains(string(raw), `"git_read":"`+gitReadBinding+`"`) {
		t.Fatalf("run DTO = %s, want the binding and never the token", raw)
	}
	rec, err := m.loadRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	if row, _ := json.Marshal(rec); strings.Contains(string(row), gitReadCanary) || rec.String(colRunGitRead) != gitReadBinding {
		t.Fatalf("run row = %s, want the binding and never the token", row)
	}
	details := strings.Join(runEventDetails(t, m, tenant, dto.RunRef), "\n")
	if strings.Contains(details, gitReadCanary) {
		t.Fatalf("the ledger carries the token: %s", details)
	}
	if !strings.Contains(details, "git_read "+gitReadBinding+" contents:read until 2026-10-06T12:00:00Z") {
		t.Fatalf("the ledger does not record the mint:\n%s", details)
	}

	// Stop revokes the token and removes the file; git gets nothing afterwards.
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	if got := src.awaitRelease(t); got != tok {
		t.Fatalf("revoked %q, want %q", got, tok)
	}
	waitFor(t, "the credential file removed", func() bool { _, err := os.Stat(files[0]); return os.IsNotExist(err) })
	if out := credentialFill(t, env, "https://github.com/acme/widgets"); strings.Contains(out, gitReadCanary) {
		t.Fatalf("the helper still answers after stop:\n%s", out)
	}
}

// The exit criterion end to end with real git: a private repository served over
// HTTPS (git http-backend behind Basic authentication) is cloned and fetched by a
// child whose environment holds no token, and refused once the session stops.
func TestGitRead_APrivateRepositoryClonesAndFetchesWithNoTokenInTheEnvironment(t *testing.T) {
	t.Parallel()
	git := requireGit(t)
	ctx := context.Background()
	root, work, home := t.TempDir(), t.TempDir(), t.TempDir()
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid"}
	bare := filepath.Join(root, "acme", "widgets.git")
	for _, args := range [][]string{
		{"init", "-q", "--bare", "-b", "main", bare},
		{"init", "-q", "-b", "main", work},
	} {
		if out, err := runGit(t, base, root, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit := func(content string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, "README"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "README"}, {"commit", "-q", "-m", content}, {"push", "-q", bare, "main"}} {
			if out, err := runGit(t, base, work, args...); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		head, _ := runGit(t, base, work, "rev-parse", "HEAD")
		return strings.TrimSpace(head)
	}
	commit("first")

	var src *fakeGitRead
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "HOME=" + home}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "x-access-token" || !src.valid(pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="private"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	m, tenant, fr, fake, _, _ := gitReadHarness(t, srv.URL+"/acme/widgets.git")
	src = fake

	dto, err := createProfiledTestRun(t, m, ctx, tenant, gitReadParams(true))
	if err != nil {
		t.Fatalf("create with git_read: %v", err)
	}
	env := childEnv(fr.lastSpec(), home, "GIT_SSL_NO_VERIFY=true")
	for _, kv := range env {
		if strings.Contains(kv, gitReadCanary) {
			t.Fatalf("the session environment holds the token: %s", kv)
		}
	}
	// Without the helper the repository is private: the clone is refused.
	if out, err := runGit(t, append(base, "GIT_SSL_NO_VERIFY=true"), root, "clone", "-q", srv.URL+"/acme/widgets", filepath.Join(root, "anonymous")); err == nil {
		t.Fatalf("an anonymous clone of the private repository succeeded:\n%s", out)
	}
	clone := filepath.Join(root, "clone")
	if out, err := runGit(t, env, root, "clone", "-q", srv.URL+"/acme/widgets", clone); err != nil {
		t.Fatalf("session clone: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(clone, "README")); string(got) != "first" {
		t.Fatalf("cloned README = %q", got)
	}
	second := commit("second")
	if out, err := runGit(t, env, clone, "fetch", "-q", "origin"); err != nil {
		t.Fatalf("session fetch: %v\n%s", err, out)
	}
	if got, _ := runGit(t, env, clone, "rev-parse", "origin/main"); strings.TrimSpace(got) != second {
		t.Fatalf("fetched origin/main = %q, want %s", got, second)
	}

	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	src.awaitRelease(t)
	commit("third")
	if out, err := runGit(t, env, clone, "fetch", "-q", "origin"); err == nil {
		t.Fatalf("a fetch after the session stopped succeeded:\n%s", out)
	}
}

func TestGitRead_ResumeMintsAFreshTokenAndAsksAgain(t *testing.T) {
	t.Parallel()
	requireGit(t)
	ctx := context.Background()
	m, tenant, fr, src, _, _ := gitReadHarness(t, "https://github.com/acme/widgets.git")
	dto, err := createProfiledTestRun(t, m, ctx, tenant, gitReadParams(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	src.awaitRelease(t)

	if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", false); statusOf(err) != http.StatusForbidden {
		t.Fatalf("resume by someone who may not give it = %v, want 403", err)
	}
	if got := src.minted(); len(got) != 1 {
		t.Fatalf("a refused resume minted: %v", got)
	}
	if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", true); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := src.minted(); len(got) != 2 || !strings.HasSuffix(got[1], "|"+gitReadBinding) {
		t.Fatalf("mints = %v, want a fresh one for the stored binding", got)
	}
	env := childEnv(fr.lastSpec(), t.TempDir())
	if out := credentialFill(t, env, "https://github.com/acme/widgets"); !strings.Contains(out, "password="+gitReadCanary+"-2\n") {
		t.Fatalf("resumed helper =\n%s\nwant the fresh token", out)
	}
	if details := strings.Join(runEventDetails(t, m, tenant, dto.RunRef), "\n"); !strings.Contains(details, "resumed: ") ||
		strings.Count(details, "git_read "+gitReadBinding) != 2 {
		t.Fatalf("the ledger does not record both mints:\n%s", details)
	}
}

func TestGitRead_RefusalsLeaveNoCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := map[string]struct {
		params   func(CreateRunParams) CreateRunParams
		err      error
		unwire   bool
		confined bool
		status   int
		minted   bool
	}{
		"not a tenant administrator": {params: func(p CreateRunParams) CreateRunParams { p.MayUseSecretEnv = false; return p }, status: http.StatusForbidden},
		"malformed binding":          {params: func(p CreateRunParams) CreateRunParams { p.GitRead = "acme widgets"; return p }, status: http.StatusBadRequest},
		"control character":          {params: func(p CreateRunParams) CreateRunParams { p.GitRead = "gh:acme/widgets\x1b[2J"; return p }, status: http.StatusBadRequest},
		"git config from env_allow": {params: func(p CreateRunParams) CreateRunParams {
			p.EnvAllow = []string{"GIT_CONFIG_COUNT"}
			return p
		}, status: http.StatusBadRequest},
		"no custody on this node": {unwire: true, status: http.StatusServiceUnavailable},
		"a GitLab binding":        {err: ErrGitReadUnsupported, status: http.StatusUnprocessableEntity, minted: true},
		"not approved":            {err: ErrGitReadNotApproved, status: http.StatusUnprocessableEntity, minted: true},
		// Claude Code on a Providers record reaches only that record's endpoint.
		"an egress-confined launch": {confined: true, status: http.StatusUnprocessableEntity},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, tenant, fr, src, _, dataDir := gitReadHarness(t, "https://github.com/acme/widgets.git")
			src.err = tc.err
			if tc.unwire {
				m.GitRead, m.GitReadDataDir = nil, ""
			}
			if tc.confined {
				m.rt.Creds = CredentialSourceFunc(func(context.Context, CredentialRequest) (Credential, error) {
					return Credential{ID: "cred-1", Token: "tok-secret", Scheme: "mock", NotAfter: farFuture,
						bound: BoundProvider{Kind: ProviderKindAnthropic, Endpoint: "https://api.anthropic.com"}}, nil
				})
			}
			p := gitReadParams(true)
			if tc.params != nil {
				p = tc.params(p)
			}
			_, err := createProfiledTestRun(t, m, ctx, tenant, p)
			if statusOf(err) != tc.status {
				t.Fatalf("create = %v (status %d), want %d", err, statusOf(err), tc.status)
			}
			if got := len(src.minted()) > 0; got != tc.minted {
				t.Fatalf("custody asked = %v, want %v", got, tc.minted)
			}
			fr.mu.Lock()
			specs := append([]LaunchSpec(nil), fr.specs...)
			fr.mu.Unlock()
			for _, spec := range specs {
				if _, ok := specEnvValue(spec, "GIT_CONFIG_COUNT"); ok {
					t.Fatal("a refused launch reached the runner with a credential helper")
				}
			}
			if files, _ := filepath.Glob(filepath.Join(dataDir, "run", "*", "git-credential-*")); len(files) != 0 {
				t.Fatalf("a refused launch left credential files %v", files)
			}
		})
	}
}

func TestGitRead_OutputNeverCarriesTheToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, _, _, _ := gitReadHarness(t, "https://github.com/acme/widgets.git")
	dto, err := createProfiledTestRun(t, m, ctx, tenant, gitReadParams(true))
	if err != nil {
		t.Fatal(err)
	}
	// The token itself, and the Basic authorization git's curl trace prints.
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + gitReadCanary + "-1"))
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"password=` + gitReadCanary + `-1"}]}}`)}
	fr.lastProc().out <- OutputFrame{Stream: streamStderr, Data: []byte("=> Send header: Authorization: Basic " + basic)}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("expected a live run")
	}
	waitFor(t, "both frames in the ring", func() bool { return len(lr.ring.readFrom(0).frames) >= 2 })
	var all strings.Builder
	for _, f := range lr.ring.readFrom(0).frames {
		all.Write(f.Data)
		all.WriteByte('\n')
	}
	out := all.String()
	if strings.Contains(out, gitReadCanary) || strings.Contains(out, basic) || strings.Count(out, "[secret git_read]") != 2 {
		t.Fatalf("session output = %s, want the token and its Basic form withheld as [secret git_read]", out)
	}
}

// Over the API, through the approval queue: the body's git_read is stored on the
// waiting row, nothing is minted while it waits, and the approved launch gets the
// credential for that binding; the run reads back the binding.
func TestGitRead_AQueuedLaunchOverHTTPGetsItsCredentialAfterApproval(t *testing.T) {
	requireGit(t)
	gate := &controlledLaunchApproval{}
	runner := &fakeRunner{initSID: "queued-git-read"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate))
	src := newFakeGitRead("https://github.com/acme/widgets.git")
	m.GitRead, m.GitReadDataDir = src, t.TempDir()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "queued-git-read")
	if r := h.doJSON("POST", "/v1/users", admin, map[string]any{"email": "git-read-admin@fh.invalid", "password": "synthetic-password1", "tenant": tenant.String(), "role": auth.RoleAdmin}, nil); r.code != http.StatusCreated {
		t.Fatalf("tenant admin = %d %s", r.code, r.raw)
	}
	login := h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": "git-read-admin@fh.invalid", "password": "synthetic-password1"}, nil)
	token, _ := login.body["token"].(string)
	issuer := auth.NewAuthenticator(h.st, nil)
	m.QueuedCredentialCapture = issuer.BindQueuedCredential
	m.QueuedLaunchAuthorization = func(ctx context.Context, tenant model.TenantID, credential auth.QueuedCredential, runID string, workspace model.ID) (auth.Principal, error) {
		return issuer.RevalidateQueuedCredential(ctx, credential)
	}
	queued := h.doJSON("POST", "/v1/m/sessions/runs", token, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, h.m, tenant), "name": "queued git read", "git_read": gitReadBinding}, tenantHdr(tenant))
	if queued.code != http.StatusAccepted || launchCount(runner) != 0 || len(src.minted()) != 0 {
		t.Fatalf("queued = %d %s, mints %v", queued.code, queued.raw, src.minted())
	}
	gate.approved.Store(true)
	waitFor(t, "the approved launch", func() bool { return launchCount(runner) > 0 })
	if got := src.minted(); len(got) != 1 || !strings.HasSuffix(got[0], "|"+gitReadBinding) {
		t.Fatalf("mints = %v, want one for the queued binding", got)
	}
	if out := credentialFill(t, childEnv(runner.lastSpec(), t.TempDir()), "https://github.com/acme/widgets"); !strings.Contains(out, "password="+gitReadCanary+"-1\n") {
		t.Fatalf("approved launch helper =\n%s\nwant the read token", out)
	}
	ref, _ := queued.body["run_ref"].(string)
	got := h.doJSON("GET", "/v1/m/sessions/runs/"+ref, token, nil, tenantHdr(tenant))
	if got.code != http.StatusOK || got.body["git_read"] != gitReadBinding || strings.Contains(string(got.raw), gitReadCanary) {
		t.Fatalf("run read = %d %s, want the binding and never the token", got.code, got.raw)
	}
}

// A launch that fails after the mint gives the token back and leaves no file.
func TestGitRead_ALaunchThatFailsAfterTheMintRevokesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, src, _, dataDir := gitReadHarness(t, "https://github.com/acme/widgets.git")
	fr.launchErr = fmt.Errorf("synthetic spawn failure")
	if _, err := createProfiledTestRun(t, m, ctx, tenant, gitReadParams(true)); err == nil {
		t.Fatal("the launch did not fail")
	}
	if got := src.awaitRelease(t); got != gitReadCanary+"-1" {
		t.Fatalf("revoked %q", got)
	}
	waitFor(t, "no credential file", func() bool {
		files, _ := filepath.Glob(filepath.Join(dataDir, "run", "*", "git-credential-*"))
		return len(files) == 0
	})
}

// A record-bound Codex session reaches only its provider, so git_read, which needs
// its Git host, is refused (422) before any credential is minted.
func TestGitReadRefusedForANetworkConfinedCodexSession(t *testing.T) {
	m, tenant, _, src, _, _ := gitReadHarness(t, "https://github.com/acme/widgets.git")
	p := gitReadParams(true)
	p.WorkspaceDir = t.TempDir()
	p.ProviderHome = &ProviderHomeSnapshot{Driver: providerDriverCodex, AuthSource: AuthSourceManagedInjection}
	spec := m.childSpec(p, childDecision{cred: Credential{bound: BoundProvider{Kind: ProviderKindOpenAI, Endpoint: "https://api.openai.com/v1"}}})
	err := m.configureGitRead(t.Context(), t.Context(), tenant, "run-one", &p, &spec)
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity {
		t.Fatalf("a confined Codex session with git_read: %v, want 422", err)
	}
	if minted := src.minted(); len(minted) != 0 {
		t.Fatalf("a credential was minted for a session that cannot use it: %v", minted)
	}
}
