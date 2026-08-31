// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitopsFakeRunner is a deterministic cmdRunner for git: it records every invocation
// (so a test can assert no secret ever reaches argv) and returns a configurable exit
// per subcommand. It NEVER touches a real git binary.
type gitopsFakeRunner struct {
	addExit    int
	commitExit int
	pushExit   int
	revertExit int
	calls      [][]string
	envs       [][]string
}

func (f *gitopsFakeRunner) run(_ context.Context, _ string, env []string, name string, args ...string) ([]byte, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	// `commit` may be preceded by -c k=v pairs; find the verb.
	verb := ""
	for _, a := range args {
		if strings.HasPrefix(a, "-") || strings.Contains(a, "=") {
			continue
		}
		verb = a
		break
	}
	switch verb {
	case "add":
		return nil, f.addExit, nil
	case "commit":
		return nil, f.commitExit, nil
	case "push":
		return nil, f.pushExit, nil
	case "revert":
		return nil, f.revertExit, nil
	}
	return nil, 0, nil
}

// gitopsCalled reports whether a git subcommand verb was invoked.
func (f *gitopsFakeRunner) gitopsCalled(verb string) bool {
	for _, c := range f.calls {
		for _, a := range c[1:] {
			if strings.HasPrefix(a, "-") || strings.Contains(a, "=") {
				continue
			}
			if a == verb {
				return true
			}
			break
		}
	}
	return false
}

// gitopsNewBackend builds a GitOps backend over a temp working tree with the fake
// runner injected and returns the backend, a Desired pointed at it, and the workdir.
func gitopsNewBackend(t *testing.T, fr *gitopsFakeRunner) (*GitOpsBackend, Desired, string) {
	t.Helper()
	dir := t.TempDir()
	gb := NewGitOpsBackend(GitOpsConfig{
		WorkdirRoot: dir,
		Branch:      "main",
		Remote:      "origin",
		Namespace:   "agents",
		PathPrefix:  "clusters/prod/apps",
		Timeout:     time.Minute,
	})
	gb.runner = fr
	d := desired("gitops")
	return gb, d, dir
}

func TestGitOpsKind(t *testing.T) {
	gb := NewGitOpsBackend(GitOpsConfig{WorkdirRoot: "/tmp"})
	if gb.Kind() != "gitops" {
		t.Fatalf("Kind = %q, want gitops", gb.Kind())
	}
}

