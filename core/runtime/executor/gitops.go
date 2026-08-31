// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GitOpsBackend is the SAFEST Kubernetes default (the brief): the
// engine holds NO cluster write credentials. Instead of talking to the API server,
// it RENDERS the Desired into a Kubernetes Deployment manifest and COMMITS+PUSHES it
// to a Git repo/branch/path. A GitOps controller already running IN the cluster
// (Argo CD or Flux) watches that repo and reconciles — the cluster pulls, the engine
// never pushes to the apiserver. The blast radius of a leaked engine credential is
// therefore "can open a commit to a config repo", not "can delete a namespace".
//
// FOUR OPERATIONS over git (never the apiserver):
//
//   - Plan: render the desired manifest and DIFF it against what is currently
//     committed at the target path. Absent => create. Changed => update.
//     Present-and-equal => an EMPTY diff (idempotent noop). A teardown => delete.
//   - Apply (forward): copy the rendered manifest into the path, `git add/commit/push`.
//   - Apply (destroy): remove the manifest from the path, `git add/commit/push`.
//   - Rollback: `git revert` the apply commit and push. Reversible=true — a GitOps
//     change is always reversible because git history IS the audit/undo log.
//   - Observe: OPTIONAL. If a controller status endpoint is configured (a k8s API
//     base URL + a read token via the short-lived credential), GET the Argo CD
//     Application or the Flux Kustomization and map its sync/health to InSync/Drift.
//     If NO status source is configured, return an HONEST gap (Observable=false) —
//     reconciliation is delegated to the controller and we cannot see it from here.
//     We NEVER fake in-sync.
//
// SELF-HEAL vs OUT-OF-SYNC (Argo CD semantics, kept distinct on purpose):
//
//   - selfHeal=true (auto-heal): the controller actively reverts manual drift in the
//     cluster back to git. selfHeal=false (the common, safer alert-only posture):
//     OutOfSync is REPORTED to the operator but NOT auto-corrected — a human decides.
//   - The RECONCILE interval (Argo polls/refreshes the repo, ~180s by default; Flux
//     interval is typically 1–10m) and the SELF-HEAL retry (Argo re-attempts a heal
//     after a short backoff, ~5s seed, capped) are TWO DISTINCT timers. Do not
//     conflate them: a fresh push is picked up on the next reconcile (up to the
//     reconcile interval of latency), and only THEN — if selfHeal is on and the live
//     state diverges — does the much faster self-heal retry loop engage. This backend
//     models neither timer; it only writes git and reads the controller's verdict.
//
// CREDENTIALS (docs/SECURITY-HARDENING.md,§4): the short-lived, environment-attested credential's
// token is the GIT credential. It is NEVER placed in the remote URL or in argv
// (`ps`-visible secrets are forbidden). It is handed to git via an explicit child
// ENV: a 0600 GIT_ASKPASS helper script (created per-apply and removed after) prints
// the token on git's credential prompt, so it travels through a pipe, never the
// process table. The same token, when a status endpoint is configured, is the Bearer
// for the read-only controller status GET (apiRequest.bearer => header only).
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): the rendered manifest references secrets ONLY by the
// runtime's native mechanism (env valueFrom.secretKeyRef) — never values. No Diff,
// Result or RealState ever carries the manifest body, the token, an env value, or a
// command line.
type GitOpsBackend struct {
	cfg    GitOpsConfig
	runner cmdRunner
	// httpClient is the read-only HTTPS client for the OPTIONAL controller status GET.
	// It is built lazily from the config (a pinned TLS bearer client); a test injects
	// an httptest.Server's client to avoid a real apiserver.
	httpClient *http.Client
}

