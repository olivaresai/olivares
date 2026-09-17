<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# OpenCode managed configuration: what Olivares AI reports and does not guarantee

> Editorial draft for the public documentation.

This page is the reference for how the `opencode` connector (`connectors/opencode`) treats
OpenCode's managed configuration layer. It lists the OpenCode configuration sources the connector
reads, the limits of a managed configuration file, and the channel through which each limit reaches
an operator. Read it before you describe an OpenCode posture as enforced.

For registration and the complete finding list, see [Integrate OpenCode with Olivares AI](./opencode-guide.md).

## Summary

- **Olivares AI does not enforce OpenCode configuration.** The connector reads files and reports
  findings. It does not write, deploy, verify or lock a managed configuration file.
- **Managed configuration is not an enforced lock.** It takes precedence only for the keys it sets.
  `OPENCODE_PERMISSION` can override permission at runtime. On macOS, managed preferences deployed
  by MDM take precedence over managed configuration files.
- **The connector detects a managed file by existence only.** The permission, MCP, share, credential
  and autonomy findings are computed without that file's contents.
- **There is no distinct admin allowlist for MCP servers.** Publishing `mcp` entries is not an
  allowlist mechanism. The `mcp.allowlist` finding states this.
- **`share` set to `auto` and a literal provider `apiKey` are findings.** Both are reported with high
  severity.

## Configuration sources and what the connector reads

OpenCode's [configuration documentation](https://opencode.ai/docs/config/) lists the sources below
from lowest to highest precedence. It describes the sources as merged: a later source overrides an
earlier one only for conflicting keys, and non-conflicting keys from every source are kept.

| Precedence | OpenCode source | What the `opencode` connector does |
|---|---|---|
| 1 (lowest) | Remote organization config from `.well-known/opencode` | Does not read it. The connector makes no network calls. |
| 2 | Global config, for example `~/.config/opencode/opencode.json` | Reads the file named by `global_config_path`, if the operator supplies it. It does not discover the path. |
| 3 | Config file named by `OPENCODE_CONFIG` | Does not read it. |
| 4 | Project config (`opencode.json`) | Reads the file named by `project_config_path`, if supplied, and merges it over the global file. |
| 5 | `.opencode` directories | Does not read them. |
| 6 | Inline config from `OPENCODE_CONFIG_CONTENT` | Does not read it. |
| 7 | Managed config files: `/etc/opencode` on Linux, `/Library/Application Support/opencode` on macOS, `%ProgramData%\opencode` on Windows | Checks whether `opencode.json` or `opencode.jsonc` exists. Does not read the contents. |
| 8 (highest) | macOS managed preferences in the `ai.opencode.managed` domain, deployed by MDM | Does not read them. |

`OPENCODE_PERMISSION` does not appear in that list. OpenCode's [CLI reference](https://opencode.ai/docs/cli/)
documents it as an inline JSON permissions configuration. The connector reports it as a runtime
override of permission, including managed permission. The connector cannot read the environment of
an OpenCode process.

## Limits of managed configuration

A finding `Title` is the display-safe summary of a finding. The detail of a finding is transmitted
only as `DetailHash`, so a limit that appears only in the detail reaches an operator through this page.

