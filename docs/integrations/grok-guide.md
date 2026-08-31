<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Integrate Grok Build with Olivares AI

> Editorial draft for the public documentation. Marked screenshots will be produced from the
> demonstration estate and must not be replaced with mockups.

The `grok` integration governs **Grok Build, the terminal agent**, from the host where it runs. In
read-only mode, it reads the TOML configuration, sandbox profile, MCP server names, system
requirements, and the file that disables hooks. It can also receive OTLP traces. This is not the
xAI API connector: it does not query remote models and does not require a provider secret.
Preventive tool control uses `olivares grok-hook` and a separate local PEP.

## Add Grok Build

### Prerequisites

- Olivares AI and Grok Build installed on the same host, or the Grok configuration paths mounted
  read-only on the connector host.
- The UUID of the tenant to which the posture will be attributed.
- Permission for the Olivares service account to read `~/.grok/config.toml`,
  `/etc/grok/requirements.toml`, `~/.grok/disabled-hooks`, and, when configured, the compatible
  `managed-settings.json`.
- A superadmin account with AAL3 elevation if the source is created from the console.

Do not enter an xAI key for this source. It has no secret field and makes no inference API calls.

1. Open **Control console** (`/console`) and select the **Connectors** tab.
2. Add a source of type `grok` with the name `grok-demo`—or a stable host name—the tenant, a batch
   interval, and enabled status. `60` seconds provides visible posture changes during a pilot
   without turning local file reads into a continuous loop.
3. Save the source, select **Test**, and reload the roster. The row confirms the roster entry; the
   first subsequent `Gather` is what reads the files and emits findings.

[CAPTURA: creation of `grok-demo` in Control console > Connectors, with type `grok`, interval 60, local paths, and enabled status; light and dark variants of the seeded estate.]

## Configure Grok Build

### 1. Host inventory and requirements

| Source setting | Default | What it measures |
|---|---|---|
| `agent_ref` | `grok-build` | Stable reference included in findings. |
| `config_path` | `~/.grok/config.toml` | User-declared sandbox profile and MCP server names. |
| `requirements_path` | `/etc/grok/requirements.toml` | System layer that constrains the effective configuration. |
| `disabled_hooks_path` | `~/.grok/disabled-hooks` | User-disabled hook names, one per line. |
| `managed_settings_path` | empty | Claude Code-compatible `managed-settings.json` honored by Grok; empty means “not measured.” |
| `otlp_http` | `false` | Trace receiver, disabled until the operator reserves a port. |

On Linux, the minimum requirement for enforcing the sandbox is:

```toml
[sandbox]
profile = "strict"
```

Distribute this in `/etc/grok/requirements.toml` with administrative ownership. `strict` limits
writes to the workspace, `~/.grok/`, and temporary directories, and blocks network access under
the documented Linux guarantee. The same value in `~/.grok/config.toml` is only a user preference:
command-line options and the environment can affect the configuration, whereas
`requirements.toml` is the constraining layer.

To restrict MCP, declare in `requirements.toml` only the
`[mcp_servers.<nombre-aprobado>]` tables that the fleet may use. Olivares inventories the names,
not the commands, URLs, or credentials in those tables. A missing file, an unreadable file, and a
present file without `[mcp_servers]` produce different states; “not measured” is never displayed
as “none.”

Grok can also read `/etc/claude-code/managed-settings.json` for compatibility. Set
`managed_settings_path` only when Olivares should measure that surface. Do not reuse a Claude hook
without verification: Grok payloads use `camelCase` keys and `snake_case` events, and require
`olivares grok-hook`.

### 2. Governed hook

Install `olivares grok-hook` through the native discovery mechanism of the deployed Grok version:
either a settings JSON file from which Grok consumes the `hooks` key, or a `*.json` file in a hook
directory such as `~/.grok/hooks/`. Grok loads these files by name. Olivares does not define the
complete authoring wrapper, and this tree does not retain it; use the schema for the installed
version and set the command exactly to:

