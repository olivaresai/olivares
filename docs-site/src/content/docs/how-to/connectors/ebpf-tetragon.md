---
title: "eBPF / Tetragon (the kernel backstop)"
description: >-
  Wire the non-cooperative half of the access map: Tetragon captures kernel
  file and network events outside the agent's control, and the connector turns
  its JSON export into honestly-approximate access edges — plus an opt-in
  anti-evasion detector.
sidebar:
  order: 3
---

The `ebpf` source is the **anti-evasion half** of the R/RW map. Where the
cooperative path sees what an agent *reports*, this sees what the kernel
*did* — file reads/writes and outbound connections — even when an agent
disables its own telemetry, because it runs **outside the agent's control**.

Two design decisions define it, and both are the security posture:

- **It does not load eBPF programs itself.** [Tetragon](https://tetragon.io)
  does the kernel capture, deployed as a separate hardened service holding
  `CAP_BPF` + `CAP_PERFMON`. The connector is a **zero-capability, read-only
  consumer** of Tetragon's JSON event export (a shared file/FIFO, mode
  `0600`, or stdin).
- **It is blind to TLS bodies and payloads.** It observes access
  relationships — never content.

The repository ships the reference deployment under `connectors/ebpf/deploy/`:
a hardened Tetragon DaemonSet, the two TracingPolicies (file access, network),
and a Compose variant for single hosts.

## What it emits

| Field | Value |
|---|---|
| Signal source | `ebpf` |
| Mode | file `read` / `write`, network connect edges |
| Origin | a **runtime identity** (process/container) — kind `identity`, never a resolved agent |
| Confidence | **always `approximate`** — see below |
| Coverage tier | kernel backstop |

The `approximate` is precise, not modest: the *access* is kernel ground truth
(the syscall happened); what the kernel cannot give is the *agent* — it knows
the process and cgroup, not which governed agent that was. The access-map
module upgrades attribution when an identity source binds the runtime
identity to an agent.

## 1. Deploy Tetragon (the sensor)

On Kubernetes, apply the shipped DaemonSet and TracingPolicies:

```bash
kubectl apply -f connectors/ebpf/deploy/tetragon-daemonset.yaml
kubectl apply -f connectors/ebpf/deploy/tracingpolicy-file-access.yaml
kubectl apply -f connectors/ebpf/deploy/tracingpolicy-network.yaml
```

Tetragon writes its JSON export to the shared volume
(`/var/run/olivares/tetragon.log`); the connector reads it from the other
side. On a single host, `connectors/ebpf/deploy/docker-compose.yaml` is the
same split without Kubernetes. The full architecture and hardening notes are
in `connectors/ebpf/deploy/README.md`.

## 2. Declare the source

```json
{
  "sources": [{
    "name": "node-kernel-backstop",
    "kind": "ebpf",
    "tenant": "<tenant-id>",
    "config": {
      "events_path": "/var/run/olivares/tetragon.log",
      "detect_evasion": "true"
    }
  }]
}
```

| Key | Default | Meaning |
|---|---|---|
| `events_path` | `-` (stdin) | Tetragon JSON event stream — file, FIFO, or stdin |
| `follow` | `true` | keep reading as the stream grows |
| `detect_evasion` | `false` | opt-in: flag a known agent process whose cooperative telemetry goes silent while the kernel still sees it acting |
| `evasion_window` | `5m` | grace period before a missing cooperative connection is flagged |
| `agent_signatures` | `claude,claude-code` | executable names classified as cooperative agents for the detector |
| `otlp_endpoints` | `127.0.0.1:4317,127.0.0.1:4318` | the cooperative-telemetry endpoints whose connections the detector correlates |

The connector consumes Tetragon `ProcessKprobe` events (file operations and
network connects) and `ProcessExit` (detector state); `ProcessExec` is used
for attribution context and never emitted as an edge.

## 3. What you'll see in the console

Kernel edges join the access map attributed to runtime identities, always
marked `approximate`. The detector's output lands in **Security** as
findings — a session that stops emitting while the kernel still sees activity
is exactly the case this source exists for:

<img class="light:sl-hidden" src="/console/security-dark.png" alt="The Security view listing findings from the estate's detective sources." />
<img class="dark:sl-hidden" src="/console/security-light.png" alt="The Security view listing findings from the estate's detective sources." />

## Honest limits

- **Its end-to-end attribution depth is still being proven out.** The
  cooperative path and store-native audit are the verified, high-fidelity
  signals; treat the kernel backstop as a floor-raiser, not a finished
  primary source ([Honesty & limits](/start/honesty-and-limits/)).
- **Tetragon's scope is its TracingPolicies.** The shipped policies cover
  file access and network connects; what they do not trace does not exist in
  the export.
- **Process ≠ agent.** Without an identity binding, every kernel edge stays
  `approximate` — by design, not by accident.

## Related

- [Connect Claude Code](/how-to/connect-claude-code/) — the cooperative half
  this backstops.
- [SSO/SCIM & identity sources](/how-to/connectors/sso-scim-identity/) — how
  attribution gets upgraded.
- [Security hardening](/how-to/security-hardening/) — where the backstop fits
  in the defensive posture.
