---
title: Session runtime API (official CLIs)
description: >-
  Community HTTP surface that lists, attaches, feeds and stops owned Claude Code,
  Codex and Grok CLI processes. Permissions, stdio I/O, resume and reconnect.
---

The control plane **launches the vendor CLI**. It does not replace Claude Code,
Codex or Grok Build. Sessions and terminals are one module of this product, not
the product.

This page documents the Community operate routes under `/v1/m/sessions/runs`.
They already exist in the [beta OpenAPI document](/reference/api-beta/). 26.10
adds the driver contract, the transport each launch form needs, and journeys
J01–J08 as tests.

## Edition boundary

| Edition | What it does | What it does not do |
|---|---|---|
| **Community (this page)** | Owned local child: launch, stdin/stdout/stderr, attach with cursor, resume of the exact conversation, reconnect of a live stream, stop with observed exit status. Session rows and evidence stay in module II. | Multi-pane Identity & Scale engine, mTLS agent listener, cockpit pane input, xterm UI chunk |
| **Identity & Scale overlay** | Commercial session-cockpit engine (listener, panes, recording ledger). Routes live under `/v1/m/session-cockpit/` when the module is present. | It does not replace `/v1/m/sessions/runs` |

A Community build answers the overlay namespace by **absence** (404). It does
not mount a 501 stub.

The Claude Code hook path stays `olivares claude-hook` (PreToolUse PEP). That
is observation and enforcement, not a replacement CLI.

## Permissions

Custom roles may confer `sessions:stop:write` or `sessions:output:read` separately.
Stopping confers no input, resume, or Git-write authority. Stored custom roles, permission groups and Cedar policies keep their existing
coverage: `sessions:run:write` includes stop, and `sessions:run:read` includes output.
Cedar policies receive the original action and `context.permission` on split routes
by default, including wildcard and negative conditions.
A policy explicitly comparing its action or `context.permission` with the narrow
replacement uses the new permission. Unrelated resource strings do not opt it in.
Use the narrow permissions for independent grants. Output access is privileged:
editors, admins and owners hold it by default.

For helpdesk work, `authenticator:write` permits a tenant member's TOTP reset at AAL3,
subject to the existing shared-account and tenant-custody checks. It grants no membership
or invitation writes. `audit:read`, `authz:read` and `accessgraph:read` are also individually
grantable. These core capabilities require tenant scope; they are not workspace tree nodes.
A tenant-scope grant can authorize a named user who has no tenant membership.

Existing enforcement on the module routes:

| Permission | Routes |
|---|---|
| `sessions:run:read` | `GET /runs`, `GET /runs/{ref}`, `POST /runs/{ref}/pin`, `DELETE /runs/{ref}/pin`, `GET /push/key`, `POST /push/subscriptions`, `DELETE /push/subscriptions/{id}` |
| `sessions:output:read` | `GET /runs/{ref}/events`, `GET /runs/{ref}/attach`, `GET /runs/{ref}/changes`, `GET /runs/{ref}/changes/file`, `GET /runs/{ref}/git`, `GET /runs/{ref}/diff`, `GET /runs/{ref}/diff/file`, `GET /runs/{ref}/handoff`, `GET /runs/{ref}/activity`, `GET /runs/{ref}/activity/agents/{agent}`, `GET /runs/{ref}/messages`, `GET /runs/{ref}/attachments/{id}` |
| `sessions:run:write` | `POST /runs`, `POST /runs/{ref}/input`, `POST /runs/{ref}/interrupt`, `POST /runs/{ref}/resume`, `POST /runs/{ref}/move`, `POST /runs/{ref}/handoff`, `PUT /runs/{ref}/name`, `POST /runs/{ref}/title`, `POST /runs/{ref}/archive`, `DELETE /runs/{ref}/archive` |
| `sessions:stop:write` | `POST /runs/{ref}/stop` |
| `sessions:run:admin` | `POST /runs/{ref}/cleanup`, `DELETE /runs/{ref}` |

A viewer can list and pin sessions for themself. Create and input require `sessions:run:write`; stop requires `sessions:stop:write`.
Session output now requires `sessions:output:read`, held by editors, admins and owners.
Viewers keep listing, metadata and personal marks, but lose events, attachment bodies,
transcripts, handoff summaries and file or git content unless explicitly granted output access. The authorizer on the
route is the enforcement point; a missing permission is 403.

Stored ABAC denies continue to restrict both the legacy session permission and its
new output or stop permission. OPA evaluates each split request twice: once with
`input.permission` set to the new permission and once with the legacy value. Both
results must allow; errors and undefined decisions still deny. Resource identity
and other request attributes stay unchanged.

Stored custom-role and permission-group lists retain their explicit legacy session
coverage on upgrade. Newly authored lists are exact: `sessions:run:read` lists
sessions without reading output. Built-in viewer grants use that same boundary;
editors can subtract `sessions:output:read` from a custom role. A
`sessions:run` resource-class filter includes output and stop permissions already
present in the role; it does not add either permission. Hand-authored Cedar
policies keep legacy inputs unless they name the new permission, including through
`!=` or a matching `like` pattern. A declared Cedar action such as `run:stop` stays
unchanged while compatibility applies to `context.permission`.

## Routes the console reads

