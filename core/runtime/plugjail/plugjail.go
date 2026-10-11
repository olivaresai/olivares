// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package plugjail confines the third-party connector-plugin subprocesses the
// runtime launches. Admission checks the admitted digest at the plugin's path
// before launch; it does not seal execution against a later path replacement.
// Plugjail bounds filesystem access on supported Linux kernels, on top of the
// operator's trust decision. It installs no seccomp filter, clears no bounding
// capabilities, and does not enforce an active health-timeout monitor.
//
// The claim is "signed trusted-operator plugin confinement", never "safe marketplace
// sandbox": see docs/security/PLUGIN-CONFINEMENT-THREAT-MODEL.md for exactly what is
// and is not contained. Every launch emits an Attestation recording the REAL level
// achieved — a control that could not be applied is reported as not-applied, never
// asserted.
package plugjail

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/runtime/confine"
)

// Level is the overall confinement grade a launch achieved.
type Level string

const (
	// LevelStrong requires the full control set, including seccomp and cleared
	// bounding capabilities. Those two are not implemented, so it is unreachable.
	LevelStrong Level = "strong"
	// LevelPartial: env scoping + the lifecycle bound applied, but one or more OS
	// isolation primitives were unavailable and were degraded (recorded per control).
	LevelPartial Level = "partial"
	// LevelMinimal: only the platform-independent controls (env scoping + bounded
	// lifecycle) apply — e.g. on a non-Linux host. Honest, never disguised as strong.
	LevelMinimal Level = "minimal"
)

// Confinement is the resolved, per-plugin confinement request the loader applies at
// exec time. The zero value is intentionally NOT a usable strong profile; callers
// build one with Default() so a plugin can never be launched un-scoped by accident.
type Confinement struct {
	// Name is the plugin's stable identity, for the attestation and cgroup path.
	Name string
	// UID/GID is the dedicated unprivileged identity the plugin runs as. Zero means
	// "keep the engine's identity" (only used when the engine itself is unprivileged
	// and cannot drop further — recorded as a degraded control, never silently).
	UID, GID int
	// MemoryBytes / PidsMax / CPUMaxPercent are the cgroup v2 ceilings (0 ⇒ unset).
	MemoryBytes   int64
	PidsMax       int64
	CPUMaxPercent int
	// ReadableRoots are explicit host read/execute grants, in addition to the plugin
	// directory, selected runtime and named DNS/TLS files. No session roots apply.
	ReadableRoots []string
	// WritableScratch is the single writable path the plugin gets (landlock). Empty ⇒
	// an owned per-plugin directory provisioned by Apply and removed by Cleanup.
	WritableScratch string
	// ExtraEnv is the explicit, minimal allow-list the plugin receives ON TOP of the
	// baseline (PATH). The engine's own environment is NEVER inherited (C1).
	ExtraEnv []string
	// Landlock requests filesystem confinement plus no_new_privs on supported Linux.
	// Seccomp is retained as a request field; no filter is installed in this release.
	Seccomp  bool
	Landlock bool
	// HealthTimeout is retained for compatibility. There is no active health-timeout
	// monitor in this release; the loader's launch timeout and cgroup limits differ.
	HealthTimeout time.Duration
}

// Default returns the baseline strong-intent confinement for a named plugin. The
// actual level is resolved from host support and successful child launch in the
// Attestation — Default states the INTENT (drop to non-root, cap ceilings, seccomp
// + landlock on), the platform decides how much of it is real.
func Default(name string) Confinement {
	return Confinement{
		Name:          name,
		UID:           defaultPluginUID,
		GID:           defaultPluginGID,
		MemoryBytes:   defaultMemoryBytes,
		PidsMax:       defaultPidsMax,
		CPUMaxPercent: defaultCPUMaxPercent,
		Seccomp:       true,
		Landlock:      true,
		HealthTimeout: defaultHealthTimeout,
	}
}

// Baseline resource ceilings (mirrors sandboxrt's hardened profile intent, sized for
// a connector rather than a red-team job).
const (
	defaultPluginUID     = 65533 // an unprivileged, "nobody"-class id distinct from sandboxrt's 65532
	defaultPluginGID     = 65533
	defaultMemoryBytes   = 512 << 20 // 512 MiB
	defaultPidsMax       = 128
	defaultCPUMaxPercent = 100 // one core-equivalent; 0 would mean unbounded
	defaultHealthTimeout = 30 * time.Second
)

