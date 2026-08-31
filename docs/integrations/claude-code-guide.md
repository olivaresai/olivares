<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Integrate Claude Code with Olivares AI

> Editorial draft for the public documentation. Pending screenshots are explicitly marked so
> the publishing team can produce them from the demonstration estate.

This integration brings Claude Code under the governance control plane without making Olivares AI
a mandatory proxy. The `claude` connector receives OTLP telemetry and hook events, correlates
sessions, and records R/RW access, costs, and findings. When preventive control is required, the
managed `olivares claude-hook` hook queries the local Olivares PEP before each tool use. These two
planes are independent: receiving telemetry does not mean that policy is being enforced.

## Add Claude Code

### Prerequisites

- An Olivares AI binary that includes the first-party `claude` connector.
- The UUID of the enterprise tenant to which observations will be attributed.
- Claude Code installed on the endpoints to be governed. The local receiver does not require an
  Anthropic API key.
- Local connectivity from Claude Code to the Olivares receiver. The defaults are
  `127.0.0.1:4317` for OTLP/gRPC and `127.0.0.1:4318` for OTLP/HTTP and cooperative hooks.
- An executable temporary path for the Olivares service. `claude` runs as an isolated plugin; on
  systems where `/tmp` is mounted with `noexec`, set `TMPDIR` in the service unit to a dedicated
  directory owned by the Olivares service account.

Do not expose the OTLP receivers or cooperative endpoint beyond loopback. They do not authenticate
the sender, so any host that can reach them could fabricate telemetry. The governed PEP is a
separate surface: it uses its own local socket, authenticates every request, and records each
decision.

1. Open **Control console** (`/console`) and select the **Connectors** tab. The connector roster is
   global: a superadmin account is required, and saving, testing, and reloading require AAL3
   elevation.
2. Add a source with type `claude`, a stable operational name such as `claude-code-prod`, the
   appropriate tenant, `live` mode, interval `0`, and enabled status. A zero interval is correct:
   this connector maintains receivers rather than polling in batches.
3. Save the source and select **Reload**. The row confirms its name, type, mode, and status. The
   console test action is unavailable for `claude` because it is an out-of-process connector;
   validation occurs on save, and the full open test uses `olivares sources test`, which launches
   the plugin.

[CAPTURA: creation of `claude-code-prod` in Control console > Connectors, with type `claude`, mode `live`, interval 0, and enabled status; light and dark variants of the seeded estate.]

## Configure Claude Code

Distribute two configurations together: the observation source and the managed agent policy.

### 1. Receiver and data minimization

The secure initial configuration is the default:

| Source setting | Initial value | Effect |
|---|---:|---|
| `enable_grpc` | `true` | Serves OTLP/gRPC on `grpc_addr` (`127.0.0.1:4317`). |
| `enable_http` | `true` | Serves OTLP/HTTP and the cooperative hook on `http_addr` (`127.0.0.1:4318`). |
| `hook_path` | `/hooks` | Cooperative hook path within the HTTP receiver. |
| `content_capture` | empty | Preserves structure, but not prompts, tool bodies, or API bodies. Extended reasoning is always redacted. |
| `enforcement` | empty | Observes hooks; this source does not return preventive decisions. |
| `allow_public_bind` | `false` | Rejects binding outside loopback. |

If a host runs multiple OTLP receivers, assign each one a different loopback address and use the
same value in the agent configuration. Claude, Codex, and Grok use `4318` as a default in some
modes and cannot bind the same socket at the same time.

### 2. Managed settings and the governed PEP

Generate the system-level Claude Code file with the Olivares binary:

```sh
olivares agent managed-settings \
  --otel-endpoint http://127.0.0.1:4317 \
  --out /etc/claude-code/managed-settings.json
```

The generator installs `allowManagedHooksOnly: true`, a `PreToolUse` hook that runs
`olivares claude-hook`, and the `PostToolUse` redaction hook. It also enables OTLP with the `grpc`
protocol, so the endpoint above uses receiver `4317`, not HTTP receiver `4318`. The file belongs
in the managed system layer, not in the session `HOME`.

The PEP server is enabled when Olivares starts with a file specified by
`OLIVARES_HOOK_PEP_CONFIG`. The following is a valid example policy for one tenant:

```json
{
  "listen": "127.0.0.1:8447",
  "tenants": [
    {
      "tenant": "11111111-1111-4111-8111-111111111111",
      "require_firm_identity": true,
      "enforcement": "enforce",
      "policy": {
        "version": "claude-prod-v1",
        "default": "allow",
        "rules": [
          {
            "tool": "Bash",
            "decision": "ask",
            "reason": "Shell commands require human confirmation"
          }
        ]
      }
    }
  ]
}
```

Sessions launched by Olivares receive ephemeral values for `OLIVARES_HOOK_PEP_URL`,
`OLIVARES_HOOK_PEP_TOKEN`, `OLIVARES_HOOK_PEP_TENANT`, and the agent attribution. For an
independently launched session, the operator must supply those values through the secrets channel;
do not write them to `managed-settings.json`. If the endpoint is missing or unavailable,
`olivares claude-hook` returns `deny`.

For an initial non-blocking rollout, use `observe` mode with a future RFC3339 `observe_until`.
This allowance is temporary: a missing, invalid, or expired timestamp resolves to `enforce`.
Platform invariants—including identity, tenant, kill switch, firewall, and fail-closed errors—remain
enforced while business rules are being observed.

[CAPTURA: Claude Code configuration showing the loopback OTLP receiver, structural content capture, and managed settings with the managed hook; light and dark variants, with no secrets visible.]

## CLI usage

The following output excerpts were measured with the binary built from this worktree on
August 30, 2026. General engine startup messages are omitted.

