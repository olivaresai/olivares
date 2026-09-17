<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Integrate OpenCode with Olivares AI

> Editorial draft for the public documentation.

Olivares AI integrates [OpenCode](https://opencode.ai/) at the configuration layer. The in-process
`opencode` source reads the OpenCode configuration files that an operator names. It reports posture
findings, permitted-capability edges, an inventory summary and telemetry coverage. It does not change
those files, enforce their settings, or launch, observe or manage OpenCode sessions.

Before you describe an OpenCode posture as enforced, read
[OpenCode managed configuration: what Olivares AI reports and does not guarantee](./opencode-managed-config-parity.md).
In short:

- OpenCode's managed configuration is not an enforced lock.
- `OPENCODE_PERMISSION` can override permission at runtime, including managed permission.
- There is no distinct admin allowlist for MCP servers.
- The reported posture is computed without the contents of the managed configuration file.

## Capabilities

| Capability | Status in this repository |
|---|---|
| Read global and project OpenCode configuration files | Implemented in `connectors/opencode` |
| Report posture findings, permitted edges, inventory and coverage | Implemented in `connectors/opencode` |
| Register the source from the CLI | Implemented: `olivares sources set --kind opencode` |
| Render a managed hardening fragment | Library function `Render` only. No CLI command or engine path calls it. |
| Deploy a managed fragment, or confirm that OpenCode applies it | Not implemented |
| Enforce OpenCode configuration | Not implemented |
| Launch, observe or manage OpenCode sessions | Not implemented |

This page documents the CLI registration path. It does not document a console workflow for this
source, or where the collected findings are displayed.

## Register the source

The connector runs inside the Olivares AI process and reads local files. The configuration files
must be on the same host and readable by that process. The connector does not guess a home
directory or discover configuration paths: you supply them.

| Setting | Default | Purpose |
|---|---|---|
| `agent_ref` | `opencode` | Stable origin reference for the permitted-capability edges of this installation. |
| `global_config_path` | empty | User or global `opencode.json`, `opencode.jsonc`, or legacy `config.json`. |
| `project_config_path` | empty | Project `opencode.json` or `opencode.jsonc`. |

1. Preview the source change. `sources plan` computes the proposed source definition without
   writing or wiring the source. Opening the installation can still migrate local store state
   or create a missing sealer key:

   ```sh
   olivares sources plan --name opencode-workstation --kind opencode --tenant t_abc123 \
     --config global_config_path=/home/dev/.config/opencode/opencode.json \
     --config project_config_path=/srv/app/opencode.json \
     --poll-seconds 300 --data-dir /var/lib/olivares
   ```

2. Apply the same flags with `sources set`. It requires `--actor` and `--reason`, which are recorded
   in the audit ledger:

   ```sh
   olivares sources set --name opencode-workstation --kind opencode --tenant t_abc123 \
     --config global_config_path=/home/dev/.config/opencode/opencode.json \
     --config project_config_path=/srv/app/opencode.json \
     --poll-seconds 300 --data-dir /var/lib/olivares \
     --actor ops@example.com --reason "govern the OpenCode workstation configuration"
   ```

   `--poll-seconds` re-runs the source every N seconds. The value `0` runs it once.

3. Confirm the roster entry:

   ```sh
   olivares sources ls --data-dir /var/lib/olivares
   olivares sources get opencode-workstation --data-dir /var/lib/olivares
   ```

Keep `global_config_path` and `project_config_path` outside the OpenCode managed directory. A path
that names the managed file makes the connector read that file as global or project configuration.

## What the connector reads

- **Configuration contents.** The two supplied paths, global first and project second. Project values override
  global values; maps such as `permission`, `mcp`, `provider`, `agent` and `tools` are merged by key.
  If you supply neither path, the connector reads no file and reports the posture of an empty
  configuration, including `permission.default`.
- **File-presence checks.** The inventory also checks for `AGENTS.md` and `CLAUDE.md` beside the
  project configuration. Managed-file detection checks existence as described below. These checks
  do not read additional configuration contents.
- **Format.** JSONC: line comments, block comments and trailing commas are accepted. The connector
  reads at most the first 1 MiB of each file.
- **Missing, unreadable and invalid files.** A missing file is an absent layer, not an error. Any
  other read error stops the collection before a finding is emitted. A file that does not parse
  produces `config.invalid.global` or `config.invalid.project`, and that layer is excluded.
- **Other OpenCode sources.** The connector does not read remote organization configuration,
  `OPENCODE_CONFIG`, `OPENCODE_CONFIG_CONTENT`, `.opencode` directories, the contents of managed
  configuration files, macOS managed preferences, or the environment of an OpenCode process.
- **References and network.** `{env:…}` and `{file:…}` values are kept as unresolved text. The
  connector makes no network calls.
- **Emitted data.** A finding carries a display-safe `Title` and a `DetailHash`; the raw detail is not
  transmitted. The connector parses agent prompts with the rest of the file but does not emit them.
  `TestMinimalData` asserts that an agent prompt, the path and contents of a `{file:…}` reference, and
  a query token in an `instructions` URL appear in no emitted observation.

## Findings

Posture findings use the subject kind `opencode.posture`. The primary agent is `default_agent` when
set; otherwise the first enabled agent, by name, whose mode is `primary` or `all`; otherwise `build`.

| `SubjectRef` | Severity | Emitted when | `Title` |
|---|---|---|---|
| `admin_override.present` | info | `opencode.json` or `opencode.jsonc` exists in the managed directory | `opencode: managed admin config present (not immutable; OPENCODE_PERMISSION may override)` |
| `admin_override.absent` | high | No such file exists in the managed directory | `opencode: no local managed admin config detected (remote org config not visible locally)` |
| `permission.default` | high | Neither the primary agent nor the top level has a `permission` block, and the primary agent is not `plan` | `opencode: no permission block — permissive default` |
| `permission.allow.top-level`, `permission.allow.agent.<name>` | high | `permission` is the single value `allow`, at the top level or on an enabled agent | `opencode: blanket permission allow configured for <subject>` |
| `permission.bash` | high | The primary agent's effective permission does not gate `bash` with `ask` or `deny` | `opencode: bash is not gated by ask/deny` |
| `permission.edit` | high | The primary agent's effective permission does not gate `edit` with `ask` or `deny` | `opencode: edit/write/patch is not gated by ask/deny` |
| `mcp.allowlist` | medium | At least one MCP server is not disabled | `opencode: <n> MCP server(s) configured without a separate allowlist mechanism` |
| `provider.apiKey` | high | A provider `options.apiKey` is non-empty and does not start with `{env:` or `{file:` | `opencode: literal provider apiKey in config` |
| `share.auto` | high | The effective `share` value is `auto` | `opencode: share auto enabled (automatic session-data egress)` |
| `autoupdate.true` | medium | `autoupdate` is `true` | `opencode: autoupdate enabled` |
| `experimental.continue_loop_on_deny` | high | `experimental.continue_loop_on_deny` is `true` | `opencode: continue-loop-on-deny enabled` |
| `permission.runtime_bypass` | info | Every posture the connector builds | `opencode: OPENCODE_PERMISSION runtime override is outside local-file coverage` |
| `config.invalid.global`, `config.invalid.project` | medium | The file exists but does not parse as JSONC | `opencode: <scope> config present but invalid JSONC` |

The connector also emits:

- **An inventory finding** (`opencode.config`) whose `Title` summarizes the effective configuration.
  It includes the configured model identifiers, the default agent, the permission mode, whether
  `bash` and `edit` are gated, counts of MCP servers, tools, custom agents and providers, and the
  `share`, telemetry, `autoupdate`, loop-on-deny and literal-credential flags.
- **A coverage finding** (`opencode.coverage`): info when `experimental.openTelemetry` is `true`,
  medium when it is not. Its detail states that live usage can reach the control plane's OTLP ingest
  when OpenCode's `OTEL_*` exporter settings, which are outside the configuration file, point at the
  collector. The connector does not check those settings, and this page does not verify that path.
- **Permitted edges** from `agent_ref`: one `mcp.server` edge per enabled MCP server, one
  `opencode.tool` edge per enabled tool, and one `opencode.agent` edge per enabled custom agent. These
  edges state what the configuration permits, not observed use.

### `share` set to `auto`

OpenCode's [share documentation](https://opencode.ai/docs/share/) states that `auto` shares every
new conversation automatically. Sharing creates a public link and syncs the conversation history to
OpenCode's servers. The documented default is `manual`, and the connector treats an unset value as
`manual`. The finding detail recommends `disabled` for governed environments.

### Literal provider `apiKey`

A literal credential in a configuration file is a finding whether or not the file is shared.
OpenCode's [configuration documentation](https://opencode.ai/docs/config/) describes `{env:VARIABLE_NAME}`
and `{file:path}` substitution; values in those forms are not flagged and are not resolved. The
`Title` and the inventory report only that a literal value exists, not the value.

## Managed configuration

OpenCode's configuration documentation names the managed configuration directories:
`/etc/opencode` on Linux, `/Library/Application Support/opencode` on macOS and `%ProgramData%\opencode`
on Windows. The connector checks the directory for the operating system it runs on, or the directory
named by `OPENCODE_TEST_MANAGED_CONFIG_DIR` in the Olivares AI process environment, for
`opencode.json` or `opencode.jsonc`.

The connector reports only whether that file exists. It does not read the file, check that it parses,
or confirm that an OpenCode process applies it. The permission, MCP, share, credential and autonomy
findings describe the global and project files only. A managed file is also not a lock: it takes
precedence only for the keys it sets, `OPENCODE_PERMISSION` can override permission at runtime, and
on macOS MDM managed preferences take precedence over it. The
[managed configuration reference](./opencode-managed-config-parity.md) lists each limit, how it
reaches an operator, and the test that pins it.

## Hardening fragment

`Render` in `connectors/opencode` returns a JSON fragment intended for the managed directory:

- `permission.edit` and `permission.bash` are `ask` by default, may be set to `deny`, and any other
  value is rejected;
- `share` is `disabled`;
- `experimental.openTelemetry` is `true`;
- `mcp` contains the entries you supply. The authoring API calls this map an allowlist, but it is not
  an allowlist mechanism; see limit 8 of the managed configuration reference.

No CLI command or engine path in this repository calls `Render`. Distributing the fragment to hosts
and confirming that OpenCode applies it are outside Olivares AI. After you deploy it, the posture
findings still describe only the global and project files.

## Verification scope

- **Olivares AI behavior** was read from `connectors/opencode` and `cmd/olivares` in the commit that
  adds this page. The connector tests in `connectors/opencode` exercise the findings and limits named
  here.
- **OpenCode behavior** was read on 2026-09-11 from the official
  [configuration](https://opencode.ai/docs/config/), [permissions](https://opencode.ai/docs/permissions/),
  [share](https://opencode.ai/docs/share/), [agents](https://opencode.ai/docs/agents/),
  [MCP servers](https://opencode.ai/docs/mcp-servers/) and [CLI](https://opencode.ai/docs/cli/) pages.
  It was not observed on a running OpenCode binary. OpenCode changes often; check those pages again
  before you rely on a statement about OpenCode.
- **Not stated by those pages:** the precedence of `OPENCODE_PERMISSION` relative to managed
  configuration. This page states it as the connector's reported caveat.
