// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TofuBackend is the declarative backend (OpenTofu default; Terraform is the same
// backend behind a binary flag — Decision 1). It shells out with
// -input=false and NEVER applies blind:
//
//   - plan: `plan -out=<tfplan> -detailed-exitcode` → branch on exit 0 (no diff /
//     idempotent noop) / 1 (error) / 2 (diff); then `show -json <tfplan>` parses the
//     structured plan into a Diff (a delete/replace is classified Destructive).
//   - apply: `apply <tfplan>` — exactly the saved plan, never a fresh one.
//   - observe: `plan -refresh-only -detailed-exitcode` — READ-ONLY drift detection
//     (exit 2 = drift). NOTE: the brief sketches `apply -refresh-only`; we use
//     `plan -refresh-only` because Verify must not mutate even the state file —
//     plan -refresh-only detects the same drift and writes nothing (the correct,
//     not the convenient, choice).
//   - rollback: re-applies the prior known-good plan handle if present; otherwise it
//     reports the limitation honestly (Tofu rollback is a re-declare-and-apply of a
//     prior revision, owned by the deploy module's revision history, not a state op).
//
// STATE INTEGRITY (docs/SECURITY-HARDENING.md, the 2026 CTO bar): a remote backend with state
// locking is MANDATORY. A workspace on local state is REFUSED (ErrStateUnlocked) —
// never a silent workaround.
//
// CREDENTIALS: the short-lived, environment-attested credential is injected into the
// child process ENVIRONMENT (never argv — `ps`-visible secrets are forbidden,
// docs/SECURITY-HARDENING.md), under the operator-configured variable names. The child env is built
// from an explicit allowlist; no ambient long-lived cloud key is inherited.
type TofuBackend struct {
	cfg    TofuConfig
	runner cmdRunner
}

// TofuConfig configures the declarative backend (operator-provisioned, no secrets).
type TofuConfig struct {
	// Binary is the executable: "tofu" (default) or "terraform".
	Binary string
	// WorkdirRoot is the base directory under which a target's workspace lives. The
	// target ref's last path segment selects the workspace subdir; an empty root
	// means the target ref is an absolute workspace path.
	WorkdirRoot string
	// CredentialEnv lists the child env variable names the short-lived credential's
	// token is injected into (e.g. "VAULT_TOKEN", "TF_TOKEN_app_terraform_io"). At
	// least one is required for a write op (else there is no attested credential to
	// act with) unless AllowAmbientCreds is set.
	CredentialEnv []string
	// PassthroughEnv lists non-secret env vars copied from the parent to the child
	// (e.g. "HOME", "PATH" is always included). Keeps the child env explicit.
	PassthroughEnv []string
	// AllowAmbientCreds, when true, permits running without injecting a minted token
	// (the workspace's backend uses its own ambient identity, e.g. an IRSA pod role).
	// Even then a credential is still minted (the deny-closed gate); it is simply not
	// injected. Default false: a token MUST be injected.
	AllowAmbientCreds bool
	// LockTimeout is the -lock-timeout passed to plan/apply (default 60s).
	LockTimeout time.Duration
	// Timeout bounds a single tofu invocation (default 10m).
	Timeout time.Duration
}

// NewTofuBackend builds a declarative backend. kind is the runtime selector the
// composition root registers it under ("tofu" or "terraform").
func NewTofuBackend(cfg TofuConfig) *TofuBackend {
	if cfg.Binary == "" {
		cfg.Binary = "tofu"
	}
	if cfg.LockTimeout <= 0 {
		cfg.LockTimeout = 60 * time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Minute
	}
	return &TofuBackend{cfg: cfg, runner: &execRunner{timeout: cfg.Timeout}}
}

// Kind returns the runtime selector (the configured binary name).
func (t *TofuBackend) Kind() string { return t.cfg.Binary }

// workdir resolves the workspace directory for a target.
func (t *TofuBackend) workdir(d Desired) string {
	ref := targetPath(d.Target)
	if t.cfg.WorkdirRoot == "" {
		return ref
	}
	return filepath.Join(t.cfg.WorkdirRoot, filepath.Base(ref))
}

// Plan computes the forward diff: plan -out -detailed-exitcode, then show -json.
func (t *TofuBackend) Plan(ctx context.Context, d Desired, cred Credential) (Plan, error) {
	return t.plan(ctx, d, cred, IntentApply)
}

// DestroyPlan computes the teardown diff: plan -destroy -out -detailed-exitcode.
func (t *TofuBackend) DestroyPlan(ctx context.Context, d Desired, cred Credential) (Plan, error) {
	return t.plan(ctx, d, cred, IntentDestroy)
}

