// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Backend is one pluggable isolation primitive (gVisor/runsc, Firecracker
// microVM, …). It owns the FULL lifecycle of one ephemeral instance: a fresh,
// hardened instance per Execute, the in-instance workload (resolve steps; deliver
// a probe to the target over the egress proxy), and a VERIFIED destruction.
//
// A backend MUST NOT claim isolation it cannot enforce: Preflight detects whether
// the primitive is actually present and functioning, and Execute is only ever
// called on a backend that passed it (the engine fails closed otherwise — NEVER a
// faked microVM). proxyAddr is the loopback address of the engine-owned,
// deny-by-default egress proxy: the instance has no NIC of its own and reaches the
// network ONLY through it.
type Backend interface {
	// Name is the backend's policy selector and the attestation runner name.
	Name() string
	// Isolated reports the backend's real OS-level isolation guarantee.
	Isolated() bool
	// Preflight verifies the isolation primitive is present and functioning. A
	// non-nil error means the backend is unavailable and must not be used.
	Preflight(ctx context.Context) error
	// Execute runs the job's workload in a fresh, hardened, ephemeral instance
	// whose only egress is proxyAddr, then DESTROYS the instance and VERIFIES it is
	// gone. profile is the hardening profile to apply.
	Execute(ctx context.Context, job Job, profile Profile, proxyAddr string) (BackendResult, error)
}

// BackendResult is what a backend reports after one run: the per-step outputs, the
// target response (red-team), and the FACTS of the instance lifecycle the engine
// folds into the Attestation. Nothing here is persisted by this package.
type BackendResult struct {
	Steps      []StepOutput
	Response   string
	Reached    bool
	InstanceID string
	// HadNIC reports whether the instance was given a network interface of its own
	// (true only for a red-team run that needs egress to the target; a synthetic,
	// deny-all run has none). The attestation derives NoNIC from this so it reflects
	// the REAL per-run network posture, not the static profile default.
	HadNIC          bool
	Destroyed       bool
	DestroyVerified bool
}

// Engine is the governed orchestrator over a set of isolation backends. It selects
// a backend by POLICY (configured order) — never hardcoded — runs every run in a
// fresh hardened instance behind a per-job deny-by-default egress proxy, and
// returns a neutral Result + Attestation the composition-root adapters map onto
// the sandbox.Runner / redteam.Sandbox contracts.
type Engine struct {
	available   []Backend // backends that passed preflight, in policy order
	unavailable []string  // names of backends that failed preflight (for the boot log)
	profile     Profile
	res         resolver
	log         *slog.Logger
}

// Option configures an Engine at construction.
type Option func(*engineConfig)

// engineConfig accumulates options before New partitions backends by preflight.
type engineConfig struct {
	backends []Backend
	profile  Profile
	res      resolver
	log      *slog.Logger
}

// WithBackend registers an isolation backend. The registration ORDER is the
// selection policy: the first backend that passes preflight is the primary.
func WithBackend(b Backend) Option {
	return func(c *engineConfig) {
		if b != nil {
			c.backends = append(c.backends, b)
		}
	}
}

// WithProfile overrides the hardening profile (default HardenedProfile()).
func WithProfile(p Profile) Option { return func(c *engineConfig) { c.profile = p } }

// WithResolver overrides the egress proxy's DNS resolver (tests pin it).
func WithResolver(r resolver) Option { return func(c *engineConfig) { c.res = r } }

// WithLogger sets the engine's logger (non-secret operational lines only).
func WithLogger(l *slog.Logger) Option {
	return func(c *engineConfig) {
		if l != nil {
			c.log = l
		}
	}
}

// New builds an Engine and runs each backend's Preflight ONCE, partitioning them
// into available (passed) and unavailable (failed), preserving policy order. With
// NO backend available every Run fails closed with ErrNoIsolation — the honest,
// deny-closed default an operator sees when the host lacks runsc/firecracker.
func New(opts ...Option) *Engine {
	cfg := engineConfig{profile: HardenedProfile(), res: netResolver{}, log: slog.Default()}
	for _, o := range opts {
		o(&cfg)
	}
	e := &Engine{profile: cfg.profile, res: cfg.res, log: cfg.log}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, b := range cfg.backends {
		if err := b.Preflight(ctx); err != nil {
			e.unavailable = append(e.unavailable, b.Name())
			e.logf("sandboxrt: backend unavailable (preflight failed; will not be used)", "backend", b.Name(), "err", err.Error())
			continue
		}
		e.available = append(e.available, b)
	}
	if len(e.available) == 0 {
		e.logf("sandboxrt: NO isolation backend available; every run fails closed (ErrNoIsolation) until a primitive is provisioned",
			"unavailable", strings.Join(e.unavailable, ","))
	} else {
		e.logf("sandboxrt: isolation runtime ready", "primary", e.available[0].Name(), "available", strings.Join(e.names(), ","))
	}
	return e
}

