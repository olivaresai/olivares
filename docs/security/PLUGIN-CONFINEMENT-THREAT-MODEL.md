<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Plugin runtime confinement — threat model

**Scope of the claim: _signed trusted-operator plugin confinement_ — NOT a "safe
marketplace sandbox".** External (third-party) connector plugins run as
operator-admitted, signed, digest-pinned binaries. Admission (Sigstore/DSSE over the
operator's trust anchors + a checksum re-pin at exec time) proves *what* runs;
confinement bounds *what a running plugin can reach*. This document states exactly
what the confinement contains and — just as importantly — what it does not, so the
attestation each launch emits can be read against a written contract instead of an
implied guarantee.

## What runs, and the trust model

A plugin is a separate process the engine launches and talks to over an AutoMTLS
gRPC channel on loopback (hashicorp go-plugin). It is admitted only when the
operator pinned its digest and its signature verifies against the operator's trust
policy (`cmd/olivares/externalplugins.go`), and go-plugin re-hashes the binary
immediately before exec (the TOCTOU pin). So the operator has already decided to
trust this code. Confinement is **defense in depth on top of that trust**, to bound
the blast radius of a plugin that is buggy, compromised after admission, or
over-curious — it is not a mechanism for safely running arbitrary untrusted code.

## Assets the confinement protects

1. **Secrets of the host and of OTHER connectors.** The control-plane process holds
   every configured connector's resolved credentials plus KMS tokens and signing
   keys in its environment and memory. A plugin must not be able to read another
   connector's material or the host's signing keys.
2. **The host filesystem.** A plugin must not read arbitrary host files (other
   tenants' data dirs, `/etc`, key files) or write outside a bounded scratch area.
3. **Host resources.** A plugin must not exhaust CPU, memory, or PIDs and take the
   control plane down (a fork bomb, a memory balloon, a busy loop).
4. **The host process identity.** A plugin must not run with the engine's UID or
   escalate privileges (setuid binaries, new capabilities).
5. **Undeclared network egress.** A plugin's network reach should be bounded to what
   it needs (its data source + the loopback control channel), not the whole network.

## What the confinement CONTAINS (in scope)

The **Status** column is the honest, per-control implementation state this release. A
control marked _follow-up_ is recorded as degraded in every attestation — never asserted.

| # | Threat | Control | Status (this release) |
|---|--------|---------|-----------------------|
| C1 | Plugin reads host/other-connector secrets from its **environment** | The plugin does NOT inherit the engine environment (`SkipHostEnv`); it gets only an explicit, minimal, allowlisted env. Its own config travels over gRPC (`conn.Open`), never env. | **Applied.** BUT effective **only with C3**: a *same-UID* plugin can still read the engine's secrets from `/proc/<engine-pid>/environ` and `/proc/<engine-pid>/mem`. The dedicated non-root UID (C3) makes those (mode 0400, owned by the engine UID) unreadable. When C3 cannot apply (unprivileged engine), the attestation records `env-scoping bypassable` and `level: minimal` — never a hidden gap. |
| C2 | Plugin exhausts host CPU / memory / PIDs | A per-plugin **cgroup v2** with `memory.max`, `pids.max`, `cpu.max`; the whole cgroup is killed on teardown (`cgroup.kill`). | **Applied when delegated.** Each ceiling is written and **read back**; a ceiling the host did not delegate (controller absent from `subtree_control`) is recorded degraded and NOT asserted. `att.Cgroup` is true only when a fork-bomb/OOM guard is verified in effect. |
| C3 | Plugin runs with engine privileges / escalates | A dedicated, **per-launch non-root UID/GID** with all **supplementary groups dropped** (co-resident plugins get distinct UIDs, so cross-plugin `/proc`/ptrace is UID-blocked). | **UID drop applied (when engine is root).** The stronger claim — bounding-set cleared + **no-new-privs** so a setuid/setcap binary cannot regain privilege — is a **follow-up** (re-exec launcher); `CapsDropped` is therefore NOT asserted this release. |
| C4 | Plugin writes/reads arbitrary host filesystem | **landlock** read-only host-fs restriction (Linux ≥ 5.13). | **Follow-up — NOT applied this release.** Always recorded degraded; `att.Landlock` is never true yet. |
| C5 | Plugin issues dangerous syscalls (ptrace, mount, kexec, bpf, module load) | A **deny-by-default seccomp** allowlist (enough to run + reach the loopback channel). | **Follow-up — NOT applied this release.** Always recorded degraded; `att.Seccomp` is never true yet. (Note: until seccomp lands, ptrace is not denied, so the C3 per-UID isolation is the only cross-plugin memory-read barrier.) |
| C6 | A hung/looping plugin stalls the host | Resource kills via the C2 cgroup guards; a post-handshake health/kill budget with a classified reason. | **Partial.** The cgroup OOM/pids guards (when effective) are the real resource kill, and go-plugin's start timeout bounds a hung launch. An **active post-handshake health-timeout** kill is a **follow-up**, recorded degraded. |

**Attestation grades.** `strong` requires the full set (uid + caps + no-new-privs + cgroup
+ seccomp + landlock) and is therefore **not attainable until the re-exec launcher lands**
C4/C5/no-new-privs; the ceiling this release is `partial`. This is stated so no reader
mistakes an absent `strong` for a defect.

**Inherited stdin (known, low).** go-plugin sets the child's stdin to the engine's stdin;
plugjail does not (and cannot, via go-plugin) override it. A daemon's stdin is normally
`/dev/null`/a TTY, so this is low risk, but it is an inherited handle to audit — do not
feed the engine secrets on stdin while a plugin is loaded.

## What the confinement does NOT contain (explicitly out of scope)

- **Kernel 0-days and container/namespace escapes.** Confinement raises the cost of
  a breakout; it does not make the kernel invulnerable. A kernel exploit defeats it.
- **Side channels.** Timing, cache, and resource-contention side channels between the
  plugin and the host are not addressed.
- **Full network-egress control for the long-lived control channel.** The plugin
  needs a loopback gRPC channel to the engine, which a "no-NIC" network namespace (as
  the one-shot `sandboxrt` job runner uses) would break. PRE-release, egress for the
  resident plugin process is **not** network-isolated to a declared allowlist; this is
  a **declared-degraded axis**, recorded as such in the attestation. The one-shot,
  no-NIC + egress-proxy model remains for batch sandbox jobs, not for the plugin RPC.
- **macOS / non-Linux hosts.** landlock, seccomp-BPF, cgroup v2 and Linux UID
  semantics are Linux primitives. On darwin the confinement **degrades honestly**:
  env scoping and the bounded lifecycle still apply, but the OS-level isolation
  controls do not. The attestation records the real, reduced level — it never claims
  parity.
- **A malicious binary that passed admission.** Confinement bounds blast radius; it is
  not a substitute for the operator's trust decision. The claim is "signed
  trusted-operator plugins", never "run any untrusted plugin safely".

## Attestation contract

Every plugin launch emits an isolation attestation recording the **real** level
achieved: which of C1–C6 applied, the platform, and an overall confinement level
(`strong` when the full Linux control set applied, `partial` when some primitive was
unavailable and degraded, `minimal` on a platform without the OS controls). A control
that could not be applied is reported as not-applied — never asserted. This is the
evidence an enterprise buyer reads; a claim without it is unverifiable.

## Coordination

The public capability wording must match this scope: "signed trusted-operator plugin
confinement with a per-launch isolation attestation", not "sandboxed marketplace". The
reusable isolation gate is inherited by the external output-plugin composition, so
third-party output plugins are confined identically.