// GitOpsConfig configures the GitOps backend (operator-provisioned; no secrets).
type GitOpsConfig struct {
	// Binary is the git executable (default "git").
	Binary string
	// WorkdirRoot is a LOCAL clone of the config repo the operator provisions and
	// keeps checked out on Branch. Apply writes/commits/pushes inside it. Required:
	// the engine never clones blind into a temp dir with an unknown remote (a clone
	// target is itself an attack surface; the operator pins the working tree). This
	// local socket-equivalent IS the least-privilege boundary for the git mechanics —
	// the only credential that ever reaches git is the minted, short-lived push token.
	WorkdirRoot string
	// Branch is the branch to commit onto and push (default "main").
	Branch string
	// Remote is the git remote name to push to (default "origin"). The remote URL is
	// whatever the operator-provisioned clone already points at; the engine never
	// rewrites it (a token in the URL is forbidden).
	Remote string
	// PathPrefix is the sub-directory under the repo root where manifests live (e.g.
	// "clusters/prod/apps").
	PathPrefix string
	// Namespace is the Kubernetes namespace the rendered Deployment targets.
	Namespace string
	// AuthorName / AuthorEmail stamp the commit (non-secret identity of the engine).
	AuthorName  string
	AuthorEmail string
	// GitUsername is the username half of HTTP basic auth for the push (the token is
	// the password). Default "x-access-token" (the convention GitHub/GitLab accept).
	// Never a secret.
	GitUsername string

	// --- OPTIONAL controller status source (Observe). If StatusBaseURL is empty,
	// Observe returns an honest gap. ---

	// StatusController selects how to read status: "argocd" (default when a base URL
	// is set) or "flux".
	StatusController string
	// StatusBaseURL is the k8s API server base (e.g. "https://api.k8s:6443"). When
	// empty, Observe is a delegated gap.
	StatusBaseURL string
	// StatusNamespace is the namespace the Application/Kustomization lives in (Argo CD
	// commonly "argocd"; Flux "flux-system"). Defaults to Namespace when empty.
	StatusNamespace string
	// StatusAppName overrides the Application/Kustomization name (defaults to the
	// rendered workload name).
	StatusAppName string
	// StatusCABundle pins the k8s API server (PEM). StatusInsecure is an explicit
	// operator opt-in to skip verification (never the default).
	StatusCABundle []byte
	StatusInsecure bool

	// Timeout bounds a single git invocation / status call (default 2m).
	Timeout time.Duration
}

// NewGitOpsBackend builds the GitOps backend. The git runner is injectable (set here,
// overridable in tests). When a status base URL is configured, a pinned TLS bearer
// client is built for the read-only controller status GET (overridable in tests by
// setting httpClient).
func NewGitOpsBackend(cfg GitOpsConfig) *GitOpsBackend {
	if cfg.Binary == "" {
		cfg.Binary = "git"
	}
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if cfg.Remote == "" {
		cfg.Remote = "origin"
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if cfg.GitUsername == "" {
		cfg.GitUsername = "x-access-token"
	}
	if cfg.AuthorName == "" {
		cfg.AuthorName = "Olivares.AI Engine"
	}
	if cfg.AuthorEmail == "" {
		cfg.AuthorEmail = "engine@olivares.ai"
	}
	if cfg.StatusController == "" {
		cfg.StatusController = "argocd"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Minute
	}
	g := &GitOpsBackend{cfg: cfg, runner: &execRunner{timeout: cfg.Timeout}}
	if strings.TrimSpace(cfg.StatusBaseURL) != "" {
		// Best-effort build of the pinned status client; a malformed CA bundle is
		// surfaced at Observe time (Observable=false), never a panic at construction.
		if hc, err := tlsBearerClient(cfg.StatusCABundle, cfg.StatusInsecure, cfg.Timeout); err == nil {
			g.httpClient = hc
		}
	}
	return g
}

// Kind returns the runtime selector.
func (g *GitOpsBackend) Kind() string { return "gitops" }

// --- rendering -------------------------------------------------------------------

// gitopsManifestPath is the repo-relative path of a desired unit's manifest. It is
// deterministic (subject ref + a .yaml suffix) so the same Desired always maps to the
// same file (the basis of the idempotent compare).
func (g *GitOpsBackend) gitopsManifestPath(d Desired) string {
	rel := gitopsWorkloadName(d) + ".yaml"
	if p := strings.Trim(g.cfg.PathPrefix, "/"); p != "" {
		rel = p + "/" + rel
	}
	return rel
}

// gitopsAbsPath is the absolute on-disk path of a desired unit's manifest inside the
// operator-provisioned working tree.
func (g *GitOpsBackend) gitopsAbsPath(d Desired) string {
	return filepath.Join(g.cfg.WorkdirRoot, filepath.FromSlash(g.gitopsManifestPath(d)))
}

// gitopsWorkloadName derives the Deployment metadata.name from the subject ref (or
// the logical name), sanitized to a DNS-1123 label.
func gitopsWorkloadName(d Desired) string {
	n := d.SubjectRef
	if strings.TrimSpace(n) == "" {
		n = d.Name
	}
	return gitopsSanitizeLabel(n)
}

// gitopsSanitizeLabel coerces an identifier into a DNS-1123-ish label so it is a valid
// Kubernetes object name (lowercase alnum and '-', bounded length). It NEVER invents
// data; an empty result falls back to "workload".
func gitopsSanitizeLabel(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "workload"
	}
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	return out
}