### Register the source

With SQLite, stop the engine before changing the roster from the CLI because it uses a
single-writer profile. With PostgreSQL, the operation can run alongside the engine. Use the
console for live changes to SQLite.

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name claude-code-prod \
  --kind claude \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 0 \
  --config mode=live \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "claude-code-prod" (kind "claude", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → claude
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 0
  enabled: - → true
  config.mode: - → live
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

`--actor` and `--reason` are required because this change alters data provenance and is recorded
in the audit ledger.

### Validate and open the connector

```sh
olivares sources validate \
  --data-dir /var/lib/olivares \
  --name claude-code-prod

olivares sources test \
  --data-dir /var/lib/olivares \
  --name claude-code-prod \
  --timeout 20s
```

```text
source "claude-code-prod"
configuration: VALID (everything that can be decided without the network)
  ? not checked here: the "claude" connector runs out-of-process, so its connector identity is only known once the binary is launched (`olivares sources test` launches it)
configuration: VALID (everything that can be decided without the network)
  ? not checked here: the "claude" connector runs out-of-process, so its connector identity is only known once the binary is launched (`olivares sources test` launches it)
source "claude-code-prod" (claude): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

`validate` does not open sockets. `test` calls `Open` and `Close`, but does not call `Gather`,
connect the source to the engine, or prove that Claude Code is sending telemetry. If the plugin
fails with `permission denied` despite having its executable bit set, check whether the process
`TMPDIR` is on a `noexec` volume.

### Confirm fail-closed hook behavior

With the endpoint deliberately left unconfigured, the client returns a denial in the format
expected by Claude Code:

```sh
printf '%s\n' '{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' | olivares claude-hook
```

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"governed PEP endpoint not configured (deny-closed)"}}
```

This probe checks the local client, not a remote policy decision. In production, also test an
allowed rule, a denied rule, and an `ask` request with firm identity before expanding the rollout.

## Control console

Adding a source does not create historical data. After reloading the roster and receiving the
first event, operators can use the following views:

| Location | What it shows | How to interpret the state |
|---|---|---|
| **Control console > Connectors** (`/console`) | Name, `claude` type, mode, non-secret configuration, roster status, and save/reload actions. | “Saved” proves persistence. It does not prove that an event has arrived. |
| **Health > Connectors** (`/health`) | Connector health, operational message, trend, and latest known poll or activity. | An open receiver can be healthy while the agent remains silent. |
| **Observability > Ingestion** (`/observability`) | Records by source, `edge`, `cost`, and `finding` types, signal, and first/last event. | These are process-wide counters since startup; they reset on restart and are not tenant-specific. |
| **Sessions** (`/sessions`) | Session, state, action, model, tokens, cost, latest activity, and `enforced` or `observed` posture. | The posture summarizes event evidence; it is not inferred from registering the connector. |
| **Access map** (`/access-map`) | R/RW edges attributed from observed tools, MCP, and resources. | An observed edge describes activity; it is not equivalent to prior authorization. |
| **Cost & FinOps** (`/finops`) | Cost and token samples derived from received telemetry. | Coverage is limited to what the fleet exports; calls that never emitted OTLP cannot be reconstructed. |
| **Security** (`/security`) | Telemetry gaps, sandbox/MCP posture, and other emitted findings. | An absent finding does not make an unobserved surface compliant. |
| **Claude Policy** (`/claude-policy`) | Authoring, distribution, versions, and check-in status for managed Claude Code surfaces. | Distribution and drift verification are separate facts and are shown separately. |

[CAPTURA: an active Claude Code session in `/sessions`, showing posture, timeline, and links to Access map, FinOps, and Security; light and dark variants of the seeded estate.]

## Production use

- **Phased rollout:** Start with structural content and rules in observed mode with an expiration
  date. Review false positives, then promote each tenant to `enforce`.
- **Fleet administration:** Distribute `/etc/claude-code/managed-settings.json` through an RPM,
  immutable image, Ansible, Salt, or the equivalent enterprise configuration manager. Check the
  live file with a second `managed-settings` source to detect absence or drift.
- **Separation of duties:** The platform team maintains receivers and availability; the security
  team versions rules; tenant owners review `ask` requests and findings. Every privileged change
  remains attributable.
- **Data minimization:** Keep `content_capture` empty unless there is an approved forensic need
  with defined residency and retention. Structural data is usually sufficient for adoption and
  cost analysis.
- **Hardened hosts:** Keep receivers on loopback, provide the plugin with a minimal executable
  temporary directory, and make the policy read-only. Do not relax `noexec` globally to make the
  connector start.

## What is enforced and what is only observed

| Surface | Actual behavior |
|---|---|
| OTLP telemetry and the cooperative hook from the `claude` connector | **Observed.** The sender cooperates; the loopback receiver does not authenticate, and a local process can omit or fabricate a signal. |
| Empty `enforcement` setting on the source | **Observed.** This is the default and does not block tools. |
| `olivares claude-hook` + PEP + managed settings | **Enforces** `allow`, `ask`, or `deny` for events that Claude Code can veto, and records the decision. Endpoint failures deny closed. |
| `allowManagedHooksOnly` in the managed layer | **Hardens the installation** against user or project hooks that could compete with the PEP. |
| `PostToolUse` | **Observes and redacts after the action.** It cannot undo effects already produced by the tool. |
| Actions outside the Claude Code process and hook | **Not covered by this wiring.** Use operating-system controls, native auditing, and network policy as backstops. |

Operational verification requires four separate checks: a persisted roster, an opened connector,
an event visible in **Ingestion**, and a tool actually blocked by the PEP. None of these checks
substitutes for the other three.
