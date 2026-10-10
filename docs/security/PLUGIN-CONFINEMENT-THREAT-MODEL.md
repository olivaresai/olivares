<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Plugin runtime confinement — threat model

**Scope of the claim: _signed trusted-operator plugin confinement_ — NOT a "safe
marketplace sandbox".** External (third-party) connector plugins run as
operator-admitted, signed, digest-pinned binaries. Admission (Sigstore/DSSE over the
operator's trust anchors + a checksum check of the original path before launch)
checks the admitted artifact;
confinement bounds *what a running plugin can reach*. This document states exactly
what the confinement contains and — just as importantly — what it does not, so the
attestation each launch emits can be read against a written contract instead of an
implied guarantee.

## What runs, and the trust model

A plugin is a separate process the engine launches and talks to over an AutoMTLS
gRPC channel on loopback (hashicorp go-plugin). It is admitted only when the
operator pinned its digest and its signature verifies against the operator's trust
policy (`cmd/olivares/externalplugins.go`). The loader uses go-plugin's checksum
check on the original plugin path before a helper-wrapped launch; unwrapped
launches keep go-plugin's ordinary check. The existing hash-then-exec window
remains: a later path replacement is not sealed out. The operator has already
decided to trust this code. Confinement is **defense in depth on top of that trust**, to bound
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
| C1 | Plugin reads secrets from its environment | `ScopedEnv` plus go-plugin `SkipHostEnv`; only PATH, owned scratch TMPDIR and explicit extras are forwarded. | **Applied.** No engine environment is inherited. Default Landlock grants no `/proc`, but an unprivileged engine still shares its UID with the child; same-UID process-memory access is not a generally enforced boundary without UID isolation. Unsupported Landlock leaves filesystem access at that UID. The attestation keeps this degradation explicit. |
| C2 | Plugin exhausts host CPU / memory / PIDs | A per-plugin **cgroup v2** with `memory.max`, `pids.max`, `cpu.max`; the whole cgroup is killed on teardown (`cgroup.kill`). | **Applied when delegated.** Each ceiling is written and **read back**; a ceiling the host did not delegate (controller absent from `subtree_control`) is recorded degraded and NOT asserted. `att.Cgroup` is true only when a fork-bomb/OOM guard is verified in effect. |
| C3 | Plugin runs with engine privileges / escalates | Dedicated per-launch non-root UID/GID and empty supplementary groups when the engine is root; shared helper sets `no_new_privs`. | **UID drop applied when privileged; no_new_privs applied with supported-Linux Landlock before exec and recorded after handshake.** The bounding capability set is **not cleared**; `CapsDropped` stays false. |
| C4 | Plugin reads/writes ungranted host files | Shared core Landlock mechanism, with explicit SDK policy rather than session defaults. | **Applied on supported Linux kernels and recorded after successful handshake.** A restriction failure refuses exec; an unsupported/disabled kernel is explicitly degraded. The policy below grants only named roots and devices. |
| C5 | Plugin issues dangerous syscalls | Requested deny-by-default seccomp filter. | **Not implemented.** `Seccomp` stays false and the request is recorded degraded. Landlock/no_new_privs do not replace seccomp or generally deny ptrace. |
| C6 | A hung/looping plugin stalls the host | Resource kills via the C2 cgroup guards; a post-handshake health/kill budget with a classified reason. | **Partial.** The cgroup OOM/pids guards (when effective) are the real resource kill, and go-plugin's start timeout bounds a hung launch. An **active post-handshake health-timeout** kill is a **follow-up**, recorded degraded. |

**SDK filesystem policy.** Read/execute grants are the plugin's logical and
canonical directory (including sibling resources), plus caller-supplied
`ReadableRoots`. Static ELF adds no runtime roots. A shebang/dynamic ELF adds its
selected interpreter and canonical target, `/etc/ld.so.cache` and matching ABI
library directories: amd64 `/lib/x86_64-linux-gnu`, `/usr/lib/x86_64-linux-gnu`,
`/lib64`, `/usr/lib64`; arm64 uses the corresponding `aarch64-linux-gnu` paths.
Custom runtimes need explicit `ReadableRoots`. Parent inspection precedes UID
drop; the child need not read an engine-readable 0711 executable.

DNS grants name `/etc/resolv.conf`, `/etc/hosts`, `/etc/nsswitch.conf`. HTTPS gets
the first existing Go Linux CA bundle: `/etc/ssl/certs/ca-certificates.crt`,
`/etc/pki/tls/certs/ca-bundle.crt`, `/etc/ssl/ca-bundle.pem`,
`/etc/pki/tls/cacert.pem`, `/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem`,
`/etc/ssl/cert.pem`, in that order. Canonical targets are granted, not their
whole parent directories or private keys. Custom trust roots are explicit.

Only per-plugin owned scratch is writable; there is no global `/tmp`, home,
`/dev/shm`, `/etc`, `/opt`, `/usr` or `/proc` grant. Devices are `/dev/null`
read/write and `/dev/urandom` read, without execute/ioctl grants. An operator who
explicitly grants a broader root authorizes its contents; this policy does not
invent a private-key carve-out inside such a grant. Session roots/devices/limits
remain their separate caller policy, byte-for-byte unchanged.

**Attestation grades.** `strong` requires uid, cleared bounding capabilities,
no_new_privs, cgroup, seccomp and Landlock. Bounding-capability clearance and
seccomp are absent, so **Strong is unreachable**; the ceiling is `partial`.
An active health-timeout monitor is also absent. These three are separate open
causes; no request field implies that its mechanism is enforced. Landlock and
no_new_privs are not asserted at preparation time or after a failed handshake.

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
  the one-shot `sandboxrt` job runner uses) would break. In this release, egress for the
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

Every successful plugin handshake emits an isolation attestation recording the **real** level
achieved: which of C1–C6 applied, the platform, and an overall confinement level
(`partial` when an OS control applied, `minimal` without those controls; `strong`
is currently unreachable). A control
that could not be applied is reported as not-applied — never asserted. This is the
evidence an enterprise buyer reads; a claim without it is unverifiable.

## Coordination

The public capability wording must match this scope: "signed trusted-operator plugin
confinement with a per-launch isolation attestation", not "sandboxed marketplace". The
reusable isolation gate is inherited by the external output-plugin composition, so
third-party output plugins are confined identically.