// gitopsRenderManifest renders the Desired into a Kubernetes Deployment manifest as a
// canonical YAML string. It is PURE (no I/O), so plan and apply render the exact same
// bytes for the exact same Desired (the basis of the idempotent compare AND of
// anti-blind-apply: the saved plan stages these exact bytes). Secrets are referenced
// ONLY via env valueFrom.secretKeyRef — never values (docs/SECURITY-HARDENING.md).
//
// The YAML is hand-emitted from the standard library (no third-party yaml dep). The
// shape is intentionally minimal and stable: a change in any rendered field changes
// the bytes and therefore surfaces as an "update" in the plan.
func (g *GitOpsBackend) gitopsRenderManifest(d Desired) string {
	name := gitopsWorkloadName(d)
	replicas := d.Replicas
	if replicas < 0 {
		replicas = 0
	}
	var b strings.Builder
	w := func(s string) { b.WriteString(s) }
	w("apiVersion: apps/v1\n")
	w("kind: Deployment\n")
	w("metadata:\n")
	w("  name: " + gitopsYAMLScalar(name) + "\n")
	w("  namespace: " + gitopsYAMLScalar(g.cfg.Namespace) + "\n")
	w("  labels:\n")
	w("    app.kubernetes.io/name: " + gitopsYAMLScalar(name) + "\n")
	w("    app.kubernetes.io/managed-by: olivares-ai\n")
	w("    olivares.ai/subject-kind: " + gitopsYAMLScalar(gitopsSanitizeLabel(d.SubjectKind)) + "\n")
	w("spec:\n")
	w(fmt.Sprintf("  replicas: %d\n", replicas))
	w("  selector:\n")
	w("    matchLabels:\n")
	w("      app.kubernetes.io/name: " + gitopsYAMLScalar(name) + "\n")
	w("  template:\n")
	w("    metadata:\n")
	w("      labels:\n")
	w("        app.kubernetes.io/name: " + gitopsYAMLScalar(name) + "\n")
	w("    spec:\n")
	w("      containers:\n")
	w("        - name: " + gitopsYAMLScalar(name) + "\n")
	w("          image: " + gitopsYAMLScalar(d.Image) + "\n")
	if cmd := strings.TrimSpace(d.Command); cmd != "" {
		w("          command:\n")
		for _, part := range strings.Fields(cmd) {
			w("            - " + gitopsYAMLScalar(part) + "\n")
		}
	}
	if len(d.Resources) > 0 {
		w("          resources:\n")
		w("            requests:\n")
		for _, k := range gitopsSortedKeys(d.Resources) {
			w("              " + gitopsYAMLScalar(k) + ": " + gitopsYAMLScalar(d.Resources[k]) + "\n")
		}
	}
	if len(d.EnvRefs) > 0 {
		// EnvRefs become env entries that reference a secret BY REFERENCE — the value
		// is read by the kubelet from the named secret at pod start, never embedded
		// here (docs/SECURITY-HARDENING.md). The SecretRef "<scheme>:<locator>" maps to a native
		// secretKeyRef {name,key}. We NEVER emit the secret.
		w("          env:\n")
		for _, e := range d.EnvRefs {
			secretName, secretKey := gitopsParseSecretRef(e.SecretRef, e.Name)
			w("            - name: " + gitopsYAMLScalar(e.Name) + "\n")
			w("              valueFrom:\n")
			w("                secretKeyRef:\n")
			w("                  name: " + gitopsYAMLScalar(secretName) + "\n")
			w("                  key: " + gitopsYAMLScalar(secretKey) + "\n")
		}
	}
	return b.String()
}

// gitopsParseSecretRef maps a "<scheme>:<locator>" secret reference to a native k8s
// secretKeyRef {name, key}. The locator convention is "name/key" (e.g.
// "k8s:acme-bot-secrets/OPENAI_API_KEY"); a locator with no "/" uses the env var name
// as the key. This resolves a REFERENCE to the runtime's native secret mechanism — it
// never reads or emits the secret value.
func gitopsParseSecretRef(ref, envName string) (name, key string) {
	locator := ref
	if i := strings.Index(ref, ":"); i >= 0 {
		locator = ref[i+1:] // strip "<scheme>:"
	}
	locator = strings.TrimSpace(locator)
	if locator == "" {
		// No usable reference: emit a deterministic, non-secret placeholder name so the
		// manifest is valid and a missing/misconfigured ref is VISIBLE in git review
		// (never a silent secret embed).
		return gitopsSanitizeLabel(envName) + "-secret", envName
	}
	if i := strings.LastIndex(locator, "/"); i >= 0 {
		return locator[:i], locator[i+1:]
	}
	return locator, envName
}