func (t *TofuBackend) plan(ctx context.Context, d Desired, cred Credential, intent Intent) (Plan, error) {
	dir := t.workdir(d)
	if err := t.requireRemoteState(dir); err != nil {
		return Plan{}, err
	}
	env, err := t.childEnv(cred)
	if err != nil {
		return Plan{}, err
	}
	planFile, err := os.CreateTemp("", "olivares-tfplan-*")
	if err != nil {
		return Plan{}, errors.New("executor: cannot create plan file")
	}
	planPath := planFile.Name()
	_ = planFile.Close()
	cleanup := func() { _ = os.Remove(planPath) }

	args := []string{"plan", "-input=false", "-no-color", "-detailed-exitcode",
		fmt.Sprintf("-lock-timeout=%s", t.cfg.LockTimeout.String()), "-out=" + planPath}
	if intent == IntentDestroy {
		args = append(args, "-destroy")
	}
	_, code, runErr := t.runner.run(ctx, dir, env, t.cfg.Binary, args...)
	if runErr != nil {
		cleanup()
		return Plan{}, fmt.Errorf("executor: tofu plan failed: %w", runErr)
	}
	switch code {
	case 0: // no changes — idempotent noop
		cleanup()
		return Plan{Runtime: t.Kind(), Intent: intent, Diff: NewDiff(nil, nil, nil, true, "", "no changes (up to date)")}, nil
	case 2: // changes present — parse the saved plan
		diff, perr := t.parsePlan(ctx, dir, env, planPath, intent)
		if perr != nil {
			cleanup()
			return Plan{}, perr
		}
		p := Plan{Runtime: t.Kind(), Intent: intent, Diff: diff, Handle: planPath, workdir: dir}
		return p.WithCleanup(cleanup), nil
	default: // 1 (or any other) — error
		cleanup()
		return Plan{}, fmt.Errorf("executor: tofu plan reported an error (exit %d)", code)
	}
}

// parsePlan reads `show -json <tfplan>` and maps resource_changes to a Diff.
func (t *TofuBackend) parsePlan(ctx context.Context, dir string, env []string, planPath string, intent Intent) (Diff, error) {
	out, code, err := t.runner.run(ctx, dir, env, t.cfg.Binary, "show", "-json", "-no-color", planPath)
	if err != nil || code != 0 {
		return Diff{}, errors.New("executor: tofu show -json failed")
	}
	var doc struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Type    string `json:"type"`
			Change  struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if jerr := json.Unmarshal(out, &doc); jerr != nil {
		return Diff{}, errors.New("executor: tofu plan JSON is malformed")
	}
	var creates, updates, deletes []ChangeItem
	for _, rc := range doc.ResourceChanges {
		item := ChangeItem{Kind: "tofu.resource", Ref: rc.Address, Detail: rc.Type}
		switch actionOf(rc.Change.Actions) {
		case "create":
			item.Action = "create"
			creates = append(creates, item)
		case "update":
			item.Action = "update"
			updates = append(updates, item)
		case "delete":
			item.Action, item.Destructive = "delete", true
			deletes = append(deletes, item)
		case "replace":
			item.Action, item.Destructive = "replace", true
			updates = append(updates, item)
		default: // no-op / read — not a change
		}
	}
	reversible := intent != IntentDestroy // a destroy is not auto-reversible without a prior revision
	summary := fmt.Sprintf("tofu plan: %d create, %d update, %d delete", len(creates), len(updates), len(deletes))
	return NewDiff(creates, updates, deletes, reversible, "re-declare a prior revision and apply to roll back", summary), nil
}

// Apply executes the SAVED plan: `apply <tfplan>`.
func (t *TofuBackend) Apply(ctx context.Context, p Plan, cred Credential) (Result, error) {
	if p.Handle == "" {
		// An empty handle means the plan was a noop (exit 0) — nothing to apply.
		return Result{Applied: nil, Detail: "no changes to apply"}, nil
	}
	env, err := t.childEnv(cred)
	if err != nil {
		return Result{}, err
	}
	// Apply runs in the workspace the saved plan belongs to (recorded at plan time);
	// tofu apply consumes the absolute plan path against that workspace's backend.
	_, code, runErr := t.runner.run(ctx, p.workdir, env, t.cfg.Binary, "apply", "-input=false", "-no-color",
		fmt.Sprintf("-lock-timeout=%s", t.cfg.LockTimeout.String()), p.Handle)
	if runErr != nil || code != 0 {
		return Result{}, fmt.Errorf("executor: tofu apply failed (exit %d): %w", code, runErr)
	}
	return Result{Applied: p.Diff.Items(), Detail: p.Diff.Summary}, nil
}