func TestGitOpsPlanCreate(t *testing.T) {
	gb, d, _ := gitopsNewBackend(t, &gitopsFakeRunner{})
	p, err := gb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	if len(p.Diff.Creates) != 1 || p.Diff.Count() != 1 {
		t.Fatalf("absent manifest must plan exactly 1 create, got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastAdditive {
		t.Fatalf("a create-only diff must be Additive, got %v", p.Diff.BlastRadius)
	}
	if !p.Diff.Reversible {
		t.Fatalf("a gitops change must be reversible (git revert)")
	}
	if p.Handle == "" {
		t.Fatalf("a non-noop plan must carry a staged handle for apply")
	}
}

func TestGitOpsPlanNoopWhenAlreadyApplied(t *testing.T) {
	gb, d, dir := gitopsNewBackend(t, &gitopsFakeRunner{})
	// Pre-commit the EXACT manifest the backend would render.
	abs := gb.gitopsAbsPath(d)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(gb.gitopsRenderManifest(d)), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := gb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	if !p.Diff.Empty() {
		t.Fatalf("planning an already-applied spec must yield an EMPTY diff, got %+v", p.Diff)
	}
	if p.Handle != "" {
		t.Fatalf("a noop plan must carry no handle")
	}
	_ = dir
}

func TestGitOpsPlanUpdate(t *testing.T) {
	gb, d, _ := gitopsNewBackend(t, &gitopsFakeRunner{})
	abs := gb.gitopsAbsPath(d)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	// Commit a DIFFERENT manifest (a stale image) so the desired diverges.
	stale := d
	stale.Image = "old/image:0.0.1"
	if err := os.WriteFile(abs, []byte(gb.gitopsRenderManifest(stale)), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := gb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	if len(p.Diff.Updates) != 1 || p.Diff.Count() != 1 {
		t.Fatalf("a diverged manifest must plan exactly 1 update, got %+v", p.Diff)
	}
	if p.Diff.BlastRadius != BlastMutating {
		t.Fatalf("an update-only diff must be Mutating, got %v", p.Diff.BlastRadius)
	}
}

func TestGitOpsDestroyPlanIsDestructive(t *testing.T) {
	gb, d, _ := gitopsNewBackend(t, &gitopsFakeRunner{})
	abs := gb.gitopsAbsPath(d)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(gb.gitopsRenderManifest(d)), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := gb.DestroyPlan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	if len(p.Diff.Deletes) != 1 {
		t.Fatalf("destroy plan must contain exactly 1 delete, got %+v", p.Diff)
	}
	if !p.Diff.Deletes[0].Destructive || p.Diff.BlastRadius != BlastDestructive {
		t.Fatalf("a delete must be Destructive and make the diff Destructive, got %+v", p.Diff)
	}
}

func TestGitOpsDestroyPlanNoopWhenAbsent(t *testing.T) {
	gb, d, _ := gitopsNewBackend(t, &gitopsFakeRunner{})
	p, err := gb.DestroyPlan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	if !p.Diff.Empty() {
		t.Fatalf("destroying an already-absent manifest must be an empty noop, got %+v", p.Diff)
	}
}

func TestGitOpsApplyCommitsPushesAndWritesManifest(t *testing.T) {
	fr := &gitopsFakeRunner{}
	gb, d, _ := gitopsNewBackend(t, fr)
	p, err := gb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	res, err := gb.Apply(context.Background(), p, mockCred())
	if err != nil {
		t.Fatalf("apply err = %v", err)
	}
	if !fr.gitopsCalled("add") || !fr.gitopsCalled("commit") || !fr.gitopsCalled("push") {
		t.Fatalf("apply must add+commit+push, calls=%v", fr.calls)
	}
	if len(res.Applied) != 1 {
		t.Fatalf("apply result must report the applied change, got %+v", res)
	}
	// The manifest must actually be written into the working tree at the target path.
	abs := gb.gitopsAbsPath(d)
	raw, rerr := os.ReadFile(abs)
	if rerr != nil {
		t.Fatalf("apply must write the manifest to the working tree: %v", rerr)
	}
	if !strings.Contains(string(raw), "kind: Deployment") {
		t.Fatalf("written manifest is not a Deployment:\n%s", raw)
	}
}

func TestGitOpsApplyNoopHandle(t *testing.T) {
	fr := &gitopsFakeRunner{}
	gb, _, _ := gitopsNewBackend(t, fr)
	res, err := gb.Apply(context.Background(), Plan{Runtime: "gitops", Intent: IntentApply}, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("an empty-handle (noop) plan must change nothing, got %+v", res)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("a noop apply must not shell out to git, calls=%v", fr.calls)
	}
}

func TestGitOpsApplyDestroyRemovesManifest(t *testing.T) {
	fr := &gitopsFakeRunner{}
	gb, d, _ := gitopsNewBackend(t, fr)
	abs := gb.gitopsAbsPath(d)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(gb.gitopsRenderManifest(d)), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := gb.DestroyPlan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	if _, err := gb.Apply(context.Background(), p, mockCred()); err != nil {
		t.Fatalf("destroy apply err = %v", err)
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("destroy apply must remove the manifest from the working tree (stat err=%v)", err)
	}
	if !fr.gitopsCalled("push") {
		t.Fatalf("destroy apply must push the removal")
	}
}

// TestGitOpsCredentialNeverInArgvOrManifest is the load-bearing security assertion:
// the credential material must never reach git's argv, the manifest, or the Result —
// it lives only in the child env (GIT_PASSWORD), fed to git through the askpass pipe.
func TestGitOpsCredentialNeverInArgvOrManifest(t *testing.T) {
	fr := &gitopsFakeRunner{}
	gb, d, _ := gitopsNewBackend(t, fr)
	// An env-ref so the manifest has a secretKeyRef (a reference, never the value).
	d.EnvRefs = []SecretBinding{{Name: "OPENAI_API_KEY", SecretRef: "k8s:acme-bot-secrets/OPENAI_API_KEY"}}
	cred := mockCred()

	p, err := gb.Plan(context.Background(), d, cred)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	res, err := gb.Apply(context.Background(), p, cred)
	if err != nil {
		t.Fatal(err)
	}

	// 1) Token must NEVER appear in argv (it would be `ps`-visible).
	for _, call := range fr.calls {
		for _, a := range call {
			if strings.Contains(a, cred.Token) {
				t.Fatalf("credential material leaked into argv: %v", call)
			}
		}
	}
	// 2) Token must NEVER appear in the remote URL on the push line.
	for _, call := range fr.calls {
		if len(call) > 1 && call[1] == "push" {
			for _, a := range call {
				if strings.Contains(a, "://") && strings.Contains(a, cred.Token) {
					t.Fatalf("token leaked into the push URL: %v", call)
				}
			}
		}
	}
	// 3) Token IS injected into the child env (GIT_PASSWORD) for the askpass helper.
	var injected bool
	for _, env := range fr.envs {
		for _, kv := range env {
			if kv == "GIT_PASSWORD="+cred.Token {
				injected = true
			}
		}
	}
	if !injected {
		t.Fatalf("the attested credential must be injected into the child env (GIT_PASSWORD) for the askpass helper")
	}
	// 4) Token must NEVER appear in the Result (id may, material must not).
	if strings.Contains(res.Detail, cred.Token) {
		t.Fatalf("credential material leaked into Result.Detail")
	}
	for _, it := range res.Applied {
		if strings.Contains(it.Ref+it.Detail, cred.Token) {
			t.Fatalf("credential material leaked into a change item: %+v", it)
		}
	}
	// 5) The written manifest references the secret natively, never the token value.
	abs := gb.gitopsAbsPath(d)
	raw, _ := os.ReadFile(abs)
	body := string(raw)
	if strings.Contains(body, cred.Token) {
		t.Fatalf("credential material leaked into the manifest")
	}
	if !strings.Contains(body, "secretKeyRef") || !strings.Contains(body, "acme-bot-secrets") {
		t.Fatalf("env must be a native secretKeyRef REFERENCE, manifest:\n%s", body)
	}
}

func TestGitOpsRollbackReverts(t *testing.T) {
	fr := &gitopsFakeRunner{}
	gb, d, _ := gitopsNewBackend(t, fr)
	p, err := gb.Plan(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	if _, err := gb.Rollback(context.Background(), p, mockCred()); err != nil {
		t.Fatalf("rollback err = %v", err)
	}
	if !fr.gitopsCalled("revert") || !fr.gitopsCalled("push") {
		t.Fatalf("rollback must git revert + push, calls=%v", fr.calls)
	}
	// token never in argv for the rollback path either
	for _, call := range fr.calls {
		for _, a := range call {
			if strings.Contains(a, mockCred().Token) {
				t.Fatalf("credential leaked into rollback argv: %v", call)
			}
		}
	}
}

// --- observe ---------------------------------------------------------------------

func TestGitOpsObserveGapWhenUnconfigured(t *testing.T) {
	gb, d, _ := gitopsNewBackend(t, &gitopsFakeRunner{})
	rs, err := gb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if rs.Observable {
		t.Fatalf("with no status endpoint configured, Observe must report an HONEST gap (Observable=false), got %+v", rs)
	}
	if rs.InSync {
		t.Fatalf("an unobservable unit must NEVER be reported in-sync")
	}
	if !strings.Contains(rs.Detail, "delegated") {
		t.Fatalf("the gap detail must explain delegation, got %q", rs.Detail)
	}
}

func TestGitOpsObserveArgoSynced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// must be a read-only GET to the Argo Application path with a bearer header
		if r.Method != http.MethodGet {
			t.Errorf("status read must be a GET, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/applications/") {
			t.Errorf("unexpected status path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+mockCred().Token {
			t.Errorf("status GET must carry the short-lived bearer, got %q", got)
		}
		_, _ = w.Write([]byte(`{"status":{"sync":{"status":"Synced"},"health":{"status":"Healthy"}}}`))
	}))
	defer srv.Close()

	gb, d, _ := gitopsNewBackend(t, &gitopsFakeRunner{})
	gb.cfg.StatusController = "argocd"
	gb.cfg.StatusBaseURL = srv.URL
	gb.cfg.StatusNamespace = "argocd"
	gb.httpClient = srv.Client()

	rs, err := gb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if !rs.Observable || !rs.InSync || !rs.Exists {
		t.Fatalf("Synced+Healthy must be observable & in-sync, got %+v", rs)
	}
	if len(rs.Drift) != 0 {
		t.Fatalf("a Synced app must report no drift, got %+v", rs.Drift)
	}
	if strings.Contains(rs.Detail, mockCred().Token) {
		t.Fatalf("credential material leaked into RealState.Detail")
	}
}

func TestGitOpsObserveArgoOutOfSyncIsDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":{"sync":{"status":"OutOfSync"},"health":{"status":"Degraded"}}}`))
	}))
	defer srv.Close()

	gb, d, _ := gitopsNewBackend(t, &gitopsFakeRunner{})
	gb.cfg.StatusBaseURL = srv.URL
	gb.httpClient = srv.Client()

	rs, err := gb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if !rs.Observable || rs.InSync {
		t.Fatalf("OutOfSync must be observable & NOT in-sync, got %+v", rs)
	}
	if len(rs.Drift) == 0 {
		t.Fatalf("OutOfSync must surface drift, got %+v", rs)
	}
}

func TestGitOpsObserveArgoNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	gb, d, _ := gitopsNewBackend(t, &gitopsFakeRunner{})
	gb.cfg.StatusBaseURL = srv.URL
	gb.httpClient = srv.Client()

	rs, err := gb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	// A not-yet-reconciled app is observable (we read the controller) but not in-sync,
	// and must NOT be faked as existing+synced.
	if !rs.Observable || rs.InSync || rs.Exists {
		t.Fatalf("a 404 Application must be observable, not in-sync, not existing, got %+v", rs)
	}
}

func TestGitOpsObserveFluxReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/kustomizations/") {
			t.Errorf("flux status path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":{"conditions":[{"type":"Ready","status":"True","reason":"ReconciliationSucceeded"}]}}`))
	}))
	defer srv.Close()

	gb, d, _ := gitopsNewBackend(t, &gitopsFakeRunner{})
	gb.cfg.StatusController = "flux"
	gb.cfg.StatusBaseURL = srv.URL
	gb.cfg.StatusNamespace = "flux-system"
	gb.httpClient = srv.Client()

	rs, err := gb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if !rs.Observable || !rs.InSync {
		t.Fatalf("a Ready=True Kustomization must be observable & in-sync, got %+v", rs)
	}
}

func TestGitOpsObserveFluxNotReadyIsDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":{"conditions":[{"type":"Ready","status":"False","reason":"BuildFailed"}]}}`))
	}))
	defer srv.Close()

	gb, d, _ := gitopsNewBackend(t, &gitopsFakeRunner{})
	gb.cfg.StatusController = "flux"
	gb.cfg.StatusBaseURL = srv.URL
	gb.httpClient = srv.Client()

	rs, err := gb.Observe(context.Background(), d, mockCred())
	if err != nil {
		t.Fatal(err)
	}
	if !rs.Observable || rs.InSync || len(rs.Drift) == 0 {
		t.Fatalf("a Ready=False Kustomization must be observable, not in-sync, with drift, got %+v", rs)
	}
}