// gitopsSortedKeys returns a map's keys in sorted order (deterministic rendering)
// without importing sort (small maps; insertion sort).
func gitopsSortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// gitopsYAMLScalar quotes a scalar when needed so the emitted YAML is always valid and
// round-trips deterministically. It double-quotes anything empty, starting with a
// YAML-significant char, or containing a char that would break a bare scalar.
func gitopsYAMLScalar(s string) string {
	if s == "" {
		return `""`
	}
	needsQuote := false
	switch s[0] {
	case ' ', '-', '?', ':', ',', '[', ']', '{', '}', '#', '&', '*', '!', '|', '>', '\'', '"', '%', '@', '`':
		needsQuote = true
	}
	if !needsQuote {
		for _, r := range s {
			if r == ':' || r == '#' || r == '\n' || r == '\t' || r == '"' || r == '\\' {
				needsQuote = true
				break
			}
		}
	}
	if !needsQuote {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// --- plan / destroy plan ---------------------------------------------------------

// Plan renders the desired manifest and diffs it against what is committed at the
// target path. Read-only: it never writes the working tree or pushes. It stages the
// rendered bytes in a temp file recorded on the Plan handle so Apply writes EXACTLY
// the planned manifest (anti-blind-apply) without re-rendering from a Desired it does
// not hold; the temp file is non-secret and removed by Plan.Cleanup.
func (g *GitOpsBackend) Plan(ctx context.Context, d Desired, cred Credential) (Plan, error) {
	return g.plan(ctx, d, cred, IntentApply)
}

// DestroyPlan computes the teardown diff: the manifest would be removed from git. It
// is always Destructive. Read-only.
func (g *GitOpsBackend) DestroyPlan(ctx context.Context, d Desired, cred Credential) (Plan, error) {
	return g.plan(ctx, d, cred, IntentDestroy)
}

func (g *GitOpsBackend) plan(_ context.Context, d Desired, _ Credential, intent Intent) (Plan, error) {
	if strings.TrimSpace(g.cfg.WorkdirRoot) == "" {
		return Plan{}, errors.New("executor: gitops WorkdirRoot is not configured (operator must provision a local clone); refusing to act")
	}
	abs := g.gitopsAbsPath(d)
	ref := g.gitopsManifestPath(d)
	rendered := g.gitopsRenderManifest(d)

	committed, exists, rerr := gitopsReadCommitted(abs)
	if rerr != nil {
		return Plan{}, rerr
	}

	if intent == IntentDestroy {
		if !exists {
			// Already absent — nothing to tear down (idempotent noop).
			return Plan{Runtime: g.Kind(), Intent: intent, Diff: NewDiff(nil, nil, nil, true, "", "no changes (manifest already absent from git)")}, nil
		}
		del := ChangeItem{Action: "delete", Kind: "kubernetes.deployment", Ref: ref, Detail: "remove manifest from git (controller prunes the workload)", Destructive: true}
		diff := NewDiff(nil, nil, []ChangeItem{del}, true,
			"git revert the removal commit to restore the manifest",
			"gitops destroy: 1 delete")
		// A destroy stages an empty manifest; Apply removes the target file (it branches
		// on the plan's Intent, so the empty staged manifest is never written).
		staged, cleanup, serr := gitopsStage(abs, "")
		if serr != nil {
			return Plan{}, serr
		}
		return Plan{Runtime: g.Kind(), Intent: intent, Diff: diff, Handle: staged, workdir: g.cfg.WorkdirRoot}.WithCleanup(cleanup), nil
	}

	// Forward apply.
	switch {
	case !exists:
		staged, cleanup, serr := gitopsStage(abs, rendered)
		if serr != nil {
			return Plan{}, serr
		}
		create := ChangeItem{Action: "create", Kind: "kubernetes.deployment", Ref: ref, Detail: "commit new manifest to git"}
		diff := NewDiff([]ChangeItem{create}, nil, nil, true,
			"git revert the apply commit to undo", "gitops apply: 1 create")
		return Plan{Runtime: g.Kind(), Intent: intent, Diff: diff, Handle: staged, workdir: g.cfg.WorkdirRoot}.WithCleanup(cleanup), nil
	case gitopsEqual(committed, rendered):
		// Present and equal — idempotent noop (an already-applied spec yields empty).
		return Plan{Runtime: g.Kind(), Intent: intent, Diff: NewDiff(nil, nil, nil, true, "", "no changes (committed manifest matches desired)")}, nil
	default:
		staged, cleanup, serr := gitopsStage(abs, rendered)
		if serr != nil {
			return Plan{}, serr
		}
		update := ChangeItem{Action: "update", Kind: "kubernetes.deployment", Ref: ref, Detail: "commit updated manifest to git"}
		diff := NewDiff(nil, []ChangeItem{update}, nil, true,
			"git revert the apply commit to restore the prior manifest", "gitops apply: 1 update")
		return Plan{Runtime: g.Kind(), Intent: intent, Diff: diff, Handle: staged, workdir: g.cfg.WorkdirRoot}.WithCleanup(cleanup), nil
	}
}

// gitopsReadCommitted reads the manifest currently on disk at abs (the operator-
// provisioned working tree, assumed checked out on Branch). A missing file is
// exists=false (a create), not an error. A read error (permission, etc.) IS an error —
// we never silently treat an unreadable file as absent.
func gitopsReadCommitted(abs string) (content string, exists bool, err error) {
	raw, rerr := os.ReadFile(abs)
	if rerr != nil {
		if errors.Is(rerr, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, errors.New("executor: gitops cannot read the committed manifest at the target path")
	}
	return string(raw), true, nil
}

// gitopsEqual compares two manifest bodies for byte-equality after trailing-newline
// normalization (a checkout may add/strip a final newline). It compares CONTENT only;
// no secret is involved (manifests reference secrets, never carry them).
func gitopsEqual(a, b string) bool {
	return strings.TrimRight(a, "\n") == strings.TrimRight(b, "\n")
}

// --- staged-plan handle ----------------------------------------------------------
//
// The frozen Plan struct carries no payload (minimal data, docs/SECURITY-HARDENING.md), so the
// rendered manifest bytes computed at plan time cannot ride inside the Plan struct.
// Yet Apply must write EXACTLY what was planned (anti-blind-apply), not re-render from
// a Desired it does not hold. Mirroring TofuBackend (which saves a tfplan FILE and
// records its path in Plan.Handle), Plan stages a small temp directory holding two
// files — `target` (the absolute destination path) and `manifest` (the rendered
// bytes, empty for a destroy) — and records that dir in Plan.Handle. Plan.Cleanup
// removes it. The temp dir is internal to a single Executor.Apply call, never
// persisted, never logged, and carries no secret (a manifest references secrets, never
// carries them).

const gitopsStageTargetFile = "target"
const gitopsStageManifestFile = "manifest"

// gitopsStage writes the staged plan dir and returns its path plus a cleanup func.
// A destroy stages an empty manifest (the apply removes the target file instead, by
// branching on the plan's Intent).
func gitopsStage(targetAbs, manifest string) (handle string, cleanup func(), err error) {
	dir, derr := os.MkdirTemp("", "olivares-gitops-plan-*")
	if derr != nil {
		return "", func() {}, errors.New("executor: gitops cannot stage the plan")
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	if werr := os.WriteFile(filepath.Join(dir, gitopsStageTargetFile), []byte(targetAbs), 0o600); werr != nil {
		cleanup()
		return "", func() {}, errors.New("executor: gitops cannot stage the plan target")
	}
	if werr := os.WriteFile(filepath.Join(dir, gitopsStageManifestFile), []byte(manifest), 0o600); werr != nil {
		cleanup()
		return "", func() {}, errors.New("executor: gitops cannot stage the plan manifest")
	}
	return dir, cleanup, nil
}

// gitopsReadStage reads back the staged target path and manifest bytes.
func gitopsReadStage(handle string) (targetAbs, manifest string, err error) {
	t, terr := os.ReadFile(filepath.Join(handle, gitopsStageTargetFile))
	if terr != nil {
		return "", "", errors.New("executor: gitops apply has no staged plan (re-plan before apply)")
	}
	m, merr := os.ReadFile(filepath.Join(handle, gitopsStageManifestFile))
	if merr != nil {
		return "", "", errors.New("executor: gitops apply has no staged manifest (re-plan before apply)")
	}
	return string(t), string(m), nil
}

// --- apply / rollback ------------------------------------------------------------

// Apply executes a SAVED plan by mutating git: it writes (forward) or removes
// (destroy) the manifest at the saved target path, then `git add` / `git commit` /
// `git push`. The credential is given to git via an explicit child env (a 0600
// GIT_ASKPASS helper) — never argv, never the remote URL. Idempotent: an empty-handle
// plan (a noop) changes nothing.
func (g *GitOpsBackend) Apply(ctx context.Context, p Plan, cred Credential) (Result, error) {
	if p.Handle == "" {
		// Noop plan ("already in desired state") — nothing to do.
		return Result{Applied: nil, Detail: "no changes to apply"}, nil
	}
	targetAbs, manifest, rerr := gitopsReadStage(p.Handle)
	if rerr != nil {
		return Result{}, rerr
	}
	destroy := p.Intent == IntentDestroy

	env, cleanup, err := g.childEnv(cred)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	dir := g.cfg.WorkdirRoot

	if destroy {
		if rmErr := os.Remove(targetAbs); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return Result{}, errors.New("executor: gitops cannot remove the manifest from the working tree")
		}
	} else {
		if mkErr := os.MkdirAll(filepath.Dir(targetAbs), 0o755); mkErr != nil {
			return Result{}, errors.New("executor: gitops cannot create the manifest directory")
		}
		if wErr := os.WriteFile(targetAbs, []byte(manifest), 0o644); wErr != nil {
			return Result{}, errors.New("executor: gitops cannot write the manifest to the working tree")
		}
	}

	rel, relErr := filepath.Rel(dir, targetAbs)
	if relErr != nil {
		rel = targetAbs
	}
	// Stage the change (`add -A` covers both an added and a removed tracked file).
	if _, code, e := g.runner.run(ctx, dir, env, g.cfg.Binary, "add", "-A", "--", rel); e != nil || code != 0 {
		return Result{}, fmt.Errorf("executor: gitops add failed (exit %d)", code)
	}
	verb := "apply"
	if destroy {
		verb = "retire"
	}
	msg := fmt.Sprintf("chore(deploy): %s %s", verb, filepath.Base(rel))
	commitArgs := []string{
		"-c", "user.name=" + g.cfg.AuthorName,
		"-c", "user.email=" + g.cfg.AuthorEmail,
		"commit", "-m", msg,
	}
	if _, code, e := g.runner.run(ctx, dir, env, g.cfg.Binary, commitArgs...); e != nil || code != 0 {
		return Result{}, fmt.Errorf("executor: gitops commit failed (exit %d)", code)
	}
	if _, code, e := g.runner.run(ctx, dir, env, g.cfg.Binary, "push", g.cfg.Remote, "HEAD:"+g.cfg.Branch); e != nil || code != 0 {
		return Result{}, fmt.Errorf("executor: gitops push failed (exit %d)", code)
	}
	return Result{Applied: p.Diff.Items(), Detail: p.Diff.Summary}, nil
}

// Rollback reverses the apply commit with `git revert` and pushes. A GitOps change is
// always reversible because the git history is the undo log. It reverts HEAD (the
// commit the apply just created) rather than re-rendering — git is the source of truth.
func (g *GitOpsBackend) Rollback(ctx context.Context, p Plan, cred Credential) (Result, error) {
	if strings.TrimSpace(g.cfg.WorkdirRoot) == "" {
		return Result{}, errors.New("executor: gitops WorkdirRoot is not configured; cannot roll back")
	}
	env, cleanup, err := g.childEnv(cred)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	dir := g.cfg.WorkdirRoot
	if _, code, e := g.runner.run(ctx, dir, env, g.cfg.Binary, "revert", "--no-edit", "HEAD"); e != nil || code != 0 {
		return Result{}, fmt.Errorf("executor: gitops revert failed (exit %d)", code)
	}
	if _, code, e := g.runner.run(ctx, dir, env, g.cfg.Binary, "push", g.cfg.Remote, "HEAD:"+g.cfg.Branch); e != nil || code != 0 {
		return Result{}, fmt.Errorf("executor: gitops push (rollback) failed (exit %d)", code)
	}
	return Result{Applied: p.Diff.Items(), Detail: "rolled back via git revert"}, nil
}

// --- credential injection (never argv, never the remote URL) ---------------------

// childEnv builds the explicit child environment for git and materializes the
// short-lived credential into a 0600 GIT_ASKPASS helper script. git invokes the helper
// to obtain the password (the token); the token therefore travels through a pipe and
// NEVER appears in argv (`ps`-visible) or the remote URL. The returned cleanup removes
// the helper. No ambient long-lived secret is inherited.
//
// A token-less credential is permitted only for a workdir whose remote needs no auth
// (e.g. a file:// remote in tests / an SSH remote keyed by the operator); in that case
// no GIT_ASKPASS is installed. We never fail closed on the token for git the way the
// tofu backend does, because the local working tree + operator-pinned remote IS the
// least-privilege boundary; the token augments it when the remote requires HTTP auth.
func (g *GitOpsBackend) childEnv(cred Credential) (env []string, cleanup func(), err error) {
	cleanup = func() {}
	env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"GIT_TERMINAL_PROMPT=0", // never block on an interactive prompt
		"GIT_CONFIG_NOSYSTEM=1", // do not read /etc/gitconfig (explicit, no ambient config)
	}
	if cred.Token == "" {
		return env, cleanup, nil
	}
	helper, askCleanup, herr := g.writeAskpass()
	if herr != nil {
		return nil, func() {}, herr
	}
	env = append(env,
		"GIT_ASKPASS="+helper,
		// The helper reads these from its inherited env (NOT argv): the username for the
		// "Username for ..." prompt and the token for the "Password for ..." prompt. The
		// token thus reaches git through a pipe — never the process table, never the URL.
		"GIT_USERNAME="+g.cfg.GitUsername,
		"GIT_PASSWORD="+cred.Token,
	)
	return env, askCleanup, nil
}

// writeAskpass writes a tiny 0600 shell script that echoes the username on a
// "Username" prompt and the token on a "Password" prompt. git passes the prompt text
// as $1; the helper reads the username/token ONLY from its inherited environment
// (GIT_USERNAME / GIT_PASSWORD, set by childEnv), so neither is in argv and neither is
// written into the script body's static text. The token never reaches this script's
// source — only git's inherited env, via a pipe to the helper's stdout.
func (g *GitOpsBackend) writeAskpass() (path string, cleanup func(), err error) {
	f, ferr := os.CreateTemp("", "olivares-gitops-askpass-*.sh")
	if ferr != nil {
		return "", func() {}, errors.New("executor: gitops cannot create the credential helper")
	}
	name := f.Name()
	cleanup = func() { _ = os.Remove(name) }
	// The script reads the secret from its environment (GIT_PASSWORD), NOT from a
	// literal — so the token is never written to disk in cleartext within the script.
	body := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  *[Uu]sername*) printf '%s' \"$GIT_USERNAME\" ;;\n" +
		"  *) printf '%s' \"$GIT_PASSWORD\" ;;\n" +
		"esac\n"
	if _, werr := f.WriteString(body); werr != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, errors.New("executor: gitops cannot write the credential helper")
	}
	if cerr := f.Close(); cerr != nil {
		cleanup()
		return "", func() {}, errors.New("executor: gitops cannot finalize the credential helper")
	}
	if cherr := os.Chmod(name, 0o700); cherr != nil {
		cleanup()
		return "", func() {}, errors.New("executor: gitops cannot set credential helper permissions")
	}
	return name, cleanup, nil
}