// names returns the available backend names (policy order).
func (e *Engine) names() []string {
	out := make([]string, 0, len(e.available))
	for _, b := range e.available {
		out = append(out, b.Name())
	}
	return out
}

// Available reports whether at least one isolation backend passed preflight.
func (e *Engine) Available() bool { return len(e.available) > 0 }

// Primary returns the identity of the backend a run will use by default (the
// first available in policy order). ok=false ⇒ no backend available; the caller
// records an honest degraded run (runner="unavailable", isolated=false) rather
// than faking a microVM.
func (e *Engine) Primary() (name string, isolated bool, ok bool) {
	if len(e.available) == 0 {
		return "unavailable", false, false
	}
	b := e.available[0]
	return b.Name(), b.Isolated(), true
}

// selectBackend picks the backend for a job: an explicit Prefer must be available
// and matched; otherwise the policy primary. Fails closed with ErrNoIsolation.
func (e *Engine) selectBackend(prefer string) (Backend, error) {
	if len(e.available) == 0 {
		return nil, ErrNoIsolation
	}
	if p := strings.TrimSpace(strings.ToLower(prefer)); p != "" {
		for _, b := range e.available {
			if strings.ToLower(b.Name()) == p {
				return b, nil
			}
		}
		return nil, fmt.Errorf("%w: preferred backend %q is not available", ErrNoIsolation, prefer)
	}
	return e.available[0], nil
}

// Run executes a job in a fresh, hardened, egress-controlled ephemeral instance
// and returns the neutral Result + Attestation. It (1) selects a backend by
// policy, (2) starts a per-job deny-by-default egress proxy (empty allowlist ⇒
// total deny), (3) runs the workload in the instance with the proxy as its sole
// egress, (4) folds the proxy log + the backend's lifecycle facts into the
// attestation. A red-team job MUST carry a target; the egress scope is the
// adapter's responsibility (defense in depth on top of the consent gate).
func (e *Engine) Run(ctx context.Context, job Job) (Result, error) {
	if job.Probe != nil && strings.TrimSpace(job.Target) == "" {
		return Result{}, ErrNoTarget
	}
	b, err := e.selectBackend(job.Prefer)
	if err != nil {
		return Result{}, err
	}
	started := time.Now()

	proxy, err := startEgressProxy(job.Egress, e.res)
	if err != nil {
		return Result{}, fmt.Errorf("sandboxrt: cannot start egress proxy (fail-closed): %w", err)
	}
	defer func() { _ = proxy.Close() }()

	br, runErr := b.Execute(ctx, job, e.profile, proxy.Addr())
	finished := time.Now()
	if runErr != nil {
		// An execution fault still yields an honest attestation (the run was
		// attempted under this backend); the caller records status=error.
		e.logf("sandboxrt: run failed", "backend", b.Name(), "err", runErr.Error())
		return Result{
			EgressLog:   proxy.Log(),
			Attestation: e.attest(b, job, br, proxy, started, finished),
		}, runErr
	}
	if !br.DestroyVerified {
		// The work completed but the ephemeral guarantee could not be confirmed —
		// reported honestly (DestroyVerified=false), never assumed away.
		e.logf("sandboxrt: ephemeral destruction NOT verified", "backend", b.Name(), "instance", br.InstanceID)
	}
	return Result{
		Steps:       br.Steps,
		Response:    br.Response,
		Reached:     br.Reached,
		EgressLog:   proxy.Log(),
		Attestation: e.attest(b, job, br, proxy, started, finished),
	}, nil
}

// attest assembles the per-run isolation attestation from the profile, the
// backend's lifecycle facts and the egress proxy's verdict counts. Every field
// reflects the REAL backend — a degraded backend is visible, not hidden.
func (e *Engine) attest(b Backend, job Job, br BackendResult, proxy *egressProxy, started, finished time.Time) Attestation {
	_, denied := proxy.counts()
	return Attestation{
		Backend:      b.Name(),
		Isolated:     b.Isolated(),
		InstanceID:   br.InstanceID,
		ReadonlyRoot: e.profile.ReadonlyRoot,
		TmpfsOnly:    e.profile.ReadonlyRoot && len(e.profile.TmpfsMounts) > 0,
		CapsDropped:  e.profile.DropAllCaps,
		NoNewPrivs:   e.profile.NoNewPrivileges,
		Seccomp:      e.profile.Seccomp,
		// NoNIC reflects the REAL per-run posture: a synthetic run has no interface;
		// a red-team run that needed egress was given one (host-restricted to the
		// proxy). Either way egress is governed by the deny-by-default proxy.
		NoNIC:             !br.HadNIC,
		EgressDenyDefault: true,
		EgressAllowed:     len(job.Egress.Allow),
		EgressDenied:      denied,
		Destroyed:         br.Destroyed,
		DestroyVerified:   br.DestroyVerified,
		StartedAt:         started,
		FinishedAt:        finished,
	}
}

// logf logs a non-secret operational line if a logger is set.
func (e *Engine) logf(msg string, args ...any) {
	if e.log != nil {
		e.log.Info(msg, args...)
	}
}
