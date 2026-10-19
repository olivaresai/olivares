<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
-->

# Session command catalogs

`GET /v1/m/sessions/runs/{ref}/commands` serves the live session's own tool
catalog to the console's slash menu. Olivares keeps no built-in command list and
does not reconstruct the tool's discovery rules by scanning configuration files.
Choosing an entry inserts text; sending it uses the existing governed input route.

Measured on the vendor programs on October 13, 2026, with fresh configuration
homes and local catalog requests only, without a model turn or real credential:

| Tool/version | What the tool reports and how | Limits |
| --- | --- | --- |
| Codex 0.162.1 | Stable app-server `skills/list`, with the session's working directory and `forceReload: true`, reports enabled skills, descriptions and scopes. A project skill added during the session appears on the next read. | This interface does not enumerate the TUI's slash built-ins or its agents. Skills use `$name`; the route's optional `insert_text` preserves that spelling. Disabled skills are omitted. |
| Gemini CLI 0.62.0 | ACP `session/update` with `available_commands_update` reports the ACP command registry: 20 entries in the isolated probe, including `memory show` and `extensions list`. | The probe's custom project command did not appear. This version's ACP registry does not enumerate custom file commands, skills or agents; the interactive CLI's command set differs. Olivares serves only what ACP advertises. |
| OpenCode 1.18.30 | ACP `session/update` with `available_commands_update` reports slash commands, including a project command and project skill in the isolated probe. | ACP reports name and description without origin labels or a separate agent catalog. Source stays empty when the tool does not state it. |

The [ACP slash command protocol](https://agentclientprotocol.com/protocol/v1/slash-commands)
advertises command names, descriptions and optional input hints. Later command
updates replace the previous catalog immediately. Multiword names retain the
tool's spelling. A catalog received before the session's nomination is served
only after its session ID matches the correlated handshake response; foreign
session updates cannot replace a bound session's catalog.

Codex uses the existing five-second per-session cache and invalidates it on
`skills/changed`. Refreshes use the live child, its configuration and its cwd;
no extra process is launched. Discovery errors, malformed answers and timeouts
are explicit failures, or the previous catalog marked `stale` after a failed
Codex refresh. Catalog response frames are withheld from attach and recording,
whether well formed, malformed, cut off at exit or late: they are recognized by
the issued request id before the rest of the frame is read, and the caller gets
a fixed error text.

`reported: false` means the tool has not reported a catalog, the tool is not
supported by a reader, or the session is no longer running on this node.
An explicitly empty native catalog is `reported: true` with `commands: []`.
The route retains the run's write permission; an uncached Codex request also
retains the launch-authority and work-lease checks before writing to the child.

`TestToolCommands_RealNativeCatalogs` exercises the installed programs on PATH.
It skips tools absent from PATH, so a skipped leg is not real-tool evidence.
Protocol and route tests also cover cache refresh, foreign-session rejection,
native insertion spelling, empty catalogs and malformed updates.