```text
olivares grok-hook
```

The PEP is mounted when `OLIVARES_GROK_HOOK_PEP_CONFIG` points to a valid configuration as
Olivares starts:

```json
{
  "listen": "127.0.0.1:8449",
  "tenant": "11111111-1111-4111-8111-111111111111"
}
```

Each instance governs one tenant and requires firm identity. The client reads
`OLIVARES_GROK_HOOK_URL`, `OLIVARES_GROK_HOOK_TOKEN`, `OLIVARES_GROK_HOOK_TENANT`,
`OLIVARES_GROK_HOOK_AGENT`, `OLIVARES_GROK_HOOK_ORG`, and `OLIVARES_GROK_HOOK_ACCOUNT`. Supply
these values through the process and secrets manager; the token does not belong in the hook JSON.

The name assigned to the hook matters. A user can add it to `~/.grok/disabled-hooks`, and the
dispatcher will omit it regardless of whether it came from a managed layer. Neither
`requirements.toml` nor MDM constrains that file. The connector reads it and emits a high-severity
finding with the disabled names, but it cannot prevent the disablement.

### 3. Optional OTLP traces

When `otlp_http=true`, the receiver listens on `127.0.0.1:4318` by default and accepts
`POST /v1/traces`, the path measured for Grok Build. This unauthenticated input must remain on
loopback. If another connector already uses `4318`, select an unused local port and apply the same
value to `otlp_http_addr` and the agent's OTLP endpoint.

Collection reduces traces to attribution, span name, and `session_id`; it does not retain content.
In this version, the next poll emits an aggregate finding with span, session, and drop counts. Use
the hook for the timeline and per-tool control.

[CAPTURA: Grok Build configuration showing a present `requirements.toml`, the `strict` profile, MCP inventory, `disabled-hooks` status, and the OTLP receiver disabled or bound to loopback; light and dark variants.]

## CLI usage

The following examples were run with the worktree binary on August 30, 2026. General startup
messages are omitted.

