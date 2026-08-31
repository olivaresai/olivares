// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// Sentinel errors. Every one is a deny-closed, honest failure — never a pretend
// success that would let the control plane believe it reconciled infrastructure.
var (
	// ErrNoBackend is returned when no backend is registered for a runtime. The
	// composition root wires backends by runtime/policy (never hardcoded); a
	// definition that targets an un-wired runtime fails closed rather than acting.
	ErrNoBackend = errors.New("executor: no backend wired for this runtime")
	// ErrBlastRadius is returned when the blast-radius gate blocks a change. It is a
	// SECOND control on top of the HITL approval the module already enforces.
	ErrBlastRadius = errors.New("executor: change blocked by blast-radius policy")
	// ErrStateUnlocked is returned by a declarative backend asked to act against
	// state without a remote lock (state integrity is non-negotiable, docs/SECURITY-HARDENING.md).
	ErrStateUnlocked = errors.New("executor: declarative state has no remote lock; refusing to act")
)

// BlastRadius classifies how far a change reaches. It is the load-bearing input to
// the blast-radius gate: a Destructive change over the configured threshold is
// blocked without an explicit allowance (blastradius.go).
type BlastRadius int

const (
	// BlastReadOnly is a no-op / refresh: nothing changes.
	BlastReadOnly BlastRadius = iota
	// BlastAdditive only creates: the lowest-risk mutation.
	BlastAdditive
	// BlastMutating updates resources in place.
	BlastMutating
	// BlastDestructive deletes, replaces or prunes — the highest-risk class.
	BlastDestructive
)

// String renders a BlastRadius for non-sensitive details and audit meta.
func (b BlastRadius) String() string {
	switch b {
	case BlastReadOnly:
		return "read-only"
	case BlastAdditive:
		return "additive"
	case BlastMutating:
		return "mutating"
	case BlastDestructive:
		return "destructive"
	default:
		return "unknown"
	}
}

// ChangeItem is one element of a diff. Minimal data (docs/SECURITY-HARDENING.md): a kind, a
// non-sensitive resource ref and a short detail — NEVER a payload, a command line
// or a secret. It mirrors the module's deploy.Change so the seam adapter can map
// it 1:1 without inventing a richer wire shape.
type ChangeItem struct {
	// Action is "create" | "update" | "delete" | "replace" | "noop".
	Action string
	// Kind is the resource class, e.g. "container", "kubernetes.deployment",
	// "tofu.resource", "nomad.job", "crossplane.xr".
	Kind string
	// Ref is the non-sensitive natural reference of the resource.
	Ref string
	// Detail is a short, non-sensitive description of the change.
	Detail string
	// Destructive marks an item that deletes/replaces real state (drives BlastRadius).
	Destructive bool
}

// Diff is the structured dry-run output of a plan: the create/update/delete sets,
// the overall blast radius, whether the change is reversible, and an opaque handle
// to the saved plan the matching Apply must execute (anti-blind-apply: the applied
// plan is exactly the planned one).
type Diff struct {
	Creates      []ChangeItem
	Updates      []ChangeItem
	Deletes      []ChangeItem
	BlastRadius  BlastRadius
	Reversible   bool
	RollbackHint string // a short, non-sensitive note on how this apply is reversed
	Summary      string
}

// Empty reports whether the diff changes nothing (an idempotent no-op).
func (d Diff) Empty() bool { return len(d.Creates)+len(d.Updates)+len(d.Deletes) == 0 }

// Count is the total number of changed items across all sets.
func (d Diff) Count() int { return len(d.Creates) + len(d.Updates) + len(d.Deletes) }

// Items returns every change item in a stable order (creates, updates, deletes).
func (d Diff) Items() []ChangeItem {
	out := make([]ChangeItem, 0, d.Count())
	out = append(out, d.Creates...)
	out = append(out, d.Updates...)
	out = append(out, d.Deletes...)
	return out
}

