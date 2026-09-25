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
They already exist in the [beta OpenAPI document](/reference/api-beta/). v26.10 is pending. It
adds the driver contract, the transport each launch form needs, and journeys
J01–J08 as tests.

## Edition boundary

| Edition | What it does | What it does not do |
|---|---|---|
| **Community (this page)** | Owned local child: launch, stdin/stdout/stderr, attach with cursor, resume of the exact conversation, reconnect of a live stream, stop with observed exit status. Session rows and evidence stay in module II. | Multi-pane Identity & Scale engine, mTLS agent listener, commercial input sessions, xterm UI chunk |
| **Identity & Scale overlay** | Commercial session-cockpit engine (listener, panes, recording ledger). Routes live under `/v1/m/session-cockpit/` when the add-on is present. | It does not replace `/v1/m/sessions/runs` |

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
