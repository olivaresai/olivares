---
title: "Session cockpit (availability)"
description: >-
  Availability descriptor for the session-cockpit API namespace. Community
  registers that namespace with zero handlers and no interactive cockpit.
  How to confirm the absence against live sessions and AgentOps, and which
  open capabilities to use.
---

The Community binary registers an availability descriptor for the
`session-cockpit` API namespace. That namespace currently has **zero handlers**
and **no interactive cockpit**. Requests under `/v1/m/session-cockpit` receive
**404 by absence**. The descriptor is not one of the 30 product modules in the
catalog.

## Current availability

| Surface | Community (this artifact) |
|---|---|
| API namespace | `session-cockpit` (`/v1/m/session-cockpit`) |
| Descriptor | `olivares.session-cockpit` `0.1.0` — title `Session cockpit (availability)` |
| Registered routes / handlers | none |
| Interactive cockpit | none |
| Declared permission | `session-cockpit:availability:read` (declared, not routed) |
| Lifecycle | empty (`Init` / `Start` / `Stop` do nothing) |

A 404 on this namespace is the expected Community answer. It does not mean the
control plane failed to install.

## How to diagnose absence

Confirm that shipped session surfaces still work:

1. Live session module routes under the `sessions` namespace —
   [Live operation & sessions](/reference/modules/ii-sessions/).
2. Console **Sessions** (`/sessions`), **Claude Code** (`/agentops`) and
   **Work** (`/work`) — [console reference](/reference/console/).
3. Official CLI lifecycle in the next section.

If those respond and `/v1/m/session-cockpit` is 404, the descriptor matches this
artifact.

## Open capabilities

Official CLI installation, launch, observation and management remain in the
open Community product:

- [Live operation & sessions](/reference/modules/ii-sessions/) — live agent
  sessions, timelines, provider profiles, and `live_ref`.
- [Operate a provider session](/how-to/operate-provider-sessions/) — launch
  Claude, Codex or Grok under a pinned official binary.
- [Run Claude Code with Olivares](/how-to/run-claude-code-with-olivares/) —
  AgentOps co-deployment of official `claude` sessions.
- Connector and PEP-hook guides (observe/govern, not session launch):
  [Claude Code](/how-to/integrations/claude-code/),
  [Codex](/how-to/integrations/codex/),
  [Grok Build](/how-to/integrations/grok/).
- [Privileged-session recording](/reference/modules/recording/)
- [Identity, permissions & governance](/reference/modules/vi-governance/)

## Related

- [Modules catalog](/reference/modules/overview/)
- [Honesty & limits](/start/honesty-and-limits/)