// classifyBlastRadius derives the overall blast radius from a diff's contents. Any
// destructive item makes the whole diff Destructive; otherwise updates make it
// Mutating, creates-only make it Additive, and nothing makes it read-only.
func classifyBlastRadius(creates, updates, deletes []ChangeItem) BlastRadius {
	for _, set := range [][]ChangeItem{creates, updates, deletes} {
		for _, it := range set {
			if it.Destructive {
				return BlastDestructive
			}
		}
	}
	switch {
	case len(deletes) > 0:
		return BlastDestructive
	case len(updates) > 0:
		return BlastMutating
	case len(creates) > 0:
		return BlastAdditive
	default:
		return BlastReadOnly
	}
}

// NewDiff assembles a Diff from its change sets, deriving the blast radius. The
// caller supplies reversibility (a property of the backend + the change shape).
func NewDiff(creates, updates, deletes []ChangeItem, reversible bool, rollbackHint, summary string) Diff {
	return Diff{
		Creates: creates, Updates: updates, Deletes: deletes,
		BlastRadius: classifyBlastRadius(creates, updates, deletes),
		Reversible:  reversible, RollbackHint: rollbackHint, Summary: summary,
	}
}

// Intent distinguishes a forward apply, a teardown, and a rollback so a backend
// (and the audit trail) knows what a saved Plan does.
type Intent int

const (
	// IntentApply reconciles toward the desired state (create/update).
	IntentApply Intent = iota
	// IntentDestroy tears the deployment down (delete) — always gated.
	IntentDestroy
	// IntentRollback reverses a prior apply.
	IntentRollback
)

// Desired is the NEUTRAL desired state the seam adapter builds from the module's
// typed deploySpec. It carries references only — never a cleartext secret (the
// module already guarantees this; secret material reaches a backend, if at all,
// only via the runtime's native secret mechanism, never persisted here).
type Desired struct {
	Tenant      string // business tenant id (string form; this package never pins a tenant)
	Environment string // "prod" | "staging" | ... — drives short-lived credential scoping
	Target      string // backend connection target ref, e.g. "docker.host/node1", "k8s.namespace/ns", "tofu.workspace/<dir>"
	Runtime     string // backend selector: docker|k8s|nomad|tofu|terraform|gitops|crossplane
	SubjectKind string // "agent" | "mcp_server"
	SubjectRef  string // logical subject name / external id
	Name        string // logical deployment name
	Image       string // container image / artifact ref (non-sensitive)
	Command     string // optional entrypoint override (non-sensitive)
	Replicas    int
	Resources   map[string]string // non-sensitive compute requests
	EnvRefs     []SecretBinding   // env values BY REFERENCE to a secret-store
	Wirings     []Wiring          // declared agent→resource connections (references only)
	SpecHash    string            // hex hash of the canonical desired spec (drift compare)

	// Workspace / hardening fields. All OPTIONAL and additive: a Desired with
	// none of them set produces exactly the pre create body (no drift churn for
	// existing deployments — they are NOT part of dockerSpecHash). They carry
	// NON-SENSITIVE host paths only (never a secret); the CALLER canonicalizes and
	// jails the mount sources (modules/sessions does, §3.1 of the contract) — the
	// executor never resolves or validates a path.
	Mounts         []Mount  // host bind mounts (the rw workspace + any ro reference mounts)
	ReadonlyRootfs bool     // mount the container root filesystem read-only (the rest of the box)
	TmpfsMounts    []string // writable scratch tmpfs targets (e.g. "/tmp") when ReadonlyRootfs is set
	RunAsUser      string   // non-root identity, e.g. "65532:65532" (Docker container User)
	WorkingDir     string   // container working directory (e.g. the workspace mount target)
}

// Mount is one host bind mount applied to a managed container. Source is an
// ABSOLUTE host path the CALLER has canonicalized/jailed (the executor never
// resolves or validates it); Target is the in-container path; ReadOnly binds it ro.
// It is non-sensitive (a path, never a secret) and safe to record in a Diff item.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// SecretBinding is one environment value supplied by secret-store reference. The
// executor passes the reference to the runtime's native secret mechanism; it never
// resolves the reference to cleartext or persists it (docs/SECURITY-HARDENING.md).
type SecretBinding struct {
	Name      string
	SecretRef string // "<scheme>:<locator>" — never the secret itself
}

