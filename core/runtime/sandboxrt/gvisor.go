// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// gvisorKind is the backend's policy selector and attestation runner name.
const gvisorKind = "gvisor"

// GVisorConfig configures the gVisor/runsc backend (operator-provisioned; no
// secrets). gVisor is the DEFAULT OS-level backend (README.md): a user-space
// kernel that intercepts the workload's syscalls, a strong isolation boundary
// that runs on an ordinary Linux host without nested virtualization.
type GVisorConfig struct {
	// Binary is the runsc executable (default "runsc").
	Binary string
	// StateRoot is runsc's container state root (default a temp subdir). Kept off
	// the default so concurrent runs never collide.
	StateRoot string
	// BundleRoot is where per-instance OCI bundles are created (default os.TempDir).
	BundleRoot string
	// RootfsDir is the operator-provisioned, READ-ONLY base root filesystem that
	// contains the guest harness. It is bind-mounted read-only; the backend never
	// writes into it. Absent ⇒ the backend cannot run (fail-closed at Execute).
	RootfsDir string
	// HarnessPath is the in-instance guest-harness entrypoint (default
	// "/sandbox-harness"); it reads the job spec and writes the result JSON.
	HarnessPath string
	// Platform is the runsc platform ("systrap" default, "kvm" for HW accel).
	Platform string
	// Network is the runsc network mode for a RED-TEAM run that needs egress to the
	// target (e.g. "sandbox" with an operator-provisioned route to the proxy). A
	// SYNTHETIC (deny-all) run ALWAYS uses "none" (no NIC). Empty ⇒ "none" even for
	// red-team, so without an operator-configured egress path a probe honestly fails
	// to reach the target (OutcomeError), never a silent open network.
	Network string
	// Timeout bounds a single runsc invocation (default 60s).
	Timeout time.Duration
}

// gvisorBackend is the runsc-backed isolation backend.
type gvisorBackend struct {
	cfg    GVisorConfig
	runner cmdRunner
}

var _ Backend = (*gvisorBackend)(nil)

// NewGVisorBackend builds the gVisor backend with production defaults and the
// os/exec runner. It returns the Backend seam (the concrete type is internal).
func NewGVisorBackend(cfg GVisorConfig) Backend {
	return newGVisorBackendWith(cfg, execRunner{timeout: gvisorTimeout(cfg)})
}

// newGVisorBackendWith is the testable constructor: it accepts the cmdRunner so a
// test can drive the full construct→run→parse→destroy→verify path without runsc.
func newGVisorBackendWith(cfg GVisorConfig, runner cmdRunner) *gvisorBackend {
	if cfg.Binary == "" {
		cfg.Binary = "runsc"
	}
	if cfg.HarnessPath == "" {
		cfg.HarnessPath = "/sandbox-harness"
	}
	if cfg.Platform == "" {
		cfg.Platform = "systrap"
	}
	if cfg.BundleRoot == "" {
		cfg.BundleRoot = filepath.Join(os.TempDir(), "olivares-sandboxrt", "gvisor")
	}
	if cfg.StateRoot == "" {
		cfg.StateRoot = filepath.Join(cfg.BundleRoot, "state")
	}
	return &gvisorBackend{cfg: cfg, runner: runner}
}

func gvisorTimeout(cfg GVisorConfig) time.Duration {
	if cfg.Timeout > 0 {
		return cfg.Timeout
	}
	return 60 * time.Second
}

// Name is the backend's selector.
func (b *gvisorBackend) Name() string { return gvisorKind }

// Isolated reports gVisor's real OS-level isolation guarantee.
func (b *gvisorBackend) Isolated() bool { return true }