Base: `/v1/m/sessions`. Authenticate. Send `X-Olivares-Tenant`.

| Method | Path | Result |
|---|---|---|
| `GET` | `/runs` | Page of managed runs. Optional `state`, `live_ref`, `claude_session_id` (legacy) |
| `GET` | `/runs/{ref}` | One run. `state` is derived. `exit_code` is observed. No fabricated success field. `archived_at` (RFC 3339, UTC) is present while the session is archived |
| `GET` | `/runs/{ref}/events` | Lifecycle evidence rows |
| `GET` | `/runs/{ref}/attach?from={seq}` | SSE: `output` frames, `lag` if the ring evicted below the cursor, `end` or a not-live `notice`. A Grok, OpenCode or Gemini CLI session resumed after a launch of a run given vault secrets, a repository credential, a managed credential or a package registry token does not stream the conversation its tool replays on resume, and says so in one `olivares_notice` frame (`history_withheld`) |
| `POST` | `/runs/{ref}/input` | stdin. Stream-json uses `line`/`message`. Codex/Grok use `text`. 202 `{accepted:true}` |
| `POST` | `/runs/{ref}/stop` | SIGTERM then SIGKILL of the process group. Observed exit status on the row |
| `POST` | `/runs/{ref}/resume` | New process generation. Exact stored conversation. No silent new-conversation fallback |
| `POST` | `/runs/{ref}/interrupt` | Cancel the active turn. The process stays |
| `POST`, `DELETE` | `/runs/{ref}/pin` | Pin the session for the caller, or unpin it. The pin is the caller's own and follows them to another browser or machine; it never changes another person's list. Both calls are idempotent and answer 200 `{run_ref, pinned, pinned_at}`. A person keeps at most 20 pins: the oldest leaves when a 21st is pinned. Releasing or deleting a session drops every pin on it. `GET /runs` and `GET /runs/{ref}` carry the caller's own `pinned` and `pinned_at`, and `GET /runs` also returns the caller's pinned sessions that fall outside its page (at most 20 rows more than `limit`; `pagination=cursor` adds none, since it reaches every session once). A pin belongs to the credential that made it: a person's console and `olivares login` sessions share their pins, an API token keeps its own. A released session answers 409 |
| `GET` | `/push/key` | The application server key a browser subscribes with (`PushManager.subscribe`), answered 200 `{public_key}`. 503 when this engine has no Web Push |
| `POST` | `/push/subscriptions` | Register the caller's browser for Web Push: `{endpoint, keys: {p256dh, auth}}`, the shape of a browser's `PushSubscription`. Answers 200 `{id}`, the hex SHA-256 of the endpoint; the same browser registering again updates its keys. The endpoint must be `https` on port 443 and a name, never an address (400 otherwise). It is the caller's own, like a pin: a session of theirs that asks for an approval or ends, other than by their own stop, is pushed to it, encrypted to the browser (RFC 8291) and signed with the engine's VAPID key (RFC 8292). A person keeps at most 10 browsers: the oldest leaves when an 11th registers. A push service that no longer knows a browser (404 or 410) removes it |
| `DELETE` | `/push/subscriptions/{id}` | Remove one of the caller's own browsers, by the `id` the registration answered. Idempotent: answers 204 whether it was registered, was already gone or is someone else's |
| `POST`, `DELETE` | `/runs/{ref}/snooze` | Snooze the session for the caller until a moment, or end the snooze. `POST` takes `{until}`, an RFC 3339 time in the future (400 otherwise); a second `POST` moves it. The snooze is the caller's own, like the pin: the console leaves the session out of that person's list until `until`, and nobody else's list changes. Both answer 200 `{run_ref, snoozed_until?, done}`. `GET /runs` and `GET /runs/{ref}` carry the caller's `snoozed_until` only while the snooze lasts. Releasing or deleting a session drops every snooze on it. A released session answers 409 |
| `POST`, `DELETE` | `/runs/{ref}/done` | Mark the session done for the caller, or clear the mark. The console files a done session under Earlier in that person's list. Both are idempotent and answer 200 `{run_ref, snoozed_until?, done}`. `GET /runs` and `GET /runs/{ref}` carry the caller's `done`. Releasing or deleting a session drops every mark on it. A released session answers 409 |
| `GET`, `POST`, `PUT` | `/runs/{ref}/queue` | The messages lined up for the session while its tool works. `GET` answers `{run_ref, items, hold?, can_steer}`: the queued messages in sending order (`id`, `text`, `queued_by`, `queued_at`), why the first has not gone (`hold`: `turn`, `approval`, `limit` or `stopped`; absent when it goes at once) and whether the tool takes a message into a running turn (`can_steer`). `POST` takes `{text}` and answers 201 with the message; when the session is not busy it is sent at once. `PUT` takes `{ids}`, every queued id once in the new order (400 otherwise), and answers the queue. The queue is saved on the server, so it survives a reload and shows on every device. When a turn ends the first message is sent once through the session's input; a stopped, interrupted or limited session, and one waiting for an approval, keep the queue. Same write permission and edition rule as `/input`; a session started by a work item and a released session answer 409. Releasing a session drops its queue, and the answer of `POST /runs/{ref}/cleanup` lists the messages it discarded as `discarded_queue` |
| `PATCH`, `DELETE` | `/runs/{ref}/queue/{id}` | Change the text of one queued message (`{text}`, 200 with the message) or take it out of the queue without sending it (200 with the queue). An id that is not in the queue answers 404 |
| `POST` | `/runs/{ref}/queue/{id}/steer` | Send one queued message now through the session's input: into the running turn when the tool takes one (`can_steer`), otherwise as the next message. The message leaves the queue first, so it goes once; a refusal before anything was written puts it back. Answers 202 `{accepted}`. A tool that takes no message into a running turn answers 409 while its turn runs |
| `PUT` | `/runs/{ref}/name` | Body `{name}`. Renames the session. The name is trimmed; an empty one clears it and clients fall back to their own naming. More than 200 characters, a control or invisible character (format, line or paragraph separator: zero-width spaces, direction overrides) or a missing `name` answers 400. 200 run. The name is the session's, so every reader sees it. Recorded in the audit ledger as `sessions.run.renamed` with the run reference, not the name; a name that is already the session's changes nothing and records nothing |
| `POST` | `/runs/{ref}/title` | Optional body `{style, if_untitled}`. Cuts the session's title from the first message the person sent, on the engine's own host: no model is asked, so nothing leaves the session's provider, and a session signed in with the person's own account is titled the same way as one on a key. `style` is `short` (28 characters), `sentence` (60, the default; the console's "Normal") or `long` (100); any other value answers 400. `if_untitled: true` makes a title only for a session with neither a name nor a title, and answers the run unchanged otherwise, without reading the transcript. 200 run, with `title`. A session whose transcript holds no message of the person's yet answers 409, and keeps no title; so does a session given vault secrets, a repository credential, a managed credential or a package registry token, whose transcript cannot be proven free of them, and a session of a tool other than Claude Code and Codex answers 422: the title is refused wherever the conversation is not read. The title is display-only: it is not the name and never an address, so two sessions may share one and a rename never loses to it. It needs `sessions:run:write` and, because it is cut from the conversation, `sessions:output:read`; a caller without the second answers 403. The title is the person's own words, so `GET /runs` and `GET /runs/{ref}` and the replies that return a run carry `title` only for a caller who holds `sessions:output:read` on that run, decided for each run; any other caller reads the run without it. Recorded in the audit ledger as `sessions.run.title_generated` with the run's reference and not the title; cutting it records no separate message read. |
| `POST`, `DELETE` | `/runs/{ref}/archive` | Archive the session, or unarchive it. No body. 200 run, with `archived_at` while it is archived. Archiving neither stops nor removes the session. The mark is the session's, not the caller's: it is archived for everyone. `GET /runs` is not narrowed by it; each client leaves archived sessions out of its list. Both calls are idempotent: archiving an archived session keeps its `archived_at` and records nothing. Recorded in the audit ledger as `sessions.run.archived` and `sessions.run.unarchived` |
| `POST` | `/runs/{ref}/move` | Body `{provider_profile_ref}`. Rebinds a stopped or failed Claude Code session to another profile of the same tool and carries its conversation. Starts nothing. 200 run |
| `GET` | `/runs/{ref}/handoff` | The summary a handoff would start from: `summary`, `source_driver`, `turns`, `transcript_bytes`. Read-only, recorded in the audit ledger. A running session is read as it is; one that has not started answers 409 |
| `POST` | `/runs/{ref}/handoff` | Body `{provider_profile_ref, summary?, name?}`. Starts a new session in that profile, linked to this one. 201 run plus `summary_state` (and `first_turn` when it is not `delivered`); 202 while the launch waits for approval |
| `GET` | `/runs/{ref}/activity` | What a Claude Code or Codex session's tool is doing, read from its own native record on each request: `agents`, `tasks` (commands and background work), `files` (path, `added`, `removed`, `created`), `todos`, `skills`, `mcp_servers`, `model`, `context_tokens`, nullable `context_window` when the tool reports it and `context_percent` (percentage for Claude Code, null when unknown), `compactions`, and `transcript` (`read`, `pending` before the tool wrote it, `missing` when it should be there). Agents and tasks are `running`, `completed`, `failed`, `stopped` or `scheduled`. Codex uses its rollout file without launching another app-server; context tokens describe the latest request, not cumulative usage. Read-only; recorded in the audit ledger once per reader and session every 15 minutes. An unsupported tool answers 422; a session given vault secrets, a repository credential or a managed credential answers 409, and so does a session that used a package registry token when it is stopped or runs after a resume that follows a launch that used one |
| `GET` | `/runs/{ref}/activity/agents/{agent}` | One Claude Code subagent's transcript in plain turns (`role`, `text`). Read-only, recorded in the audit ledger. 404 for an unknown agent. Codex reports child state in the root Activity answer; child transcript reads are not offered |
| `GET` | `/runs/{ref}/messages` | The messages of a Claude Code or Codex session, read on each request from the tool's own transcript, in pages: `items` (`id`, `role` `user` or `assistant`, `at`, `first_line`), `total`, `next`, `transcript` (`read`, `pending`, `missing`). By default only what the person sent; `role=all` adds the tool's answers. `q` keeps messages with that phrase anywhere in them (case-insensitive, at most 256 bytes); `at` (RFC 3339) keeps messages at or after that time, so the first item is the anchor; `order` is `asc` (default) or `desc`, `limit` at most 200 and `cursor` the id a page continues from (`next`); items are always oldest first. `id` reads one message whole (`text`, and `later_same`, how many later messages of the same sender repeat its first line; no filter applies and `total` is the session's message count). `text=1` adds `text` to every item of the page, bounded at 64 KiB per message as a read by `id` is, and no `later_same`; a text cut at that bound ends in ` [...]` and its item carries `clipped: true`, in a read by `id` too; such a page holds at most 50 messages; follow `next` to copy a whole conversation. A message's `id` is its position among the session's messages and does not change as the transcript grows. Read-only; recorded in the audit ledger once per reader, session and window of 15 minutes, as `sessions.run.messages`, and a read with `text=1` as `sessions.run.messages.text`. An unsupported tool answers 422; a session given vault secrets, a repository credential or a managed credential answers 409; an unknown `id` 404; an invalid parameter 400 |
| `DELETE` | `/runs/{ref}` | Needs `sessions:run:admin`. Deletes a released session's record. A session that is not released answers 409 `session must be cleaned before delete (state=…)`. Recorded in the audit ledger as `sessions.run.deleted`, in the transaction of the delete |
| `GET` | `/runs/{ref}/diff` | A worktree session's branch against where it began: `branch`, `base`, `head` and the changed `files` (`path`, `status`). 404 without a worktree |
| `GET` | `/runs/{ref}/diff/file?path=` | One path's text at `base` and at `head` (`original`, `modified`, at most the smaller of the workspace's `max_read_bytes` and 64 KiB each) |
| `GET` | `/source-items?workspace_ref=&url=` | The GitHub issue or pull request a start names: `url`, `kind`, `number`, `title`, `body`. Needs tenant administration; read with the approved repository binding of that workspace. See Starting from a GitHub issue or pull request |

For Codex Code Mode calls whose plan arguments are not retained in the rollout, Activity also reads the latest `turn/plan/updated` notification already received by the live bridge. A browser reload uses the same server-side session; no app-server is launched for a refresh. After the live handle is gone, only data retained by Codex itself can be read.

For Claude Code, the window comes from its native result-frame `modelUsage[model].contextWindow`, retained across stop and reconnect. A matching, unambiguous report supplies `context_percent` as `100 × context_tokens / context_window`; otherwise both fields are `null` and the CLI and console display tokens with an unknown-window notice. A new tool launch clears the previous window report.

Workflow tasks may include `workflow`: `run_id`, `name`, `duration_ms`, `agents_total`, `agents_done`, `agents_failed`, and `phases`. Each phase has `index`, `title`, optional `detail` and `state`, and the same agent counts. Counts describe agents observed so far. While live, a phase state describes observed running or failed agents. Agent completion does not prove that the script has left its phase, so it does not mark the phase completed or identify a current script step. The final workflow summary supplies completed phase states.

Live workflow snapshots come from Claude Code's `system/task_progress` output, shared with the attach stream. The tool's completed `workflows/<runId>.json` fills the final summary under the transcript's home, link and size rules. Summary reads are bounded at 8 MiB per file and 64 MiB per request; larger native logs or results make the summary unreadable while preserving available live progress. Unreadable summaries and gaps in the bounded live buffer contribute to `unreadable`.

### Moving a session and handing it off

A session continues in another account in one of two ways, and the console and
`olivares session move` / `olivares session handoff` offer both. A move carries the
stored conversation of a Claude Code session to another profile of the same tool, so only
Claude Code sessions can be moved. A handoff starts a new session from the conversation
of a Claude Code, Codex, Gemini CLI or OpenCode session.

A run carries `limit` when its tool ended the last turn on a limit:
`{kind, resets_at, account_ref}`. `kind` is `usage` (the account's plan allowance
ran out) or `rate` (the provider is throttling requests). `resets_at` is RFC 3339,
present when the tool reports when the limit lifts and absent otherwise, and
`account_ref` is the profile the turn ran under. Claude Code supplies the fact in
its own result (`api_error: usage_limit_reached` or HTTP 429) and the reset time in
its `rate_limit_event`; Codex supplies it in a failed turn typed `usageLimitExceeded`
or `rateLimitExceeded` and the reset time in its `account/rateLimits/updated`
windows; when two windows are used up, the later reset is the one recorded, and a reset
time more than a year from now is left out. The ledger gains one `limit_reached` event
when the fact is first recorded; the same fact met again adds none.
The next turn that succeeds clears `limit`, and so does a move to another account. A
result of any other kind never sets it.

`POST /runs/{ref}/move` keeps the same session. It needs a `stopped` or `failed`
session whose conversation is in its account home, and a target profile of the
same tool that a launch under it would accept (active, this node's environment,
an authorized source). The engine copies that one conversation, the transcript
and its subagent and tool-result files, to the target home, never a login, a
setting or another conversation, and rebinds the run row, the live row and the
ledger in one transaction. The run keeps its `run_ref` and its provider
conversation id; the ledger gains a `moved` event whose detail names both
profiles by reference and counts the files carried, and a failed session keeps
its `reason`. The source home is only read. A refusal writes nothing and removes
what the move made:

- 409 for a running session, a session given vault secrets, a repository
  credential, a managed credential or a package registry token (what it printed cannot be proven free
  of them), a row whose home
  its profile does not own, a conversation id already bound to another session
  under the target, a target that holds a different version of the conversation,
  a target of another tool, or a file that changed while it was carried
  (retry);
- 422 for a link, a second name for a file, or a special file inside the
  conversation, and for another tool's session.

A copy of the same conversation left on the target by an earlier move is
replaced by the newer one. Nothing starts: the next `POST /runs/{ref}/resume` is
the ordinary one, so the target profile's policy, budgets and approvals apply to
it. A session that has no conversation yet moves with nothing to carry and
starts again on the target.

`GET /runs/{ref}/handoff` and `POST /runs/{ref}/handoff` continue in another
tool, where no native resume exists. They read the stored conversation of a
Claude Code, Codex, Gemini CLI or OpenCode session (a Grok session answers 422).
The session may be `stopped`, `failed`, `running` or `idle`; one that has not
started answers 409. The preview reads a running session as it is. The POST stops
a running session first, the way `POST /runs/{ref}/stop` does, and reads its
conversation once its tool has exited; every refusal that does not need the stop
(the target profile, vault secrets, a repository credential) comes before it, and
a refusal after it says that the session was stopped and can be resumed. Codex is
read from its rollout, Gemini CLI from its chat log, and OpenCode through its own
`opencode export`, run once and offline under the session's profile.
The preview is generated from the stored conversation without a model, so the
person can review and edit it, and each read of it is recorded in the audit
ledger as `sessions.run.handoff_preview`. The POST starts a new session in the
chosen profile, in the same workspace and under the same template, whose first
turn is the reviewed `summary` (generated when absent) and the path of the
transcript, written under the target profile's configuration home under a name
of its own. The transcript carries messages in full up to 256 KiB each, tool
calls and results shortened, and says how many records it could not read. The
new run reports `handoff_of`, and the original's ledger gains a `handed_off`
event. The answer carries `summary_state`:

| `summary_state` | Meaning |
|---|---|
| `delivered` | The first turn reached the new session. |
| `not_delivered` | It did not cross. `first_turn` holds the text to send. |
| `unknown` | It was attempted and the outcome was not confirmed; check before sending `first_turn` again. |
| `waits_for_approval` | The launch is held for a human (202); no first turn is sent automatically. |

`first_turn` is present only when the state is not `delivered`, because it names
the attachment's path. The source conversation is refused with 409 when its
session was given vault secrets, a repository credential, a managed credential or a
package registry token, because a handoff
sends it to another provider, and with 422 above 256 MiB or when it holds one
record larger than 32 MiB.

### Worktree option

`POST /runs` accepts an optional boolean `worktree`. When it is true and
`workspace_ref` names a registered workspace that is a git repository's top
folder, the session works in a new git worktree and branch of that repository
(`workspace_path` is the worktree and the run reports `worktree_branch`).
Absent, null or false keeps today's behavior. The engine answers 422 for a
workspace that is not a git work tree, is not the repository's top folder, has
no commit, is read-only or has read-only folders, whose git configuration names a
filter or includes a file, for a non-native isolation, and for a launch with no
workspace; and 503 when the node has no worktree directory.
`POST /runs/{ref}/resume` returns to the same worktree.

`POST /runs/{ref}/cleanup` accepts an optional body `{"discard_worktree": true}`;
with no body it is the call it always was, and the server still takes any body:
only an explicit `true` confirms. A session with a worktree is released when its
branch is merged into the workspace's current branch, the worktree is on that
branch and it has no uncommitted files; otherwise the call is refused with 409
(unmerged work, a detached HEAD, a worktree the node cannot reach) and the run
stays stopped, so it can be repeated with `discard_worktree` to remove the
worktree and branch anyway, or, for a worktree the node cannot reach, to release
the session and leave the worktree where it is. The ledger's `cleaned` event says
what happened to the worktree, with the branch tip.

### Starting from a GitHub issue or pull request

`POST /runs` accepts an optional string `source_url`: the page of a GitHub issue
or pull request (`https://github.com/<owner>/<repo>/issues/<n>` or `/pull/<n>`;
a query, a fragment and a pull request's `/files` or `/commits` are ignored). It
implies `worktree`, so it needs a registered workspace that is a git repository's
top folder, and the branch is named after the item, `olivares/issue-<n>-<suffix>`
or `olivares/pr-<n>-<suffix>`. The run reports `source_url` in one canonical form,
and the console and `olivares session show` link back to it. A value that is not
such a page, names another host or comes with `repository_url` answers 400. The
worktree starts at the workspace's current commit, also for a pull request.

`GET /source-items?workspace_ref=<ref>&url=<page>` reads the item for a start: it
answers `url`, `kind` (`issue` or `pull_request`), `number`, `title` and `body`
(at most 32 KiB). It needs `sessions:run:write` and tenant administration, as a
repository credential for a session does. The engine reads the item with the
approved GitHub repository binding that lists the repository for that workspace,
using an installation token that can read issues and pull requests and nothing
else, and revokes it after. It answers 422 when no such binding lists the
repository, 404 when the item does not exist or the credential cannot see it, 503
when the host fails or the node has no Git-host custody, and never carries the
host's own error text. `olivares session start --from <page>` and the console's
GitHub issue or pull request field read the item first, so a page that cannot be
read leaves no session behind, and send its title and body as the first message.

### Approving a folder's setup command

A setup command belongs to the registered folder, in engine storage. Repository
files and environment variables cannot change it. Storing and approving the
command never runs it; a worktree start does, as described below.

- `PUT /workspaces/{ref}/setup-command` takes `{"command":"npm ci"}`. The exact
  UTF-8 text is preserved, including whitespace; at most 8192 bytes, without NUL.
  An empty string clears the command. Requires workspace administration.
- `GET /workspaces/{ref}` returns `setup_command`, `setup_command_sha256`, and,
  after approval, `setup_approval` with `sha256`, `actor`, and `at`.
  `setup_approved` is true only while that approval covers the current text.
  After an edit `setup_approval` stays as evidence, so test `setup_approved`,
  never the presence of `setup_approval`.
- `POST /workspaces/{ref}/setup-command/approve` takes `{"sha256":"<reviewed hash>"}`.
  It requires workspace administration and refuses an agent identity. The hash
  must match the currently stored text; a concurrent edit returns 409. Approval
  and its `sessions.workspace.approve-setup` audit entry commit together.

A worktree start returns 409 when the command has no matching approval, before
creating a worktree. Its sentence names the command and the approval step.
Changing one character, including whitespace, requires approval again. The last
approval remains as evidence; its hash must equal `setup_command_sha256` to apply,
so setting the command back to text that was approved before applies that approval
again. Anyone who can read the folder can read the command, so keep secrets out of it.
`POST /runs` with `"no_setup":true` skips setup for that start only. A handoff of
a session that has a worktree starts a new worktree and needs the same approval;
it has no skip option. Starts without a worktree keep their existing behavior.

Once the new worktree exists and before the session's first turn, the approved
command runs once with `/bin/sh -c`, in the worktree folder. It runs under the
folder's confinement (the worktree and its git metadata) and the session's network
policy. The same allowlist applies to downloads: a key-bound session reaches only
its provider and explicitly allowed hosts, so allow the package registry before
running an installer. It gets none of the session's environment, credentials,
provider account homes or credential files; its `PATH` is the session's, so the
programs of the session's profile are found. The permission mode does not
restrict it: the mode governs the tool's turns, not the command the person
approved, so a plan-mode session still prepares its worktree. A non-zero exit, or a stop at the time
limit, stops the start, and the run's ledger records a `worktree_setup` event with the
command's last output. The request waits up to 20 seconds for the command. A command that
ends in that time answers as a start always has: when it fails, the response is 422 and
carries the last output. A command still running then belongs to the session: the response
is 201 with `state` `pending`, the command keeps running under the session's confinement,
and a stop request ends it. Its time limit is 10 minutes. The session's first input waits up to 30 seconds for
the command to finish; after that it is refused with 409 (`session is not running`,
`state=pending`) and the caller sends it again. The console holds the first message until
the session is ready and sends it then, so a message sent while an installer runs arrives
after it, never into a half-prepared folder. A command that fails, hits the limit or is stopped leaves the run `failed` (`stopped` for
a stop request) with the same event and the last output; no input reaches the tool.
A resume waits for its command to end and keeps the 20-second limit.
On Linux, anything the command leaves running is stopped when it finishes.
On other platforms, the start is refused rather than risking detached processes.
Files the command writes are checked like files that were already there: project
agent configuration that can run programs or redirect traffic stops the start.

The command runs for the first start of a worktree session. A start that waits for
approval runs it when the approval lets it start. Resuming after a failed setup runs
it again before the session does; if it cannot be approved or completed, the session
does not start. A resume after a successful setup never runs it again, including after
a later launch failure. If the guard could not confirm process cleanup, resume is
refused; stop the leftover processes and start a new worktree. `no_setup` skips setup
for that start and its later resumes.
A worktree run from an older release, before setup state was recorded, instead runs
an approved setup once if it never started.

With the CLI, use `olivares agent workspace setup <ref> --command 'npm ci'`,
read it with `olivares agent workspace setup <ref>`, and approve the displayed
hash with `olivares agent workspace approve-setup <ref> --sha256 <hash>`.
`olivares session start --worktree --no-setup` skips setup once.

### Starting a worktree at a commit or branch

With `worktree`, `POST /runs` also accepts an optional `worktree_from`: a full commit
id (40 or 64 lowercase hex digits) or a local branch of the workspace's repository.
The new worktree and its own new branch start there instead of at the workspace's
current commit; the named branch and the workspace checkout do not move. This is how
a receiver opens the work a handoff names, whose content may carry an optional
`branch` and `sha`. Git resolves the value to a full commit id and only that id is
used. The engine answers 422, before anything is created, for a commit the repository
does not hold, a branch it does not have, a revision expression, a range, an option,
and a `worktree_from` without `worktree`. Absent, a launch starts at the workspace's
current commit as before.

`GET /runs/{ref}/diff` lists the paths the session's worktree branch changed since it
left the workspace's current commit (`base` is their merge base, `head` the branch
tip, both full commit ids; at most 200 paths, `truncated` says when more exist), and
`GET /runs/{ref}/diff/file?path=` returns one path's text at `base` and at `head`,
empty where the file does not exist there. It reads git's committed objects with
plumbing commands, so uncommitted edits are not in it, and it needs the same
permission as reading the run. It keeps the workspace's own file rules: a path outside
the allowed subpaths is not listed and answers 404, a workspace whose DLP posture
denies answers 403 (and the read is audited like a workspace file read), and a file over
16 MiB answers 413. A session without a worktree, and a run of another tenant, answer
404; a branch that is gone, or shares no history with the workspace, answers 409.
`worktree_from` also answers 422 for a commit id that no branch, tag or remote branch of
the repository holds.

A comment on one line of that diff goes back to the agent as its next message, through `POST /runs/{ref}/input` like any other input. Add `review_comment` (`path`, a `line` from 1 and the `side`: `original` for the file at `base`, `modified` for the file at `head`) to name the place. A malformed place, or one sent with `attachments`, answers 400 and sends nothing. Once the input is accepted, the ledger records `sessions.run.review_comment` with the run, the path, the line and the side; the comment itself is the message the tool received and is not copied into the ledger. `olivares session diff <session>` prints the same diff as a unified diff.

Reconnect after a dropped attach is `GET …/attach?from={last+1}` on the **same**
live process. After process loss, attach tells you the session is not live.
Resume starts a new generation. Reconnect does not invent a replacement
process (SDD R04).

### Agent configuration a repository carries

A cloned repository can carry agent configuration that runs a program or
redirects traffic. The engine treats it as untrusted input when a session
starts in a folder. It reads the files below, never executes them, and
collects only the keys listed. An empty value (`{}`, `[]`, `""`, `false`,
`null`) configures nothing and is not flagged.

| Tool | File | Flagged |
| --- | --- | --- |
| Claude Code | `settings.json` and `settings.local.json` in the `.claude` folder | `hooks`, `apiKeyHelper`, `statusLine`, `mcpServers`, `enableAllProjectMcpServers`, `enabledMcpjsonServers`, `awsAuthRefresh`, `awsCredentialExport`, `otelHeadersHelper`, `fileSuggestion`, `enabledPlugins`, `extraKnownMarketplaces`, and `env` entries with a name listed in the last row |
| Claude Code | `.mcp.json` | `mcpServers` |
| Codex | `.codex/config.toml` | `hooks`, `notify`, `mcp_servers`, `model_providers`, `otel`, any top-level `*_base_url`, and `shell_environment_policy.set` entries with a name listed in the last row; the same keys under `profiles.<name>` |
| Gemini CLI | `.gemini/settings.json` | `hooks`, `mcpServers`, `toolDiscoveryCommand`, `toolCallCommand`, `proxy`, `tools.discoveryCommand`, `tools.callCommand`, `mcp.serverCommand`, `telemetry.otlpEndpoint` |
| Gemini CLI | `.gemini/.env`, `.env` | entries with a name listed in the last row |
| Cursor CLI | `cli.json` in the `.cursor` folder | `permissions`, `approvalMode`, `autoAcceptWebSearch` |
| Cursor CLI | `mcp.json` in the `.cursor` folder | `mcpServers` |
| Cursor CLI | `hooks.json` in the `.cursor` folder | `hooks` |
| Cursor CLI | `settings.json` and `settings.local.json` in the `.claude` folder, because Cursor also loads the hooks of Claude Code's project settings | `hooks` only; the other keys of those files belong to Claude Code |
| OpenCode | `opencode.json`, `opencode.jsonc` | every top-level key except `$schema`, `theme`, `keybinds`, `logLevel`, `model`, `small_model`, `username`, `share`, `autoupdate`, `snapshot`, `layout`; every `{file:…}` and `{env:…}` token anywhere in the text, with a `{file:}` bound to the sha256 of the file it names |
| OpenCode | `.opencode/` | the whole folder: every file by its sha256 (a link inside the folder by its target), except its own `.gitignore`; its `opencode.json` and `opencode.jsonc` as in the row above, with a `{file:}` read from `.opencode/` |
| Environment names | | `NODE_OPTIONS`, `NODE_EXTRA_CA_CERTS`, `NODE_TLS_REJECT_UNAUTHORIZED`, `LD_PRELOAD`, `LD_LIBRARY_PATH`, `LD_AUDIT`, `DYLD_INSERT_LIBRARIES`, `BASH_ENV`, `PATH`, `PYTHONPATH`, `GIT_SSH_COMMAND`, `GIT_EXTERNAL_DIFF`, `SSL_CERT_FILE`, `CLAUDE_CODE_SHELL_PREFIX`, `ANTHROPIC_CUSTOM_HEADERS`, `AWS_ENDPOINT_URL*`, and any name ending in `_BASE_URL`, `_API_BASE`, `_ENDPOINT` or `_PROXY` (`NO_PROXY` only narrows a proxy and is not flagged) |

Before approving `.opencode/`, review its `package.json` and run `OpenCode=1.19.4; npm install --ignore-scripts --save-exact @opencode-ai/plugin@"$OpenCode"` from that folder. The package version matches pinned OpenCode 1.19.4; install scripts stay disabled during preparation. The check requires `node_modules/` and a `package-lock.json` whose root locks `@opencode-ai/plugin` and every declared dependency. Otherwise the session is refused with an instruction to install dependencies first. The installed tree is part of the approval hash; changes require a new approval. OpenCode then leaves the prepared dependencies unchanged.

A file that exists but cannot be read as its format (not valid JSON or TOML,
over 1 MiB, nested deeper than 64 levels, not a regular file) counts as
flagged, because it cannot be shown safe. In TOML every segment of a dotted key
or table header is one level, as in the parsed document, and adds to the
brackets around it: a 30-segment header over 35 nested inline tables is 65
levels. Once a file has an `[[array]]` header, header segments count twice,
because each may name an array and its element. An oversized file is
hashed in full on every check, except an OpenCode file, which is refused after
the bounded parse read. A folder with nothing flagged starts as before.

- `GET /workspaces/{ref}/project-config` lists the flagged keys per file with
  their values (a password inside a URL is masked) and the `sha256` of exactly
  that flagged part, over the files of all the tools above, plus `approved` and the
  last `approval`. It reads the folder itself; a worktree started from another
  commit can carry different tracked files, and its refusal names its own hash.
- `POST /workspaces/{ref}/project-config/approve` takes `{"sha256":"<reviewed hash>"}`,
  requires workspace administration and refuses an agent identity. The approval
  stores who, when and the hash, with a `sessions.workspace.approve-project-config`
  audit entry in the same transaction.

A start or resume of a tool that reads a flagged file returns 409 unless the
stored approval equals the current hash. Changing one character of a flagged
value, or adding or removing a flagged key, requires approval again; changing
keys that are not flagged does not. There is no option to skip the check.

Some configuration cannot be bound by a hash, so no approval lets a session use
it. Its entry carries a `refused` reason, and every start or resume of that tool
returns 409 until the configuration is changed or removed, whatever is approved:
an OpenCode file with an `{env:…}` token; an OpenCode file that is not valid JSONC; a `{file:…}` that names a file outside
the folder, under `~`, at an absolute path, with spaces around its path, or one that is not a regular
file of at most 1 MiB; `{file:…}` tokens that name more than 8192 files or 64 MiB together; expanded substitution content over 64 MiB; a nonempty `plugin` or `instructions` key; an OpenCode file over 1 MiB or that is not a regular file; an `opencode.json(c)` or
`.opencode` that is a link out of the folder; a `.opencode` that is not a folder; and a `.opencode/` folder over
8192 entries, 64 MiB or 1024 folders, or with a link out of it, a link loop, a
non-regular entry, or a folder or file the check cannot read.

A Claude Code session that carries the engine's hooks is launched with
`--setting-sources ""`; on Claude Code 2.1.299 that session loads no project or
local settings file and no project `.mcp.json`, so such a start is not refused.
A Claude Code launch without the engine's hooks (remote control, or an engine
with no hook endpoint) reads those files and is checked. Codex honors
`.codex/config.toml` only for a project trusted in its account home and ignores
an untrusted project's file; the engine cannot ask the tool, so it checks that
file and a person approves it once. Gemini CLI 0.64.0 started as the engine
starts it (`gemini --acp`) ignores `.gemini/settings.json` and `.gemini/.env`
of a folder that its account home does not trust, and in a trusted folder runs
`tools.discoveryCommand`, the `hooks`, and the `mcpServers` commands and takes
the base URL from `.gemini/.env`; the engine checks those files for the same
reason. It also loads files of parent folders, which this check does not read.