// Wiring is one declared agent→resource connection (references only).
type Wiring struct {
	ResourceKind string
	ResourceRef  string
	Mode         string // "read" | "write" | "readwrite"
	SecretRef    string // reference only
}

// Unit is the stable identity of a managed unit for observe()/drift. It is derived
// from a Desired and is what the drift loop keys on.
type Unit struct {
	Tenant      string
	Environment string
	Target      string
	Runtime     string
	SubjectKind string
	SubjectRef  string
}

// Unit derives the managed-unit identity of a Desired.
func (d Desired) Unit() Unit {
	return Unit{
		Tenant: d.Tenant, Environment: d.Environment, Target: d.Target,
		Runtime: d.Runtime, SubjectKind: d.SubjectKind, SubjectRef: d.SubjectRef,
	}
}

// RealState is what observe() reads from REAL infrastructure. Observable=false is
// an HONEST gap (the unit can't be read) — never silently reported as in-sync.
type RealState struct {
	Exists     bool
	Observable bool         // false => can't read this unit; surface a gap, never fake in-sync
	InSync     bool         // real matches desired (meaningful only when Observable)
	Drift      []ChangeItem // the desired-vs-real delta (empty when InSync)
	Detail     string       // short, non-sensitive
}

// Plan is a saved, ready-to-apply plan produced by a backend. The Handle is an
// opaque, backend-specific reference to the saved plan (e.g. a temp tfplan path);
// it is internal to a single Executor.Apply call, never persisted and never a
// secret. A Plan carries its Diff so Apply executes exactly what was planned.
type Plan struct {
	Runtime string
	Intent  Intent
	Diff    Diff
	Handle  string
	// workdir is the backend working directory the saved plan belongs to (e.g. the
	// tofu workspace). Internal to a single apply call; never persisted, non-secret.
	workdir string
	// payload carries backend-specific apply data the neutral Diff cannot (e.g. the
	// container command + env-by-reference a docker create needs). It is INTERNAL to a
	// single Plan→Apply within one Executor call, never persisted, and carries
	// references only — never a cleartext secret.
	payload any
	// cleanup releases any backend-side resources of the saved plan (temp files).
	// It is nil when there is nothing to release.
	cleanup func()
}

// Cleanup releases the saved plan's resources; safe to call on a zero Plan.
func (p Plan) Cleanup() {
	if p.cleanup != nil {
		p.cleanup()
	}
}

// WithCleanup returns a copy of the plan with a cleanup function attached.
func (p Plan) WithCleanup(fn func()) Plan { p.cleanup = fn; return p }

// Result is the outcome of an apply/rollback/destroy.
type Result struct {
	Applied      []ChangeItem
	Detail       string
	BackendID    string // the backend kind that acted (non-sensitive, for audit meta)
	CredentialID string // the NON-SENSITIVE id of the short-lived credential used; the material is NEVER recorded
}

// Backend is the per-runtime actuator. Every method runs under a short-lived,
// environment-attested credential (credential.go) the Executor mints and passes
// in; a backend NEVER reads an ambient/long-lived key. All methods are idempotent:
// planning an already-applied spec yields an empty Diff and Apply changes nothing.
type Backend interface {
	// Kind returns the backend's runtime selector (e.g. "tofu").
	Kind() string
	// Plan computes the forward diff (create/update) against real state. Read-only.
	Plan(ctx context.Context, d Desired, cred Credential) (Plan, error)
	// DestroyPlan computes the teardown diff (delete). Read-only; always Destructive.
	DestroyPlan(ctx context.Context, d Desired, cred Credential) (Plan, error)
	// Apply executes a SAVED plan (forward or destroy). Idempotent.
	Apply(ctx context.Context, p Plan, cred Credential) (Result, error)
	// Rollback reverses a prior apply (reversibility).
	Rollback(ctx context.Context, p Plan, cred Credential) (Result, error)
	// Observe reads the REAL state and the desired-vs-real drift. Never mutates; an
	// unobservable unit is reported with Observable=false (a gap, never faked).
	Observe(ctx context.Context, d Desired, cred Credential) (RealState, error)
}

