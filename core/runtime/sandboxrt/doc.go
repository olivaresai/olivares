// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package sandboxrt is the real, pluggable, deny-closed ISOLATED EXECUTION
// RUNTIME behind two module seams: the testing-sandbox's Runner (module XVII,
// modules/sandbox) and the red-team's Sandbox (module XVIII, modules/redteam).
// It is the OS-level execution boundary the contract (docs/contracts/
//-sandbox.md §3.2/§3.3) and the RED LINE (docs/SECURITY-HARDENING.md) describe:
// an ephemeral, hardened, EGRESS-CONTROLLED instance, attested per run.
//
// WHY THIS LIVES IN THE AGPL MOTOR, NOT A CONNECTOR.
// ARCHITECTURE.md / docs/SECURITY-HARDENING.md draw a hard line: the read-first connectors DISCOVER
// (Apache-2.0, import only /sdk, never act). RUNNING UNTRUSTED CODE and OPENING
// A GOVERNED NETWORK PATH TO A TARGET are acting — the crown-jewel exception to
// the read-first norm — so they live behind the AGPL boundary, exactly like the
// deploy executor (core/runtime/executor). This package is self-contained: it
// imports ONLY the standard library — never core/model, never modules/sandbox or
// modules/redteam (which would invert the /core → /modules layering). It speaks
// NEUTRAL terms (Job, Result, Attestation, EgressPolicy). The thin seam adapters
// that satisfy sandbox.Runner and redteam.Sandbox by translating the modules'
// own types into a Job (and a Result back) live in the composition root
// (cmd/olivares/sandboxrt.go), the ONLY layer that imports both the modules
// and this package — mirroring deployexec.go.
//
// THE ISOLATION BAR (ARCHITECTURE.md, README.md, the brief). Every run is an
// instance that is, by DEFENSE IN DEPTH (not a single frontier):
//
//   - EPHEMERAL & FRESH — a new instance per run; its state is discarded and the
//     destruction is VERIFIED (not assumed) before the run is reported destroyed.
//   - READ-ONLY ROOT + TMPFS — the code/rootfs is mounted read-only; only a
//     size-bounded tmpfs is writable, so nothing persists past the instance.
//   - LEAST PRIVILEGE — cap-drop ALL, no-new-privileges, a non-root uid, a pinned
//     deny-by-default SECCOMP profile, and pid/mem/cpu bounds.
//   - NO NIC INSIDE — the instance has no network interface of its own; its ONLY
//     egress is an out-of-process proxy the engine owns (proxy.go).
//   - EGRESS DENY-BY-DEFAULT — the proxy refuses every destination not on the
//     job's allowlist (empty allowlist ⇒ TOTAL deny), fail-closed, and records an
//     engagement-bound log of every allowed/denied connection.
//
// HONEST, DENY-CLOSED, CAPABILITY-GATED. The isolation primitives (gVisor/runsc,
// Firecracker + KVM) are NOT assumed present: each Backend runs a real Preflight
// and FAILS CLOSED when its primitive is absent or not functioning — the engine
// then refuses to run rather than executing un-isolated (NEVER a faked microVM).
// Backends are selected by POLICY (the configured order), never hardcoded. The
// composition root wires the engine ONLY when an operator provisions it
// (OLIVARES_SANDBOX_RUNTIME_CONFIG); un-wired, the modules keep their honest
// defaults (the in-proc-mock runner / the offline sandbox), so a default
// deployment is fully functional and never degraded by an absent host primitive.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md). A Result carries the per-step outputs and (for a
// red-team probe) the target's response so the COMPOSITION-ROOT adapter can judge
// and then HASH/SCRUB before anything is persisted — this package never persists,
// never logs a payload, and never returns a secret. The egress log records
// destinations and verdicts (host:port + allow/deny), never bodies.
package sandboxrt