// Attestation records launch controls. Apply prepares it; ConfirmLaunch records
// the child-only controls after a successful handshake. Every
// bool reflects a control that ACTUALLY applied on this host; a false is an honest
// "not applied", never a hidden gap. It intentionally mirrors the shape of
// sandboxrt.Attestation so the trust center reads one isolation-evidence vocabulary.
type Attestation struct {
	Plugin        string    `json:"plugin"`
	Platform      string    `json:"platform"`       // runtime.GOOS
	Level         Level     `json:"level"`          // strong | partial | minimal
	EnvScoped     bool      `json:"env_scoped"`     // C1: engine env NOT inherited
	DedicatedUID  bool      `json:"dedicated_uid"`  // C3: ran as a dedicated non-root uid
	UID           int       `json:"uid,omitempty"`  // the per-launch uid actually assigned (0 ⇒ not dropped)
	CapsDropped   bool      `json:"caps_dropped"`   // C3: all capabilities dropped
	NoNewPrivs    bool      `json:"no_new_privs"`   // C3: PR_SET_NO_NEW_PRIVS
	Cgroup        bool      `json:"cgroup"`         // C2: cgroup v2 ceilings applied
	MemoryBytes   int64     `json:"memory_bytes"`   // the ceiling actually written (0 ⇒ none)
	PidsMax       int64     `json:"pids_max"`       // the ceiling actually written (0 ⇒ none)
	Seccomp       bool      `json:"seccomp"`        // C5: deny-by-default filter installed
	Landlock      bool      `json:"landlock"`       // C4: read-only host fs restriction
	EgressBounded bool      `json:"egress_bounded"` // declared-degraded PRE-release: false + note
	Degraded      []string  `json:"degraded,omitempty"`
	At            time.Time `json:"at"`
}

// KillReason retains termination classifications for callers. Plugjail does not
// install an active health-timeout monitor in this release.
type KillReason string

const (
	KillNone      KillReason = ""
	KillTimeout   KillReason = "health_timeout" // retained classification; no active health monitor
	KillOOM       KillReason = "cgroup_oom"     // exceeded the memory ceiling
	KillResource  KillReason = "resource"       // exceeded a pids/cpu ceiling
	KillShutdown  KillReason = "shutdown"       // ordinary teardown
	KillAdmission KillReason = "admission"      // failed a post-launch admission/health check
)

// baselineEnv is the minimal environment every confined plugin receives. It deliberately
// does NOT include the engine's environment — that is the whole point of C1 (the engine
// holds every connector's secrets + KMS/signing keys). Only a hermetic PATH is kept so a
// plugin can find its runtime; the plugin's own resolved config arrives over gRPC, not env.
func baselineEnv() []string {
	path := os.Getenv("PATH")
	if strings.TrimSpace(path) == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	return []string{"PATH=" + path}
}

// ScopedEnv returns the exact environment a confined plugin should be launched with:
// the hermetic baseline plus ONLY the caller's explicit, non-secret extras. It never
// reads or forwards the engine's own environment. A caller that needs the plugin to
// see a value passes it explicitly here, so every variable a plugin receives is an
// auditable decision rather than an inherited default.
func ScopedEnv(extra ...string) []string {
	env := baselineEnv()
	for _, e := range extra {
		if strings.TrimSpace(e) == "" || !strings.Contains(e, "=") {
			continue
		}
		env = append(env, e)
	}
	return env
}

// Cleanup releases any host resources a confinement allocated (e.g. the per-plugin
// cgroup). It is safe to call more than once.
type Cleanup func()

func noopCleanup() {}

// Apply prepares a scoped command and host resource cleanup. On supported Linux,
// the shared re-exec helper must apply Landlock/no_new_privs before exec; those
// child-only controls are recorded by ConfirmLaunch after a successful handshake.
// Bounding capabilities, seccomp and an active health monitor remain absent.
// Callers must retain cmd.Env, cmd.SysProcAttr and the helper executable/argv.
func Apply(cmd *exec.Cmd, c Confinement) (Attestation, Cleanup, error) {
	att := Attestation{
		Plugin:        c.Name,
		Platform:      runtime.GOOS,
		EnvScoped:     true,  // C1 always applies
		EgressBounded: false, // declared-degraded PRE-release (see threat model)
		At:            time.Now(),
	}
	att.Degraded = append(att.Degraded,
		"egress: the resident plugin RPC channel is not network-isolated to a declared allowlist (see threat model)")

	// C1: scope the environment on EVERY platform. This is the load-bearing control —
	// without it a third-party plugin inherits every connector secret + KMS/signing key.
	cmd.Env = ScopedEnv(c.ExtraEnv...)

	cleanup, err := applyOS(cmd, c, &att)
	if err != nil {
		return att, noopCleanup, err
	}
	att.Level = resolveLevel(att)
	return att, cleanup, nil
}

// ConfirmLaunch records the child-only controls after the caller has observed
// a successful plugin handshake. A helper that cannot apply them refuses exec,
// so a failed launch must never call this function.
func ConfirmLaunch(cmd *exec.Cmd, att Attestation) Attestation {
	if confine.IsWrapped(cmd) {
		att.Landlock, att.NoNewPrivs = true, true
	}
	att.Level = resolveLevel(att)
	return att
}

// resolveLevel folds the applied controls into the overall grade. Strong requires the
// full Linux set; env scoping alone (a non-Linux host) is minimal; anything between is
// partial. It reads only what the platform hook actually set, so a level is never more
// optimistic than the controls behind it.
func resolveLevel(a Attestation) Level {
	if a.DedicatedUID && a.CapsDropped && a.NoNewPrivs && a.Cgroup && a.Seccomp && a.Landlock {
		return LevelStrong
	}
	if a.DedicatedUID || a.Cgroup || a.Seccomp || a.Landlock {
		return LevelPartial
	}
	return LevelMinimal
}
