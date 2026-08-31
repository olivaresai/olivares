<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: Apache-2.0
-->

# Deploying the eBPF / Tetragon backstop

The `olivares.ebpf` connector is the **non-cooperative, kernel-level backstop** of
the R/RW access map (see `ARCHITECTURE.md` and `docs/SECURITY-HARDENING.md`). It sees what the kernel actually did
— file reads/writes and outbound connections — even when an agent disables its own
telemetry, because it runs **outside the agent's control**.

It does **not** load eBPF programs itself. [Tetragon](https://tetragon.io) does the
kernel capture; this connector **consumes Tetragon's JSON event export** and emits
normalized access edges through the SDK. That split is deliberate and is the
security posture below.

## Architecture (the privilege split)

```
  ┌─ host / node ─────────────────────────────────────────────┐
  │                                                            │
  │  Tetragon (sensor)            Olivares eBPF connector      │
  │  CAP_BPF + CAP_PERFMON   ──▶  NO capabilities              │
  │  root allowed by examples     non-root, read-only rootfs   │
  │  loads BPF, writes export     reads shared export file     │
  │           │                          │                     │
  │           └──── shared volume ───────┘                     │
  │              /var/run/olivares/tetragon.log                │
  └────────────────────────────────────────────┬──────────────┘
                                                │ SDK Sink (push, mTLS)
                                                ▼  core engine → access map / security
```

- **Tetragon (the sensor)** holds the kernel capabilities. The minimum is
  **`CAP_BPF` + `CAP_PERFMON`** on kernels ≥ 5.8 (the capability split). On older
  kernels Tetragon needs `CAP_SYS_ADMIN` instead — verify on your kernel. The
  Kubernetes example permits root and uses seccomp `RuntimeDefault`; the Compose
  example does not set a user and explicitly uses `seccomp=unconfined`. Both set
  CPU/memory limits and expose no inbound listener. Treat the sensor as the
  privileged side of the boundary and tighten it only after validating Tetragon on
  your kernel. See `tetragon-daemonset.yaml` / `docker-compose.yaml`.
- **The Olivares connector** holds **no kernel capabilities at all**. It drops
  `ALL` caps, runs non-root with a read-only root filesystem, and only **reads** the
  export file Tetragon owns.

### Threat model (honest framing)

The connector needs no privilege, but the **data** it consumes — unfiltered kernel
events across the host (process, network and file metadata of every workload) — is
sensitive. A compromised connector is a metadata-disclosure risk, not a privilege
escalation. Therefore:

- Keep the export volume **private to the two containers**; do not mount it anywhere
  else. The example manifests do not enforce file/directory modes, so set and verify
  ownership and permissions in the deployment environment.
- Run the connector **isolated** (own user, no listener, read-only rootfs).
- Tetragon hardening is a **prerequisite, not a replacement**, for connector
  isolation.

## TracingPolicies (the connector's input)

Apply the two policies so Tetragon emits the events the connector parses:

| Policy | Hook | Produces | Connector mapping |
|---|---|---|---|
| `tracingpolicy-file-access.yaml` | `security_file_permission(file, mask)` | `process_kprobe` with `file_arg{path}` + `int_arg=mask` | `file.path` edge, R/RW from the MAY_* mask |
| `tracingpolicy-network.yaml` | `tcp_connect(sock)` | `process_kprobe` with `sock_arg` 5-tuple | `net.endpoint` edge (`tcp://ip:port`) |

A host-wide file hook is high-volume — **scope the policies** (selectors on path /
binary / mask) before production, and validate them against your kernel. The
connector classifies events by **argument shape**, so minor kprobe-naming
differences across Tetragon/kernel versions do not break ingestion. **Minimum
Tetragon: v1.0.**

### SNI (optional enrichment — not native to Tetragon)

Tetragon has **no first-class SNI field**. Network edges are `tcp://<dst-ip>:<port>`
by default. If you want the cleartext ClientHello hostname (`tcp://api.example.com:443`),
deploy a policy/uprobe that emits the SNI as a **`string_arg`** on the connect
event; the connector uses the first `string_arg` when present (see
`connectors/ebpf/network.go`). The connector is always **blind to the TLS body** —
it captures the 5-tuple and, when provided, the cleartext SNI hostname, never any
payload. Treat SNI as recon-relevant: it is gated by the same RBAC as every other
edge.

## Configuration

The connector receives its settings from the engine as `sdk.Config` (over the
go-plugin channel), not from the environment:

| Setting | Default | Meaning |
|---|---|---|
| `events_path` | `-` | Tetragon JSON export file/FIFO to read, or `-` for stdin |
| `follow` | `true` | tail the export (false reads once to EOF) |
| `detect_evasion` | `false` | emit anti-evasion findings (heuristic — see below) |
| `evasion_window` | `5m` | grace before a missing cooperative connection is flagged |
| `agent_signatures` | `claude,claude-code` | executable/argv tokens that mark a cooperative agent |
| `otlp_endpoints` | `127.0.0.1:4317,127.0.0.1:4318` | cooperative-telemetry endpoints |

### Anti-evasion detection — off by default, heuristic

`detect_evasion` flags an agent-classified workload that acts at the kernel but is
never seen connecting to a configured `otlp_endpoints` within `evasion_window`. It
is a **Low-severity heuristic, not proof**, and is **off by default** because it
false-positives on remote/BYO collectors, batching exporters, startup lag and
air-gapped deployments. Set `otlp_endpoints` to your real collector addresses
before enabling. Final correlation/forensics is the security module's job.

## What it emits (for the access map & security module)

Authoritative contract is the package doc (`go doc github.com/olivaresai/olivares/connectors/ebpf`).
Summary:

- **`file.path` edge** — `OriginKind=identity`, `OriginRef=<workload>/<exe>`,
  `Mode` from MAY_READ/MAY_WRITE/MAY_APPEND/MAY_EXEC, `Source=ebpf`,
  `Confidence=approximate`, `ToolRef=security_file_permission`.
- **`net.endpoint` edge** — same origin, `ResourceRef=tcp://<sni-or-ip>:<port>`,
  `Mode=readwrite`, `ToolRef=tcp_connect`.
- **`anti_evasion` finding** (when enabled) — `Severity=low`, subject = the workload
  identity.

`OriginKind` is **`identity`**, never `agent`: the kernel attributes to a
process/cgroup, and `Confidence=approximate` reflects that the **agent** attribution
is pending (the access map upgrades it once a resolved agent identity is available).
The access itself is kernel ground-truth.
