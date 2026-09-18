<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: Apache-2.0
-->

# Codex connector

Olivares AI integrates the **official OpenAI Codex CLI**. It does not replace
that CLI and does not vendor it.

Install, probe and launch are `olivares agent tool` and `olivares agent session`.
This connector is the **read-only enterprise source** plus the PEP hook path.

## Can-enforce

These controls are enforced by Codex when the system-tier files are in place
(`/etc/codex/requirements.toml` and `/etc/codex/managed_config.toml`), authored
by `olivares codex managed-config`:

- allowed approval policies and sandbox modes
- web-search mode allowlist (empty array is lockdown)
- remote-control and managed-hooks-only flags
- MCP server allowlist
- sandbox network / workspace-write constraints in `requirements.toml`

`olivares codex-hook` plus the local PEP **enforces** allow/ask/deny for hook
events Codex can veto. An endpoint failure is deny-closed.

## Can-only-observe

- Analytics, Compliance, Audit Logs and billed costs (enterprise APIs; a ChatGPT
  subscription is not a connector credential)
- Whether a host binary is present, its `--version` line, and whether
  `~/.codex/auth.json` **exists** (presence is not a ready account; values are
  never read)
- Origin-install of the official package requires a subject-proof verifier.
  Without it, Olivares **refuses** to record a publisher-signed receipt rather
  than claiming Sigstore success. Detection of an already-installed `codex`
  still works.

## Honest labels

| Surface | Label |
|---|---|
| `olivares agent tool install --driver codex` | mixed-assurance when a subject verifier is configured; otherwise `verification_unavailable` |
| Probe `--version` | identity of the binary, **not** authentication |
| Session launch | requires `OLIVARES_SESSION_RUNTIME_CODEX_BIN` **or** a managed install receipt |

See [Install the Codex CLI](/how-to/install-codex-cli/) and
[Integrate Codex](/how-to/integrations/codex/).
