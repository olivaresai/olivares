// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"errors"
	"time"
)

// Sentinel errors. Every one is a deny-closed, honest failure — never a pretend
// success that would let the control plane believe a run was isolated when it was
// not, or that egress was governed when it was open.
var (
	// ErrNoIsolation is returned when no configured backend passes its preflight
	// (the isolation primitive — runsc / firecracker+KVM — is absent or not
	// functioning). The run fails closed rather than executing un-isolated. This
	// is the runtime equivalent of executor.ErrNoBackend.
	ErrNoIsolation = errors.New("sandboxrt: no isolation backend available (preflight failed); refusing to run un-isolated")
	// ErrEgressDenied is returned by the egress proxy when a destination is not on
	// the job's allowlist. It is the deny-by-default verdict of the network gate.
	ErrEgressDenied = errors.New("sandboxrt: egress denied (destination not on the engagement allowlist)")
	// ErrDestroyUnverified is returned when an instance could not be verified
	// destroyed after a run. An ephemeral guarantee that cannot be CONFIRMED is a
	// failure, not an assumption (docs/SECURITY-HARDENING.md): the run is reported not-destroyed.
	ErrDestroyUnverified = errors.New("sandboxrt: ephemeral instance could not be verified destroyed")
	// ErrNoTarget is returned when a red-team job carries no reachable target
	// endpoint; the runtime never opens an un-scoped network path.
	ErrNoTarget = errors.New("sandboxrt: red-team job has no target endpoint")
)

// Step is one ordered input to resolve/execute inside the instance. Key is the
// stable per-step identifier; Input is the synthetic input / the resource the
// step asks for. It mirrors the sandbox module's own Step (mapped by the adapter).
type Step struct {
	Key   string
	Input string
}

// Mock is one simulated MCP/resource response. Resource is matched against a
// step's Input; Response is the synthetic text returned for a hit. No secrets.
type Mock struct {
	Resource string
	Response string
}

// StepOutput is the runner's result for one step. MockHit reports whether the
// step resolved against a mock (vs a deterministic mock-miss).
type StepOutput struct {
	Key     string
	Output  string
	MockHit bool
}

// Probe is a single red-team payload delivered to the governed target over the
// gated egress path. It carries ONLY what the runtime needs to deliver it; the
// judgement lives in the red-team module (the composition-root adapter calls
// redteam.Judge on the captured response). Surface selects how the payload is
// delivered ("input" | "tool" | "output"); the runtime treats them as request
// shapes, never as an instruction to reach anything but the target.
type Probe struct {
	ID      string
	Surface string
	Payload string
}

// EgressRule is one allowed destination on a job's egress allowlist: an exact
// host, or a CIDR, optionally narrowed to a set of ports (empty ⇒ any port).
// Host and CIDR are mutually exclusive per rule; the proxy matches a connection's
// destination against every rule and permits it only on a hit.
type EgressRule struct {
	Host  string // exact hostname or IP literal (e.g. "agent.client.internal", "10.1.2.3")
	CIDR  string // network range (e.g. "10.1.2.0/24"); empty when Host is set
	Ports []int  // allowed ports; empty ⇒ any port on a host/CIDR hit
}

// EgressPolicy is the deny-by-default scope of a job's network egress, bound to
// an engagement (a tenant + run/target id) so the proxy log is attributable. An
// EMPTY Allow slice means TOTAL DENY (the synthetic-scenario default): no
// destination is reachable. A red-team job scopes Allow to EXACTLY the authorized
// target's host/CIDR — everything else is denied (defense in depth on top of the
// red-team module's consent gate, docs/SECURITY-HARDENING.md).
type EgressPolicy struct {
	Engagement string       // "<tenant>/<run-or-target-id>" — binds the egress log
	Allow      []EgressRule // empty ⇒ deny all
}

// denyAll reports whether the policy permits nothing (an empty allowlist).
func (p EgressPolicy) denyAll() bool { return len(p.Allow) == 0 }

// EgressEvent is one entry of the engagement-bound proxy log: a connection
// attempt and its verdict. It records the destination (host:port) and whether it
// was allowed — never a request body, header or secret (docs/SECURITY-HARDENING.md).
type EgressEvent struct {
	Engagement string
	Host       string
	Port       int
	Allowed    bool
	Reason     string
	At         time.Time
}

// Job is the COMPLETE, neutral input to one isolated run — the only thing a
// backend sees. It carries no store, secret or module type by construction.
//
//   - A SYNTHETIC SCENARIO job carries Steps + Mocks and an empty Egress
//     allowlist (deny all): the instance resolves steps against mocks with no
//     network reachable.
//   - A RED-TEAM job additionally carries a Probe + a Target endpoint and an
//     Egress allowlist scoped to EXACTLY that target: the instance delivers the
//     probe to the target over the gated path and captures the response.
type Job struct {
	Tenant string // business tenant id (string form; this package never pins a tenant)
	RunID  string // attestation / engagement binding (a run or target id)
	Prefer string // optional backend preference ("gvisor" | "firecracker"); "" ⇒ policy order

	Steps []Step // ordered synthetic inputs (scenario / replay)
	Mocks []Mock // synthetic resource responses

	Probe   *Probe       // non-nil ⇒ a red-team delivery is requested
	Target  string       // the authorized target endpoint (opaque handle; red-team only)
	Egress  EgressPolicy // the deny-by-default network scope
	Timeout time.Duration
}

// Result is the neutral outcome of a run plus the isolation Attestation. The
// composition-root adapter maps it back onto the module's own outcome type.
type Result struct {
	Steps       []StepOutput // per-step outputs (synthetic / mock-resolved)
	Response    string       // the target's response (red-team only; "" otherwise)
	Reached     bool         // red-team: whether the target was actually reached over the gate
	EgressLog   []EgressEvent
	Attestation Attestation
}

// Attestation is the per-run, auditable proof of HOW the run was isolated. Every
// field reflects the REAL backend — a degraded/portable backend is visible, never
// hidden (docs/contracts: "se ve el backend real, sin fingir microVM").
type Attestation struct {
	Backend           string // the real backend name ("gvisor" | "firecracker" | ...)
	Isolated          bool   // the backend's real isolation guarantee
	InstanceID        string // the ephemeral instance id (non-sensitive)
	ReadonlyRoot      bool
	TmpfsOnly         bool
	CapsDropped       bool
	NoNewPrivs        bool
	Seccomp           string // the pinned seccomp profile name ("" ⇒ none)
	NoNIC             bool   // the instance had no network interface of its own
	EgressDenyDefault bool   // the egress proxy denied by default
	EgressAllowed     int    // number of allowlist rules in effect (0 ⇒ total deny)
	EgressDenied      int    // number of egress attempts the proxy DENIED (off-allowlist)
	Destroyed         bool   // the ephemeral state was discarded
	DestroyVerified   bool   // destruction was VERIFIED, not assumed
	StartedAt         time.Time
	FinishedAt        time.Time
}