// Preflight verifies runsc is present and responds — the capability check that
// makes the backend fail closed on a host without gVisor (NEVER a faked microVM).
func (b *gvisorBackend) Preflight(ctx context.Context) error {
	if _, err := b.runner.look(b.cfg.Binary); err != nil {
		return fmt.Errorf("runsc binary %q not found on PATH", b.cfg.Binary)
	}
	_, code, err := b.runner.run(ctx, "", os.Environ(), b.cfg.Binary, "--version")
	if err != nil {
		return fmt.Errorf("runsc not executable: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("runsc --version exited %d", code)
	}
	return nil
}

// Execute runs the job in a fresh runsc instance: it builds a hardened OCI bundle
// (read-only root, cap-drop ALL, no-new-privs, pinned seccomp, own network
// namespace = no NIC, tmpfs), bind-mounts the job spec read-only, runs the guest
// harness with the proxy as its sole egress, parses the result, then DELETES the
// instance and VERIFIES it is gone.
func (b *gvisorBackend) Execute(ctx context.Context, job Job, profile Profile, proxyAddr string) (BackendResult, error) {
	if strings.TrimSpace(b.cfg.RootfsDir) == "" {
		return BackendResult{}, fmt.Errorf("sandboxrt: gvisor backend has no RootfsDir provisioned (fail-closed)")
	}
	id := newInstanceID(gvisorKind, job.RunID)
	bundleDir := filepath.Join(b.cfg.BundleRoot, id)
	if err := os.MkdirAll(bundleDir, 0o700); err != nil {
		return BackendResult{}, fmt.Errorf("sandboxrt: cannot create bundle dir: %w", err)
	}
	// The bundle dir is the ONLY host state of the instance; remove it on the way
	// out so nothing of the run persists (part of the ephemeral guarantee).
	defer func() { _ = os.RemoveAll(bundleDir) }()

	// 1) Write the guest-harness job spec (bind-mounted read-only into the guest).
	jobBytes, err := encodeHarnessJob(job, proxyAddr)
	if err != nil {
		return BackendResult{InstanceID: id}, err
	}
	jobPath := filepath.Join(bundleDir, "job.json")
	if err := os.WriteFile(jobPath, jobBytes, 0o600); err != nil {
		return BackendResult{InstanceID: id}, fmt.Errorf("sandboxrt: cannot write job spec: %w", err)
	}

	// 2) Build the hardened OCI spec. The harness reads the job spec from the
	// read-only bind mount at /sandbox/job.json; its egress is forced through the
	// proxy (proxyEnv). Everything else is the canonical hardened profile.
	jobMount := ociMount{
		Destination: guestJobPath, Type: "bind", Source: jobPath,
		Options: []string{"ro", "bind", "nosuid", "nodev", "noexec"},
	}
	specBytes, err := buildOCISpec(profile, b.cfg.RootfsDir,
		[]string{b.cfg.HarnessPath, guestJobPath}, proxyEnv(proxyAddr), jobMount)
	if err != nil {
		return BackendResult{InstanceID: id}, err
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "config.json"), specBytes, 0o600); err != nil {
		return BackendResult{InstanceID: id}, fmt.Errorf("sandboxrt: cannot write config.json: %w", err)
	}

	// 3) runsc run (blocking): the container's stdout is the harness result JSON.
	// A SYNTHETIC (deny-all) run has NO network ("none"); a red-team run uses the
	// operator-provisioned mode (route restricted to the proxy), defaulting to
	// "none" when unconfigured so it fails to reach the target rather than opening.
	network, hadNIC := b.networkMode(job)
	stdout, code, err := b.runner.run(ctx, bundleDir, os.Environ(),
		b.cfg.Binary, "--root", b.cfg.StateRoot, "--network="+network, "--platform", b.cfg.Platform,
		"run", "--bundle", bundleDir, id)
	// Always attempt destruction + verification, even on a run fault.
	destroyed, verified := b.destroy(id)
	if err != nil {
		return BackendResult{InstanceID: id, HadNIC: hadNIC, Destroyed: destroyed, DestroyVerified: verified}, err
	}
	if code != 0 {
		return BackendResult{InstanceID: id, HadNIC: hadNIC, Destroyed: destroyed, DestroyVerified: verified},
			fmt.Errorf("sandboxrt: runsc run exited %d", code)
	}
	steps, response, reached, perr := decodeHarnessResult(stdout)
	if perr != nil {
		return BackendResult{InstanceID: id, HadNIC: hadNIC, Destroyed: destroyed, DestroyVerified: verified}, perr
	}
	return BackendResult{
		Steps: steps, Response: response, Reached: reached,
		InstanceID: id, HadNIC: hadNIC, Destroyed: destroyed, DestroyVerified: verified,
	}, nil
}

// networkMode selects the runsc network for a job: "none" (no NIC) for a deny-all
// synthetic run; the operator-configured mode for a red-team run that needs egress
// (defaulting to "none" when unconfigured — a probe then honestly fails to reach
// the target rather than opening an un-routed network). hadNIC reports whether a
// NIC was attached (for the attestation).
func (b *gvisorBackend) networkMode(job Job) (mode string, hadNIC bool) {
	if job.Egress.denyAll() {
		return "none", false
	}
	mode = strings.TrimSpace(b.cfg.Network)
	if mode == "" || strings.EqualFold(mode, "none") {
		return "none", false
	}
	return mode, true
}

// destroy force-deletes the instance and POSITIVELY VERIFIES it is gone: after
// `runsc delete --force`, `runsc state` must RUN cleanly (serr==nil) and report
// the instance ABSENT (a clean non-zero exit). A probe that itself errors cannot
// confirm absence ⇒ verified=false (fail-closed; ErrDestroyUnverified semantics).
// It uses a FRESH context so a canceled/timed-out run does not also abort cleanup.
func (b *gvisorBackend) destroy(id string) (destroyed, verified bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _, derr := b.runner.run(ctx, "", os.Environ(), b.cfg.Binary, "--root", b.cfg.StateRoot, "delete", "--force", id)
	destroyed = derr == nil
	_, code, serr := b.runner.run(ctx, "", os.Environ(), b.cfg.Binary, "--root", b.cfg.StateRoot, "state", id)
	// Verified ONLY when the state probe ran cleanly AND reported the instance
	// absent (non-zero exit). An errored probe (serr!=nil) confirms nothing.
	verified = serr == nil && code != 0
	return destroyed, verified
}

// guestJobPath is the in-instance path the job spec is bind-mounted to.
const guestJobPath = "/sandbox/job.json"
