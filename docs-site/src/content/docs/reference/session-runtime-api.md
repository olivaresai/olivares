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
| `sessions:run:read` | `GET /runs`, `GET /runs/{ref}`, `GET /runs/{ref}/events`, `GET /runs/{ref}/attach` |
| `sessions:run:write` | `POST /runs`, `POST /runs/{ref}/input`, `POST /runs/{ref}/interrupt`, `POST /runs/{ref}/stop`, `POST /runs/{ref}/resume` |
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
| `GET` | `/runs/{ref}/diff` | A worktree session's branch against where it began: `branch`, `base`, `head` and the changed `files` (`path`, `status`). 404 without a worktree |
| `GET` | `/runs/{ref}/diff/file?path=` | One path's text at `base` and at `head` (`original`, `modified`, at most the smaller of the workspace's `max_read_bytes` and 64 KiB each) |

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