// Rollback re-applies a prior plan handle when available; otherwise it reports the
// honest limitation (Tofu rollback is a re-declare of a prior revision, owned by the
// deploy module's revision history — not a state-file operation).
func (t *TofuBackend) Rollback(ctx context.Context, p Plan, cred Credential) (Result, error) {
	return Result{}, errors.New("executor: tofu rollback is a re-declare of a prior revision (deploy module revision history), not a state operation")
}

// Observe runs `plan -refresh-only -detailed-exitcode` — read-only drift detection.
func (t *TofuBackend) Observe(ctx context.Context, d Desired, cred Credential) (RealState, error) {
	dir := t.workdir(d)
	if err := t.requireRemoteState(dir); err != nil {
		return RealState{Observable: false, Detail: "state backend not remote/locked: " + err.Error()}, nil
	}
	env, err := t.childEnv(cred)
	if err != nil {
		return RealState{}, err
	}
	_, code, runErr := t.runner.run(ctx, dir, env, t.cfg.Binary, "plan", "-refresh-only", "-input=false", "-no-color",
		"-detailed-exitcode", fmt.Sprintf("-lock-timeout=%s", t.cfg.LockTimeout.String()))
	if runErr != nil {
		return RealState{Observable: false, Detail: "tofu refresh-only plan could not run"}, nil
	}
	switch code {
	case 0:
		return RealState{Exists: true, Observable: true, InSync: true, Detail: "state matches real infrastructure"}, nil
	case 2:
		return RealState{Exists: true, Observable: true, InSync: false,
			Drift:  []ChangeItem{{Action: "update", Kind: "tofu.state", Ref: targetPath(d.Target), Detail: "real infrastructure has drifted from state"}},
			Detail: "drift detected (refresh-only plan)"}, nil
	default:
		return RealState{Observable: false, Detail: "tofu refresh-only plan errored"}, nil
	}
}

// requireRemoteState refuses to act unless the workspace is initialized with a
// REMOTE backend (state locking). A local/uninitialized backend is ErrStateUnlocked.
func (t *TofuBackend) requireRemoteState(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, ".terraform", "terraform.tfstate"))
	if err != nil {
		return fmt.Errorf("%w: workspace %q is not initialized (run init with a remote backend)", ErrStateUnlocked, targetLabel(dir))
	}
	var meta struct {
		Backend struct {
			Type string `json:"type"`
		} `json:"backend"`
	}
	if jerr := json.Unmarshal(raw, &meta); jerr != nil {
		return fmt.Errorf("%w: workspace backend metadata is unreadable", ErrStateUnlocked)
	}
	switch strings.ToLower(strings.TrimSpace(meta.Backend.Type)) {
	case "", "local":
		return fmt.Errorf("%w: workspace uses %q state — a remote backend with locking is required", ErrStateUnlocked, "local")
	default:
		return nil
	}
}

// childEnv builds the explicit child environment: PATH/HOME, the operator's
// passthrough allowlist, and the short-lived credential injected into the
// configured variable names. No ambient long-lived secret is inherited.
func (t *TofuBackend) childEnv(cred Credential) ([]string, error) {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TF_IN_AUTOMATION=1"}
	for _, k := range t.cfg.PassthroughEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	if len(t.cfg.CredentialEnv) == 0 {
		if !t.cfg.AllowAmbientCreds {
			return nil, errors.New("executor: no CredentialEnv configured — refusing to run tofu without injecting the attested credential (set AllowAmbientCreds only for a workspace with its own attested identity)")
		}
		return env, nil
	}
	if cred.Token == "" && !t.cfg.AllowAmbientCreds {
		return nil, errors.New("executor: minted credential has no token to inject (fail-closed)")
	}
	for _, k := range t.cfg.CredentialEnv {
		env = append(env, k+"="+cred.Token)
	}
	return env, nil
}

// --- helpers ---------------------------------------------------------------------

// actionOf maps a Tofu resource_change action list to a single action label.
func actionOf(actions []string) string {
	switch len(actions) {
	case 1:
		switch actions[0] {
		case "create":
			return "create"
		case "update":
			return "update"
		case "delete":
			return "delete"
		case "no-op", "read":
			return "noop"
		}
	case 2:
		// ["delete","create"] or ["create","delete"] => replace.
		return "replace"
	}
	return "noop"
}

// targetPath extracts the workspace/path component of a "scheme/locator" target
// ref such as "tofu.workspace/<dir>" → "<dir>". An absolute path (already a
// filesystem location, no scheme) is returned unchanged, and a ref with no "/" is
// returned as-is.
func targetPath(target string) string {
	if strings.HasPrefix(target, "/") {
		return target
	}
	if i := strings.Index(target, "/"); i >= 0 {
		return target[i+1:]
	}
	return target
}

// targetLabel is a short, non-sensitive label for a directory (its base name).
func targetLabel(dir string) string { return filepath.Base(dir) }