// --- observe ---------------------------------------------------------------------

// Observe reports the GitOps controller's verdict on the unit. If NO status endpoint
// is configured, it returns an HONEST gap (Observable=false): reconciliation is
// delegated to the controller and is not visible from the engine. When configured, it
// GETs the Argo CD Application or the Flux Kustomization (read-only) using the
// short-lived credential as the Bearer and maps sync/health to InSync/Drift. We NEVER
// fake in-sync.
func (g *GitOpsBackend) Observe(ctx context.Context, d Desired, cred Credential) (RealState, error) {
	if strings.TrimSpace(g.cfg.StatusBaseURL) == "" {
		return RealState{
			Observable: false,
			Detail:     "reconciliation delegated to the GitOps controller; configure a status endpoint to observe",
		}, nil
	}
	if g.httpClient == nil {
		return RealState{Observable: false, Detail: "gitops status client is misconfigured (CA bundle invalid?)"}, nil
	}
	switch strings.ToLower(strings.TrimSpace(g.cfg.StatusController)) {
	case "flux":
		return g.observeFlux(ctx, d, cred)
	default:
		return g.observeArgo(ctx, d, cred)
	}
}

// observeArgo GETs an Argo CD Application and maps .status.sync.status /
// .status.health.status. OutOfSync => drift (alert-only when selfHeal is off; the
// controller auto-corrects only when selfHeal is on — two distinct behaviors, see the
// type doc). A missing/unreadable Application is an honest gap, never faked.
func (g *GitOpsBackend) observeArgo(ctx context.Context, d Desired, cred Credential) (RealState, error) {
	path := fmt.Sprintf("/apis/argoproj.io/v1alpha1/namespaces/%s/applications/%s", g.statusNamespace(), g.statusAppName(d))
	code, body, err := doAPI(ctx, g.httpClient, apiRequest{
		method: "GET", baseURL: g.cfg.StatusBaseURL, path: path,
		bearer: cred.Token, accept: "application/json",
	}, maxAPIBody)
	if err != nil {
		return RealState{Observable: false, Detail: "argocd status unreachable"}, nil
	}
	if code == 404 {
		return RealState{Exists: false, Observable: true, InSync: false,
			Detail: "argocd Application not found (manifest pushed but not yet reconciled, or app not created)"}, nil
	}
	if !ok2xx(code) {
		return RealState{Observable: false, Detail: "argocd status query was rejected"}, nil
	}
	var app struct {
		Status struct {
			Sync struct {
				Status string `json:"status"`
			} `json:"sync"`
			Health struct {
				Status string `json:"status"`
			} `json:"health"`
		} `json:"status"`
	}
	if jerr := json.Unmarshal(body, &app); jerr != nil {
		return RealState{Observable: false, Detail: "argocd status JSON is malformed"}, nil
	}
	sync := app.Status.Sync.Status
	inSync := strings.EqualFold(sync, "Synced")
	rs := RealState{
		Exists:     true,
		Observable: true,
		InSync:     inSync,
		Detail:     fmt.Sprintf("argocd sync=%s health=%s", gitopsSafeWord(sync), gitopsSafeWord(app.Status.Health.Status)),
	}
	if !inSync {
		rs.Drift = []ChangeItem{{
			Action: "update", Kind: "kubernetes.deployment", Ref: g.gitopsManifestPath(d),
			Detail: "argocd reports OutOfSync (selfHeal=off => alert-only; selfHeal=on => controller auto-heals on its retry timer)",
		}}
	}
	return rs, nil
}

