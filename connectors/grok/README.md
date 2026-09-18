<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: Apache-2.0
-->

# Grok Build connector

Olivares AI integrates the **official Grok Build CLI**. It does not replace
that CLI and does not vendor it.

This connector reads **local** Grok Build configuration. It is not the xAI
model API (`connectors/xai`).

## Can-enforce

When `/etc/grok/requirements.toml` is present and root-owned, Grok **clamps**
it as the highest layer (above user config, managed overlays and
`GROK_SANDBOX`). `olivares grok managed-config` authors that file:

- `[sandbox] profile` (`strict`, `read-only`, `workspace`, `devbox`, `off`)
- `[mcp_servers.<name>]` allowlist (an empty table set is lockdown)

`olivares grok-hook` plus the local PEP **enforces** veto on `pre_tool_use`.
Other hook events are recorded and cannot be blocked.

## Can-only-observe

- User `~/.grok/config.toml` sandbox preference (command line and
  `GROK_SANDBOX` can still differ when requirements.toml is **absent**)
- `~/.grok/disabled-hooks`: a user can disable a managed hook **by name**.
  requirements.toml and MDM do not clamp that file
- Claude Code-compatible `managed-settings.json` when
  `managed_settings_path` is set (compatibility, not a Grok-native authoring
  surface)
- Binary presence, `--version`, and whether `~/.grok/config.toml` exists
  (presence is not a ready account)

Origin-install verification class is **origin-only**: HTTPS from the official
origin plus a bounded probe. That is not a publisher signature.

## Honest labels

| Surface | Label |
|---|---|
| `olivares agent tool install --driver grok` | `none-origin-only` |
| Probe `--version` | identity of the binary, **not** authentication |
| Session launch | requires `OLIVARES_SESSION_RUNTIME_GROK_BIN` **or** a managed install receipt |
| Official Grok account compatibility | **not claimed** by the operate path |

See [Install the Grok CLI](/how-to/install-grok-cli/) and
[Integrate Grok Build](/how-to/integrations/grok/).