With the CLI, use `olivares agent workspace project-config <ref>` to read it and
`olivares agent workspace approve-project-config <ref> --sha256 <hash>` to approve.

## Transport (Community)

Each official CLI is launched on the transport its own operate form needs, and
`cliruntime.LaunchTransport` declares which. All three forms today are stdio
protocols, so the composition root wires `sessions.NewProcRunner()`: stdin,
stdout and stderr are pipes and stay distinct streams.

**Claude Code refuses a terminal on stdin.** Its `--print` stream-json form
answers `Error: Input must be provided either through stdin or as a prompt
argument when using --print` and exits 1 without a protocol frame. The Codex
app-server and the Grok stdio agent speak JSON-RPC over the same streams. A
terminal is what an interactive form would need; `sessions.NewPTYRunner()`
stays available on Linux for that, with stdin/stdout on a raw local PTY and
stderr on a pipe. Neither runs container or sandbox isolation. A host with podman or Docker adds a
container runner for `isolation=container` on the stdio form; sandbox stays refused
(see [Topology 4](/how-to/run-claude-code-with-olivares/#topology-4--olivares-native-claude-in-a-per-session-container)).

The driver contract lives in `modules/sessions/cliruntime`. Kinds: `claude`,
`codex`, `grok`. Conformance runs against an in-process fake always, and
against a local peer on both transports. When `claude` / `codex` / `grok` are
on PATH, a separate battery drives the whole contract against the real binary
— launch, attach, input, output, reconnect, stop, resume — and sends no model
turn: every handshake is a protocol handshake, and each launch gets a fresh
HOME and configuration home.

## Related

- [Operate a provider session](/how-to/operate-provider-sessions/)
- [Module II — live operation](/reference/modules/ii-sessions/)
- [Connect Claude Code](/how-to/connect-claude-code/)
- [Beta OpenAPI](/reference/api-beta/)