// observeFlux GETs a Flux Kustomization and maps its Ready condition. Flux models
// readiness as a condition type "Ready" with status "True"/"False" — there is no
// Argo-style "Synced" word; readiness IS the in-sync signal.
func (g *GitOpsBackend) observeFlux(ctx context.Context, d Desired, cred Credential) (RealState, error) {
	path := fmt.Sprintf("/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/%s/kustomizations/%s", g.statusNamespace(), g.statusAppName(d))
	code, body, err := doAPI(ctx, g.httpClient, apiRequest{
		method: "GET", baseURL: g.cfg.StatusBaseURL, path: path,
		bearer: cred.Token, accept: "application/json",
	}, maxAPIBody)
	if err != nil {
		return RealState{Observable: false, Detail: "flux status unreachable"}, nil
	}
	if code == 404 {
		return RealState{Exists: false, Observable: true, InSync: false,
			Detail: "flux Kustomization not found (manifest pushed but not yet reconciled)"}, nil
	}
	if !ok2xx(code) {
		return RealState{Observable: false, Detail: "flux status query was rejected"}, nil
	}
	var ks struct {
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if jerr := json.Unmarshal(body, &ks); jerr != nil {
		return RealState{Observable: false, Detail: "flux status JSON is malformed"}, nil
	}
	ready, reason := false, ""
	for _, c := range ks.Status.Conditions {
		if strings.EqualFold(c.Type, "Ready") {
			ready = strings.EqualFold(c.Status, "True")
			reason = c.Reason
			break
		}
	}
	rs := RealState{
		Exists:     true,
		Observable: true,
		InSync:     ready,
		Detail:     fmt.Sprintf("flux ready=%t reason=%s", ready, gitopsSafeWord(reason)),
	}
	if !ready {
		rs.Drift = []ChangeItem{{
			Action: "update", Kind: "kubernetes.deployment", Ref: g.gitopsManifestPath(d),
			Detail: "flux Kustomization not Ready (reconcile interval and self-heal retry are distinct timers)",
		}}
	}
	return rs, nil
}

// statusAppName resolves the Application/Kustomization name to query.
func (g *GitOpsBackend) statusAppName(d Desired) string {
	if n := strings.TrimSpace(g.cfg.StatusAppName); n != "" {
		return n
	}
	return gitopsWorkloadName(d)
}

// statusNamespace resolves the namespace the Application/Kustomization lives in.
func (g *GitOpsBackend) statusNamespace() string {
	if n := strings.TrimSpace(g.cfg.StatusNamespace); n != "" {
		return n
	}
	return g.cfg.Namespace
}

// gitopsSafeWord keeps only a short, non-sensitive status word for a detail line
// (defends against an unexpected long/odd value reaching a surfaced detail).
func gitopsSafeWord(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	if len(s) > 32 {
		s = s[:32]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 {
			return ' '
		}
		return r
	}, s)
}