### Register the local source

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --kind grok \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 60 \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "grok-demo" (kind "grok", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → grok
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 60
  enabled: - → true
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

With SQLite, stop the engine before an offline roster mutation or use the live console. With
PostgreSQL, the command can run alongside the engine. `--actor` and `--reason` attribute the
provenance change.

For non-default paths, add explicit configuration values:

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --config config_path=/srv/grok-home/.grok/config.toml \
  --config requirements_path=/etc/grok/requirements.toml \
  --config disabled_hooks_path=/srv/grok-home/.grok/disabled-hooks \
  --config managed_settings_path=/etc/claude-code/managed-settings.json \
  --actor platform-operator \
  --reason grok-paths-for-service-user
```

### Connectivity test and actual file reading

The reproducible measurement taken on the screenshot host on August 30, 2026 produced this result:

```sh
olivares sources test \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --timeout 20s
```

```text
configuration: VALID (everything that can be decided without the network)
source "grok-demo" (grok): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

The process exited with code `0`. That host had an active Grok session and a present
`~/.grok/config.toml`; `/etc/grok/requirements.toml` and `~/.grok/disabled-hooks` were absent.
`sources test` read none of them: `Open` only resolves configuration, and `test` closes immediately
without calling `Gather`. Therefore, `ANSWERED` does not prove the session, sandbox, or findings.
To test file reading, reload the engine and inspect the findings emitted by the next poll.

### Verify fail-closed hook client behavior

With the endpoint unconfigured:

```sh
printf '%s' '{"hookEventName":"pre_tool_use","sessionId":"s-1","toolName":"Bash"}' | olivares grok-hook
```

Standard output:

```json
{"decision":"deny","reason":"no hay endpoint de gobierno configurado (deny-closed)"}
```

Standard error:

```text
no hay endpoint de gobierno configurado (deny-closed)
```

The exit code is `2`, which Grok interprets as a veto for `pre_tool_use`. For other events, a
denial is recorded but cannot prevent the action; the client reports this on stderr instead of
claiming enforcement.

## Control console

| Location | What it shows | Operational limitation |
|---|---|---|
| **Control console > Connectors** (`/console`) | `grok` roster, configured paths, interval, mode, and Test/Save/Reload actions. | The test opens and closes the connector; it does not read the TOML files. |
| **Health > Connectors** (`/health`) | Source status, message, trend, and latest poll. | Process health does not prove that a missing file is governed. |
| **Observability > Ingestion** (`/observability`) | Findings emitted by `olivares.grok`, first/last record, and, when enabled, aggregate OTLP activity. | Process-wide counters since startup; they reset and are not tenant-specific. |
| **Security** (`/security`) | Observed and enforced sandbox profile, MCP names, requirement presence/validity, managed-settings compatibility, and disabled hook names. | “Unreadable” remains unknown rather than becoming absent. |
| **Sessions** (`/sessions`) | Session, action, identity, permission mode, latest activity, and `enforced` or `observed` posture. | Requires hook events. Local inventory does not create a session. |
| **Audit** (`/audit`) | Attributable PEP decisions and chained evidence. | Exists only for calls that reached the PEP; a disabled hook leaves a gap. |

Do not expect a model catalog, xAI spend, or prompts: this source does not use the xAI API, and
the OTLP receiver discards content.

[CAPTURA: Grok Build dashboard showing sandbox/MCP/hook findings in Security, source activity in Ingestion, and a hook session with visible posture; light and dark variants of the seeded estate.]

## Production use

- **Linux endpoint baselines:** Distribute `requirements.toml` as a root-owned file and poll every
  host. Absence becomes an actionable finding, not a green default.
- **MCP control:** Compare user-declared names with those fixed by the administrator. The
  `GROK_CONFIG` variable cannot add sensitive tables such as MCP, authentication, or egress; that
  protection comes from Grok, and Olivares reports it without duplicating it.
- **Hook canary:** Start with a harmless tool and confirm the event, decision, and effect. Then
  monitor `disabled-hooks` continuously because the control can disappear by name.
- **Shared endpoints:** Configure absolute paths to the actual `HOME` of the account that runs
  Grok. The Olivares service's `~` can resolve to another user and produce an accurate measurement
  of the wrong host profile.
- **Minimal telemetry:** Enable OTLP only when the aggregate signal is required, and reserve a
  dedicated local socket. For preventive governance, prioritize reliable hook execution.

## What is enforced and what is only observed

| Surface | Actual behavior |
|---|---|
| `grok` source | **Observed, read-only.** Reads files and emits findings; does not modify Grok Build or call xAI. |
| `/etc/grok/requirements.toml` | **Enforces in the agent** the constrained sandbox and MCP values. Olivares verifies its presence and declared effect. |
| `~/.grok/config.toml` | **Observed preference.** Not an administrative policy by itself. |
| `olivares grok-hook` on `pre_tool_use` | **Can prevent the tool** when the command runs and exits with `2`. The client denies closed when the PEP is missing or fails. |
| Other Grok events | **Observed.** The denial remains as evidence, but the event has no equivalent veto. |
| Timeout, crash, or a hook that never runs | **Agent fails open.** Grok continues; the internal fail-closed behavior of `olivares grok-hook` applies only when the process is invoked. |
| `~/.grok/disabled-hooks` | **Can disable even a managed hook.** Olivares detects this afterward, but no requirements layer prevents it. |
| OTLP receiver | **Observes aggregates.** Does not authenticate, retain content, or replace the hook timeline. |

A deployment must not be declared “enforced” merely because the sandbox is fixed. Completion
requires effective requirements, a hook that actually runs, continuous monitoring for its absence
from `disabled-hooks`, a visible event, and a demonstrated `pre_tool_use` veto.
