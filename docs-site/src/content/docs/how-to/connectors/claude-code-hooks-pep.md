---
title: "Claude Code hooks & enforcement (the PEP)"
description: >-
  The governance half of the Claude Code connector: hooks observed by default,
  and an opt-in policy enforcement point that answers PreToolUse /
  PermissionRequest hooks with deny or ask — every gate recorded as a finding.
sidebar:
  order: 5
---

[Connect Claude Code](/how-to/connect-claude-code/) wires the *observation*
half — OTLP telemetry in, access edges out. This page is the **governance
half**: Claude Code's **hooks** report tool decisions to the connector, and an
opt-in **policy enforcement point (PEP)** turns that channel into a gate — the
connector answers a matching `PreToolUse` / `PermissionRequest` hook with a
`permissionDecision` of `deny` or `ask`, and records every gate as a finding.

The default is deliberately **read-first**: with no enforcement policy
configured, hooks are *observed, never gated*. Enforcement is a named,
explicit opt-in, and an invalid policy **fails at startup** — the connector
will not run silently ungoverned.

## How the hook channel works

The connector's OTLP/HTTP receiver (loopback `127.0.0.1:4318` by default)
also serves the hook endpoint at `hook_path` (default **`/hooks`**). On the
developer machine, Claude Code's hook configuration posts its hook events to
that loopback endpoint — the exact hook settings syntax belongs to Claude
Code's own documentation; what this product owns is the receiver and the
policy below.

Hook events and OTLP telemetry about the same tool call are **correlated**
(the `correlation_window`, default 5s, holds one side waiting for the other),
so a gated action and its telemetry land as one coherent story, not two
disconnected records. A session that keeps hooking but goes OTLP-silent
beyond `silence_threshold` (default 2m) is flagged as a telemetry gap — the
anti-evasion signal.

## Turning on enforcement

Add an `enforcement` policy to the source's config
(`OLIVARES_SOURCES_CONFIG`):

```json
{
  "sources": [{
    "name": "claude",
    "kind": "claude",
    "tenant": "<tenant-id>",
    "config": {
      "enforcement": "{\"rules\":[{\"tool\":\"Bash\",\"decision\":\"ask\",\"reason\":\"shell needs a human\"},{\"resource_kind\":\"file\",\"mode\":\"write\",\"decision\":\"deny\"}]}"
    }
  }]
}
```

Rules match on the tool name and/or the resource kind and access mode; the
decision is `deny` or `ask` (escalate to the human in the session). Matching
`PreToolUse` / `PermissionRequest` hooks get that decision back as Claude
Code's `permissionDecision`; everything else passes through observed. Each
gate is recorded as a **finding**, so the enforcement trail is queryable, not
folklore.

:::note[The kill switch outranks everything]
If the estate (or the specific agent) is under an
[emergency stop](/how-to/cookbook/kill-switch-drill/), `claude.tool.use` is
killed at the governance layer regardless of this policy — the stop gate is
checked before any per-tool rule, and it fails closed.
:::

## Fleet posture: managed settings, observed

Enforcement at the hook is one layer. The fleet-wide layer is Claude Code's
**managed settings** file, which the `managed-settings` source observes
read-only:

```json
{
  "sources": [{
    "name": "fleet-policy",
    "kind": "managed-settings",
    "tenant": "<tenant-id>",
    "config": {
      "config_path": "/etc/claude-code/managed-settings.json",
      "expected_policy": "{…governance-authored intent…}"
    }
  }]
}
```

| Key | Default | Meaning |
|---|---|---|
| `config_path` | `/etc/claude-code/managed-settings.json` (Linux) | the host's live managed-settings file (macOS: `/Library/Application Support/ClaudeCode/…`) |
| `scope` | OS hostname | attribution scope (host id / distribution name) |
| `expected_policy` | — | optional authored intent; when set, the connector reports **drift** (permitted-policy vs observed-config). Empty = observe-only |

Related opt-in observers on the `claude` source: `managed_mcp_path` (models
the managed-MCP allowlist's eval order and flags name-only allow entries) and
`sandbox_path` (posture findings on the sandbox lockdown settings) — both
read-only, both off until pointed at a file.

## What you'll see in the console

**Claude Code governance** is the authoring and truth-loop surface: the
policy you intend, the configuration hosts actually carry, and the drift
between them. Gates and telemetry-gap findings land in **Security**; the
session itself stays visible in **Sessions**:

<img class="light:sl-hidden" src="/console/claude-policy-dark.png" alt="The Claude Code governance view — policy authoring and fleet posture in one place." />
<img class="dark:sl-hidden" src="/console/claude-policy-light.png" alt="The Claude Code governance view — policy authoring and fleet posture in one place." />

## Honest limits

- **The PEP gates what hooks report.** A host whose hooks are not configured
  is not gated — pair the fleet with the
  [managed-settings observer](#fleet-posture-managed-settings-observed) so
  absence is visible, and with the
  [kernel backstop](/how-to/connectors/ebpf-tetragon/) so it is not blind.
- **`ask` defers to a human in the session** — it is friction, not a lock.
  `deny` is the lock.
- **Subprocesses are out of scope here** (hooks fire for Claude Code's own
  tool calls); see the
  [enterprise OTel page](/how-to/claude-code-enterprise-otel/) for what the
  telemetry env does and does not reach.

## Related

- [Connect Claude Code](/how-to/connect-claude-code/) — the observation half.
- [Enterprise OTel for Claude Code](/how-to/claude-code-enterprise-otel/) —
  fleet telemetry, labels, tracing.
- [Govern and approve](/how-to/govern-and-approve/) — the authorization model
  the PEP plugs into.