// Executor is the governed orchestrator over a set of runtime backends. It selects
// the backend by runtime, mints a short-lived credential scoped to the operation
// (read for plan/observe, write for apply/destroy), enforces the blast-radius gate
// before any mutation, and returns NEUTRAL Diff/Result/RealState the seam adapter
// maps onto the deploy.Executor contract. It is the AGPL motor's actuation core.
type Executor struct {
	backends map[string]Backend
	creds    CredentialSource
	policy   BlastRadiusPolicy
	log      *slog.Logger
}

// Option configures an Executor at construction.
type Option func(*Executor)

// WithBackend registers a backend under its Kind() (and any extra aliases). A
// later registration of the same key wins (the composition root controls order).
func WithBackend(b Backend, aliases ...string) Option {
	return func(e *Executor) {
		if b == nil {
			return
		}
		e.backends[strings.ToLower(b.Kind())] = b
		for _, a := range aliases {
			if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
				e.backends[a] = b
			}
		}
	}
}

// WithCredentialSource wires the short-lived credential source. Without it, every
// actuation fails closed (deny-closed default; credential.go).
func WithCredentialSource(c CredentialSource) Option {
	return func(e *Executor) {
		if c != nil {
			e.creds = c
		}
	}
}

// WithBlastRadiusPolicy overrides the default blast-radius gate policy.
func WithBlastRadiusPolicy(p BlastRadiusPolicy) Option {
	return func(e *Executor) { e.policy = p }
}

// WithLogger sets the executor's logger (non-secret operational lines only).
func WithLogger(l *slog.Logger) Option {
	return func(e *Executor) {
		if l != nil {
			e.log = l
		}
	}
}