// --- end-to-end through the Executor (gate proves a plain apply that deletes is
// blocked, and an additive apply is allowed). ------------------------------------

func TestGitOpsAdditiveApplyThroughExecutor(t *testing.T) {
	fr := &gitopsFakeRunner{}
	gb, d, _ := gitopsNewBackend(t, fr)
	e := New(WithBackend(gb, "gitops"), WithCredentialSource(mockCredSource(time.Hour)))
	res, err := e.Apply(context.Background(), d)
	if err != nil {
		t.Fatalf("an additive gitops apply must pass the gate, got %v", err)
	}
	if res.BackendID != "gitops" || !strings.HasSuffix(res.CredentialID, ":write") {
		t.Fatalf("result must carry backend id + write credential id, got %+v", res)
	}
	if !fr.gitopsCalled("push") {
		t.Fatalf("the apply must reach push, calls=%v", fr.calls)
	}
}

func TestGitOpsRetireThroughExecutor(t *testing.T) {
	fr := &gitopsFakeRunner{}
	gb, d, _ := gitopsNewBackend(t, fr)
	// Pre-commit so the destroy plan has something to delete.
	abs := gb.gitopsAbsPath(d)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(gb.gitopsRenderManifest(d)), 0o644); err != nil {
		t.Fatal(err)
	}
	e := New(WithBackend(gb, "gitops"), WithCredentialSource(mockCredSource(time.Hour)))
	if _, err := e.Retire(context.Background(), d); err != nil {
		t.Fatalf("retire (deliberate teardown) must be allowed by default, got %v", err)
	}
	if !fr.gitopsCalled("push") {
		t.Fatalf("retire must push the removal, calls=%v", fr.calls)
	}
}