| # | Limit | Consequence for an operator | Channel to the operator | Source | Test |
|---|---|---|---|---|---|
| 1 | Managed configuration takes precedence only for the keys it sets. Keys it omits come from the other sources. | A managed fragment does not govern a key that it does not set. | `Title` of `admin_override.present` ("not immutable"). Emitted only when a managed file exists. | `postureFindings` in `govern.go` | `TestTheManagedFindingSaysItIsNotALock` |
| 2 | `OPENCODE_PERMISSION` can override permission at runtime, including managed permission. | A variable in the OpenCode process environment can change permission without a file change. | `Title` of `admin_override.present` and of `permission.runtime_bypass`. The second is emitted with every posture the connector builds. | `postureFindings` in `govern.go` | `TestTheManagedFindingSaysItIsNotALock`, `TestTheRuntimeOverrideCaveatIsEmittedWithEveryBuiltPosture` |
| 3 | Remote organization configuration is not visible to a local-file reader. | `admin_override.absent` means that no local managed file was found. It does not mean that no organization configuration applies. | `Title` of `admin_override.absent` ("remote org config not visible locally"). Emitted only when no managed file exists. | `postureFindings` in `govern.go` | `TestTheAbsentManagedFindingSaysWhatItCannotSee` |
| 4 | On macOS, MDM managed preferences take precedence over managed configuration files. The connector does not read them. | On a Mac, a managed file can be overridden by a profile that the connector does not report. | This page only. | `managedConfigDirs` in `govern.go` | None |
| 5 | When `OPENCODE_TEST_MANAGED_CONFIG_DIR` is set in the Olivares AI process environment, the connector checks that directory instead of the OS managed directory. | The directory the connector checks can differ from the directory an OpenCode process uses. | Finding detail only, which is transmitted as `DetailHash`. The readable channel is this page. | `managedConfigDirs` in `govern.go` | None |
| 6 | The connector detects the managed file by existence only. Its contents do not enter the reported posture. | An empty or invalid managed file still produces `admin_override.present`. After you deploy a managed fragment, the posture findings do not show its effect. | This page only. No finding reports it. | `detectManagedConfig` in `govern.go`, `readLayers` in `opencode.go` | `TestTheManagedPermissionLeafNeverReachesThePosture`, `TestTheManagedShareMCPCredentialAndAutonomyLeavesNeverReachThePosture` |
| 7 | `global_config_path` and `project_config_path` are used as supplied. They are not compared with the managed directory. | If a supplied path names the managed file, the connector parses that file as global or project config and its contents enter the posture. Keep both paths outside the managed directory. | This page only. | `Open` and `readLayers` in `opencode.go` | `TestTheProjectPathAliasDoesLetManagedBytesIn` |
| 8 | There is no distinct admin allowlist for MCP servers. The authoring API documents `Policy.MCPServers` as "the governed MCP server allowlist", but publishing `mcp` entries is not an allowlist mechanism. | Do not treat published MCP entries as the set of servers OpenCode admits. | `Title` of `mcp.allowlist` ("without a separate allowlist mechanism"). Emitted only when at least one MCP server is enabled in the files the connector read. | `postureFindings` in `govern.go`, `Policy` in `authoring.go` | `TestTheAllowlistFindingDeniesBeingAnAllowlist` |

About limit 8: OpenCode's [MCP documentation](https://opencode.ai/docs/mcp-servers/) describes
disabling a server with `enabled` set to `false` and managing MCP tools through the `tools`
configuration. It describes no separate MCP allowlist. The configuration documentation describes
sources as merged rather than replaced, but does not state how `mcp` entries from different sources
combine, and this page does not claim it. The `mcp.allowlist` finding counts enabled servers only;
it does not evaluate `tools` patterns.

Limit 1 depends on OpenCode's merge behavior as documented. The connector does not execute OpenCode
to confirm it.

## What managed configuration does provide

According to OpenCode's configuration documentation, managed configuration files take precedence
over sources 1 to 6 in the table above for the keys they set. The
documentation also states that writing to the managed directories requires administrator or root
access.

The connector package includes a library function, `Render`, that produces a managed configuration
fragment. The fragment sets `permission.edit` and `permission.bash` to `ask` or `deny`, sets `share`
to `disabled`, enables `experimental.openTelemetry`, and includes the supplied `mcp` entries. No CLI
command or engine path in this repository calls `Render`. Deploying the fragment, and confirming
that an OpenCode process applies it, are outside Olivares AI.

## Changing this page

The tests in the table pin the finding titles and the limits of the current connector. If a title
changes, update its test and this page in the same change. If the connector starts reading
managed file contents, or starts refusing a supplied path that names the managed file, the tests
for limits 6 and 7 fail. Rewrite those rows instead of weakening the tests.

## Verification scope

- **Olivares AI behavior** was read from `connectors/opencode` and `cmd/olivares` in the commit
  that adds this page, and is exercised by the tests named above.
- **OpenCode behavior** was read on 2026-09-11 from these official pages:
  [configuration](https://opencode.ai/docs/config/), [permissions](https://opencode.ai/docs/permissions/),
  [share](https://opencode.ai/docs/share/), [agents](https://opencode.ai/docs/agents/),
  [MCP servers](https://opencode.ai/docs/mcp-servers/) and [CLI](https://opencode.ai/docs/cli/).
  It was not observed on a running OpenCode binary. OpenCode changes often; check those pages
  again before you rely on a statement about OpenCode.
- **Not stated by those pages:** the precedence of `OPENCODE_PERMISSION` relative to managed
  configuration, and any OpenCode behavior for `OPENCODE_TEST_MANAGED_CONFIG_DIR`. Row 2 states the
  connector's reported caveat. Row 5 describes only the connector's own lookup.
- This page does not compare OpenCode with the managed layers of other agents.