// New builds an Executor with deny-closed defaults: no credential source (every
// actuation fails closed) and the default blast-radius policy (destructive over
// threshold is blocked). Backends are added via WithBackend; with none registered
// every runtime fails closed with ErrNoBackend.
func New(opts ...Option) *Executor {
	e := &Executor{
		backends: map[string]Backend{},
		creds:    DenyCredentialSource{},
		policy:   DefaultBlastRadiusPolicy(),
		log:      slog.Default(),
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Runtimes returns the sorted set of runtimes this executor can act on (for the
// composition root's boot log and the exposed contract).
func (e *Executor) Runtimes() []string {
	out := make([]string, 0, len(e.backends))
	for k := range e.backends {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// backendFor selects the backend for a runtime, failing closed with ErrNoBackend.
func (e *Executor) backendFor(runtime string) (Backend, error) {
	b, ok := e.backends[strings.ToLower(strings.TrimSpace(runtime))]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNoBackend, runtime)
	}
	return b, nil
}

// Plan is the dry-run path (mapped from deploy.Executor.Plan): it returns the
// forward diff without mutating anything. It mints a READ credential; a backend
// that needs no credential simply ignores it, but an un-wired source still fails
// closed (no actuation context, no plan).
func (e *Executor) Plan(ctx context.Context, d Desired) (Diff, error) {
	b, err := e.backendFor(d.Runtime)
	if err != nil {
		return Diff{}, err
	}
	cred, err := e.mint(ctx, d, ModeRead)
	if err != nil {
		return Diff{}, err
	}
	plan, err := b.Plan(ctx, d, cred)
	if err != nil {
		return Diff{}, err
	}
	defer plan.Cleanup()
	return plan.Diff, nil
}

// Apply is the governed mutation path (mapped from deploy.Executor.Apply). It
// re-plans (so the applied plan is exactly the freshly computed one — backend-level
// anti-TOCTOU), short-circuits a no-op, enforces the blast-radius gate, then
// applies under a WRITE credential. The HITL approval has already been checked
// by the module before this is ever called; the blast-radius gate is a SECOND,
// independent control.
func (e *Executor) Apply(ctx context.Context, d Desired) (Result, error) {
	b, err := e.backendFor(d.Runtime)
	if err != nil {
		return Result{}, err
	}
	cred, err := e.mint(ctx, d, ModeWrite)
	if err != nil {
		return Result{}, err
	}
	plan, err := b.Plan(ctx, d, cred)
	if err != nil {
		return Result{}, err
	}
	defer plan.Cleanup()
	if plan.Diff.Empty() {
		return Result{Detail: "already in desired state", BackendID: b.Kind(), CredentialID: cred.ID}, nil
	}
	if err := e.gate(plan.Diff, IntentApply); err != nil {
		return Result{}, err
	}
	res, err := b.Apply(ctx, plan, cred)
	if err != nil {
		return Result{}, err
	}
	res.BackendID, res.CredentialID = b.Kind(), cred.ID
	e.logf("executor: applied", "runtime", b.Kind(), "blast_radius", plan.Diff.BlastRadius.String(), "changes", plan.Diff.Count(), "credential_id", cred.ID)
	return res, nil
}

// Verify is the read-only drift path (mapped from deploy.Executor.Verify). It mints
// a READ credential and observes the REAL state; the returned RealState carries the
// desired-vs-real drift (or an honest gap when the unit is unobservable).
func (e *Executor) Verify(ctx context.Context, d Desired) (RealState, error) {
	b, err := e.backendFor(d.Runtime)
	if err != nil {
		return RealState{}, err
	}
	cred, err := e.mint(ctx, d, ModeRead)
	if err != nil {
		return RealState{}, err
	}
	return b.Observe(ctx, d, cred)
}

// Retire is the governed teardown path (mapped from deploy.Executor.Retire). It
// computes the destroy plan (always Destructive), enforces the blast-radius gate
// (the allowEmpty/destroy guard), then destroys under a WRITE credential. As with
// Apply, the module's HITL gate has already approved; this is the second control.
func (e *Executor) Retire(ctx context.Context, d Desired) (Result, error) {
	b, err := e.backendFor(d.Runtime)
	if err != nil {
		return Result{}, err
	}
	cred, err := e.mint(ctx, d, ModeWrite)
	if err != nil {
		return Result{}, err
	}
	plan, err := b.DestroyPlan(ctx, d, cred)
	if err != nil {
		return Result{}, err
	}
	defer plan.Cleanup()
	if plan.Diff.Empty() {
		return Result{Detail: "nothing to retire (already absent)", BackendID: b.Kind(), CredentialID: cred.ID}, nil
	}
	if err := e.gate(plan.Diff, IntentDestroy); err != nil {
		return Result{}, err
	}
	res, err := b.Apply(ctx, plan, cred)
	if err != nil {
		return Result{}, err
	}
	res.BackendID, res.CredentialID = b.Kind(), cred.ID
	e.logf("executor: retired", "runtime", b.Kind(), "changes", plan.Diff.Count(), "credential_id", cred.ID)
	return res, nil
}

// gate applies the blast-radius policy. A blocked change returns ErrBlastRadius —
// a deny that even a HITL-approved apply cannot bypass (the second control).
func (e *Executor) gate(d Diff, intent Intent) error {
	dec := e.policy.Decide(d, intent)
	if !dec.Allowed {
		e.logf("executor: blast-radius gate BLOCKED a change",
			"blast_radius", d.BlastRadius.String(), "intent", intent, "reason", dec.Reason)
		return fmt.Errorf("%w: %s", ErrBlastRadius, dec.Reason)
	}
	return nil
}

// mint obtains a short-lived, environment-scoped credential for an operation.
func (e *Executor) mint(ctx context.Context, d Desired, mode Mode) (Credential, error) {
	cred, err := e.creds.Mint(ctx, MintRequest{Environment: d.Environment, Target: d.Target, Runtime: d.Runtime, Mode: mode})
	if err != nil {
		return Credential{}, fmt.Errorf("executor: short-lived credential unavailable (fail-closed): %w", err)
	}
	if cred.Expired(nowFunc()) {
		return Credential{}, errors.New("executor: minted credential is already expired (fail-closed)")
	}
	return cred, nil
}

// logf logs a non-secret operational line if a logger is set.
func (e *Executor) logf(msg string, args ...any) {
	if e.log != nil {
		e.log.Info(msg, args...)
	}
}
