<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: Apache-2.0
-->

# Claude Code connector

Olivares AI integrates the **official Claude Code CLI**. It does not replace
that CLI and does not vendor it.

## Can-enforce

- `olivares agent managed-settings` writes `/etc/claude-code/managed-settings.json`
  (permissions, managed MCP, sandbox lockdown keys Claude honors)
- `olivares claude-hook` plus the local PEP **enforces** allow/ask/deny for
  events Claude Code can veto; endpoint failure is deny-closed
- `olivares agent tool install --driver claude` records a **publisher-signed**
  receipt (detached OpenPGP on the vendor manifest, pinned key)

## Can-only-observe

- OTLP traces/metrics Claude emits
- Whether `~/.claude.json` exists (presence is not a ready account; values are
  never read)
- Probe `--version` (identity, **not** authentication)

See [Run Claude Code with Olivares](/how-to/run-claude-code-with-olivares/) and
[Integrate Claude Code](/how-to/integrations/claude-code/).
