---
title: "MCP introspection & registry governance"
description: >-
  Inventory every MCP server your agents can reach, treat its self-declared
  hints as untrusted by spec, scan the catalog for tool-poisoning and posture
  issues, and reconcile against the public and federated registries.
sidebar:
  order: 7
---

The `mcp` source governs the **capability surface** your agents see: it
introspects MCP servers (tools, resources, prompts), derives read/write
*hints* from their annotations, and — opt-in — reconciles what is running
against the public MCP Registry, your federated registries, and the Docker
MCP Catalog, grading posture along the way.

One rule anchors everything this source emits:

:::caution[MCP annotations are untrusted, by specification]
A server's `readOnlyHint` / `destructiveHint` are self-declarations, and the
MCP spec says clients MUST treat them as untrusted. Every edge this source
produces is a **declared capability hint** — `approximate`, neither observed
nor permitted — that supplies the surface to diff against. It is corroborated
by observed sources, never trusted alone.
:::

## Declare the source

```json
{
  "sources": [{
    "name": "mcp-estate",
    "kind": "mcp",
    "tenant": "<tenant-id>",
    "config": {
      "config_path": "/etc/claude-code/.mcp.json",
      "posture_scan": "true",
      "registry_enabled": "true"
    }
  }]
}
```

Point it at servers either way:

| Key | Meaning |
|---|---|
| `servers` | inline JSON array of MCP server specs to introspect |
| `config_path` | path to a Claude Code `.mcp.json` whose `mcpServers` are introspected |
| `timeout` | per-server introspection timeout |

## The governance layers (each opt-in, each honest)

- **Posture scan** (`posture_scan`, default `true`) — scans the introspected
  catalog metadata for tool-poisoning, injection, homoglyphs and over-broad
  scopes, grading posture against the OWASP MCP Top-10. Catalog *metadata*
  only — it does not probe or exploit servers.
- **Public registry** (`registry_enabled`, default `false`; `registry_url`) —
  read-only provenance enrichment from the MCP Registry (preview upstream;
  the connector self-verifies what it reads).
- **Registry sync** (`registry_sync` + `owned_namespaces`) — enumerate the
  reverse-DNS namespaces your org owns in the public registry to detect
  yanked or unmanaged publications (the supply-chain angle), and clear your
  internal servers from shadow flagging.
- **Internal reconciliation** (`internal_servers`) — a JSON array of approved
  internal servers (`{name, registry_name, version}`); running servers are
  reconciled against it, with version-drift detection. What runs but is not
  on the list is a **shadow** finding.
- **Federated registries** (`federated_registries`) — GitHub BYO org
  registries, Azure API Center and private subregistries implementing the
  pinned **`/v0.1` registry OpenAPI**.
- **Deprecation feed** (`deprecation_feed`) — fetch the official MCP
  deprecated-features registry each pass to detect rule drift; the compiled
  deprecation rules never depend on the fetch.
- **Docker MCP Catalog** (`docker_catalog`) — image digest-pin drift plus
  Docker-built (signed) vs community (unattested) provenance per server.
- **Next-revision preview** (`next_revision_preview`) — introspect servers in
  the MCP 2026-07-28 RC stateless mode while still advertising 2025-11-25;
  explicitly a preview knob.

Findings land per layer: posture grades, provenance gaps, shadow servers,
deprecated-feature usage, registry drift.

## What you'll see in the console

**MCP & skills** is the live capability catalog — servers, their tools and
declared hints, skills, and how each is wired into agents:

<img class="light:sl-hidden" src="/console/capabilities-dark.png" alt="The MCP & skills view: the live capability catalog with servers, tools, wiring and managed configs." />
<img class="dark:sl-hidden" src="/console/capabilities-light.png" alt="The MCP & skills view: the live capability catalog with servers, tools, wiring and managed configs." />

The hints contribute the *declared* surface to the **Access map**; the drift
panel is where a declared-read-only tool observed writing stops being a
hint problem and becomes a finding.

## Honest limits

- **Introspection is a snapshot of what servers claim.** A server can lie;
  that is the spec's own position and the reason every edge is marked the
  way it is. Corroboration comes from observed sources.
- **A partial registry snapshot is an error, not a result** — the connector
  refuses to grade against a registry read it could not complete.
- **The posture scan reads metadata.** It does not execute tools, fuzz
  servers, or detect a backdoored implementation behind a clean catalog.

## Related

- [Connect Claude Code](/how-to/connect-claude-code/) — where MCP hints meet
  session telemetry.
- [Module V — MCP, skills & capabilities](/reference/modules/v-capabilities/).
- [Build and ship a connector](/how-to/build-a-connector/) — the deny-closed
  signed admission story for connector binaries themselves.
