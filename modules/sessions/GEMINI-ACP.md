<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
-->

# Gemini CLI ACP driver

Owned operate form: `gemini --acp`, with `GEMINI_CLI_NO_RELAUNCH=true`.
Protocol token: `gemini_acp` (bidirectional, text). Driver key: `gemini-cli`.
The transport, the turn, the permission request and shutdown are the shared ACP
session's (`acp_session.go`, `acp_conversation.go`, `acp_permission.go`); the
driver keeps its handshake, its settings and its facts row
(`core/driverfacts/facts.go`).

Recorded against `@google/gemini-cli` 0.62.0, driven over stdio with a loopback
stand-in for the Gemini API and a synthetic key. Nothing below was measured
against Google's own service.

## What the CLI does

- `initialize` advertises `oauth-personal`, `gemini-api-key`, `vertex-ai` and
  `gateway`, `loadSession`, and HTTP and SSE MCP. It has no `session/close`.
- With no credential in its environment, `session/new` answers `-32000`
  ("Gemini API key is missing or not configured."). With `GEMINI_API_KEY` in the
  environment it opens a conversation without `authenticate`. A `.env` file in
  the home or the folder is not read in ACP mode.
- `session/new` returns `modes` (`default`, `autoEdit`, `yolo`, `plan`) and
  `models` (`auto` and named models). `session/set_mode` and `session/set_model`
  answer `{}`. Setting a mode also emits one `[MODE_UPDATE] <mode>` message chunk.
- The model `auto` sends a second routing request to another model. A launch that
  names a model avoids it.
- `session/request_permission` offers `proceed_always` (`allow_always`),
  `proceed_once` (`allow_once`) and `cancel` (`reject_once`). A shell call
  (`kind: execute`) carries its command only in `title` and no `rawInput`; an edit
  (`kind: edit`) carries its absolute path in `locations`, and its `title` is a
  description ("Writing to note.txt"). Safe commands such as `echo` run without a
  request.
- A `tools.allowed` in `<home>/.gemini/settings.json` runs a shell call with no
  request at all. One in the folder's `.gemini/settings.json` did not, and a system
  settings file named by `GEMINI_CLI_SYSTEM_SETTINGS_PATH` with `tools.allowed: []`
  did not override the home's.
- Without `GEMINI_CLI_NO_RELAUNCH` the CLI starts a second copy of itself and
  ignores SIGTERM in the first.

## What the driver does

- A bound Gemini API key selects the advertised native `gemini-api-key` method
  before opening a conversation. Account-home and legacy launches retain the
  CLI's own login. Readiness starts `unknown`; `-32000` moves it to `required`.
- It always sets the approval mode: `plan` for the read-only preset, `default`
  for every other preset. It never selects `autoEdit` or `yolo`, so every tool the
  CLI does not consider safe reaches the live approval policy. A mode or model the
  CLI did not offer fails the handshake.
- It grants a permission with `proceed_once` only, never `proceed_always`, and
  refuses with the CLI's `reject_once` option.
- It refuses, without asking the policy, a request that gave the policy nothing to
  review: any tool that is not a read, a search or a thought and has no command or
  path fact. A shell call is always that, so a Gemini session cannot run a command
  under Olivares until the CLI names the command in `rawInput`. An edit carries its
  path from `locations` and reaches the policy.
- It refuses a launch whose profile home pre-approves tools, on every create and
  resume, and one whose configuration home is not named `.gemini`. Refused: a
  non-empty `tools.allowed`, `tools.core`, `mcp.allowed`, `policyPaths` or
  `adminPolicyPaths`, any `mcpServers.<name>.trust: true`, and any file under
  `policies/`. Keys are read as the CLI reads them: case-sensitive, the last of a
  repeated key wins, and a `trust` that is truthy in JavaScript (`"true"`, `1`) counts.
  A file that cannot be read, is not a regular file or is over 1 MiB is refused. A
  settings file that does not parse here (it has comments, say) is refused when it
  names any of those keys (`allowed`, `core`, `trust`, `policyPaths`,
  `adminPolicyPaths`) whatever the value, or holds a `\u` escape, which could spell
  one.
- It refuses template instructions and tool restrictions, as OpenCode does, rather
  than discard them.
- The model is carried; the effort is not (the CLI has none).
- `GEMINI_CLI_HOME` names the home that holds `.gemini`. A profiled launch sets it
  to the parent of the profile's configuration home, `HOME` stays the profile's
  user home, and no `GEMINI_CLI_*` variable can come from `env_allow` or the host.

## Limits

- Providers accepts a `gemini` key bound only to Google's native API address. The
  child receives `GEMINI_API_KEY`; a bound launch refuses alternate Google
  credentials, routing variables and saved OAuth/Vertex/gateway authentication
  settings before spawning. Legacy unbound explicit `env_allow` remains supported.
- The official installer verifies the GitHub release bundle's SHA-256 before
  bounded ZIP extraction, inventories the complete bundle, and uses a fixed Node.js
  launcher. Node.js 20 or newer must be installed on the engine host.
- `olivares tool login gemini` and the console relay the native ACP Google
  `oauth-personal` flow with `NO_BROWSER=true`. Each account owns separate user and
  `.gemini` homes. The CLI writes its credentials; Olivares does not read their
  contents. Native Google settings plus a nonempty regular `oauth_creds.json`
  are a readiness hint, not proof
  that a refresh token remains valid. Login completion requires the native
  authentication acknowledgment. Saved `oauth-personal` settings can prompt before
  ACP starts on 0.62.0; starting the relay in that home is refused with the next
  step of using its existing login or creating a new Google profile. The relay
  does not rewrite native settings.
- Resume sends `session/load`. On 0.62.0 the CLI answered "No previous sessions
  found for this project." for a conversation it had just written, so a resume is
  refused rather than started as a new conversation. The success path is tested
  against a fake peer only.
- The Olivares session MCP edge is not connected; MCP servers in the CLI's own
  settings are not governed. The run says so in `mcp_governance_warning`.
- Bound-key sessions disable native OpenTelemetry and prompt logging switches.
  CLI 0.62.0 still attempts Clearcut requests to `play.googleapis.com`; the
  enforced session network boundary must block these attempts.
  Google login URL and code prompt were measured against the official CLI;
  successful Google account exchange uses a faithful stub until vendor accounts
  are introduced at migration.
- The approval mode is set and not read back: `session/set_mode` answers `{}`, so a
  CLI that changed it afterwards would not be noticed. Measured effect: under
  `plan` the CLI neither asked for nor ran a shell call.
- Only `tools.allowed` was run against the CLI. The other refused keys come from
  reading the policy builder (`createPolicyEngineConfig`) and the settings schema in
  the pinned 0.62.0 package; none of them was tried end to end. Not covered: an
  extension's own MCP servers under `<home>/.gemini/extensions/`, which were not read,
  and `general.defaultApprovalMode` (the driver sets the mode itself, see above).
  The folder's own `.gemini/settings.json` was measured for `tools.allowed` only.
- Settings that pre-approve tools are checked when a session starts. One the
  session writes during its run takes effect at the next start, which the check
  then refuses.
- Read tools run without a request in `default` mode; only Landlock bounds them.
