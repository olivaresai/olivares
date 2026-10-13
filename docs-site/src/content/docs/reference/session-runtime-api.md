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

Existing enforcement on the module routes:

| Permission | Routes |
|---|---|
| `sessions:run:read` | `GET /runs`, `GET /runs/{ref}`, `GET /runs/{ref}/events`, `GET /runs/{ref}/attach`, `GET /runs/{ref}/handoff`, `GET /runs/{ref}/activity`, `GET /runs/{ref}/activity/agents/{agent}` |
| `sessions:run:write` | `POST /runs`, `POST /runs/{ref}/input`, `POST /runs/{ref}/interrupt`, `POST /runs/{ref}/stop`, `POST /runs/{ref}/resume`, `POST /runs/{ref}/move`, `POST /runs/{ref}/handoff` |
| `sessions:run:admin` | `POST /runs/{ref}/cleanup`, `DELETE /runs/{ref}` |

A viewer can list. Create, input and stop require write. The authorizer on the
route is the enforcement point; a missing permission is 403.

## Routes the console reads

Base: `/v1/m/sessions`. Authenticate. Send `X-Olivares-Tenant`.

| Method | Path | Result |
|---|---|---|
| `GET` | `/runs` | Page of managed runs. Optional `state`, `live_ref`, `claude_session_id` (legacy) |
| `GET` | `/runs/{ref}` | One run. `state` is derived. `exit_code` is observed. No fabricated success field |
| `GET` | `/runs/{ref}/events` | Lifecycle evidence rows |
| `GET` | `/runs/{ref}/attach?from={seq}` | SSE: `output` frames, `lag` if the ring evicted below the cursor, `end` or a not-live `notice` |
| `POST` | `/runs/{ref}/input` | stdin. Stream-json uses `line`/`message`. Codex/Grok use `text`. 202 `{accepted:true}` |
| `POST` | `/runs/{ref}/stop` | SIGTERM then SIGKILL of the process group. Observed exit status on the row |
| `POST` | `/runs/{ref}/resume` | New process generation. Exact stored conversation. No silent new-conversation fallback |
| `POST` | `/runs/{ref}/interrupt` | Cancel the active turn. The process stays |
| `POST` | `/runs/{ref}/move` | Body `{provider_profile_ref}`. Rebinds a stopped or failed Claude Code session to another profile of the same tool and carries its conversation. Starts nothing. 200 run |
| `GET` | `/runs/{ref}/handoff` | The summary a handoff would start from: `summary`, `source_driver`, `turns`, `transcript_bytes`. Read-only, recorded in the audit ledger. A running session answers 409 |
| `POST` | `/runs/{ref}/handoff` | Body `{provider_profile_ref, summary?, name?}`. Starts a new session in that profile, linked to this one. 201 run plus `summary_state` (and `first_turn` when it is not `delivered`); 202 while the launch waits for approval |
| `GET` | `/runs/{ref}/activity` | What a Claude Code session's tool is doing, read from its own transcript on each request: `agents`, `tasks` (background shell commands, monitors, workflows, wakeups), `files` (path, `added`, `removed`, `created`), `todos`, `skills`, `mcp_servers`, `model`, `context_tokens`, `compactions`, and `transcript` (`read`, `pending` before the tool wrote it, `missing` when it should be there). Agents and tasks are `running`, `completed`, `failed`, `stopped` or `scheduled`. Read-only; recorded in the audit ledger once per reader and session every 15 minutes. Another tool answers 422; a session given vault secrets or a repository credential answers 409 |
| `GET` | `/runs/{ref}/activity/agents/{agent}` | One subagent's transcript in plain turns (`role`, `text`). Read-only, recorded in the audit ledger. 404 for an unknown agent |
| `GET` | `/runs/{ref}/diff` | A worktree session's branch against where it began: `branch`, `base`, `head` and the changed `files` (`path`, `status`). 404 without a worktree |
| `GET` | `/runs/{ref}/diff/file?path=` | One path's text at `base` and at `head` (`original`, `modified`, at most the smaller of the workspace's `max_read_bytes` and 64 KiB each) |

### Moving a session and handing it off

A session continues in another account in one of two ways, and the console and
`olivares session move` / `olivares session handoff` offer both. Only Claude Code
sessions have a stored conversation the engine reads, so only they can do either.

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

- 409 for a running session, a session given vault secrets or a repository
  credential (what it printed cannot be proven free of them), a row whose home
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
tool, where no native resume exists. Both need a `stopped` or `failed` session.
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
session was given vault secrets or a repository credential, because a handoff
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

Reconnect after a dropped attach is `GET …/attach?from={last+1}` on the **same**
live process. After process loss, attach tells you the session is not live.
Resume starts a new generation. Reconnect does not invent a replacement
process (SDD R04).

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
stderr on a pipe. Container and sandbox isolation stay refused on both.

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
