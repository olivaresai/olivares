---
title: Session permissions
description: The permissions a session can run under, what each one hands Claude
  Code, Codex, OpenCode, Grok Build and Gemini CLI, and where a tool asks
  instead.
---

Every session runs under one permission. Choose it by name in the New session
form, under **Settings → Permissions**, or with
`olivares session start --permission <name>`. The names are the same everywhere.

## The default

A session starts under **Edit files and run commands**: the agent reads, edits
files and runs commands in the session folder without asking, and every other
tool is refused. No default is ever **Full**. Full is chosen on purpose, for one
session, and only by someone who administers sessions.

## A default for a folder

A folder can store the permission its sessions start under. Open the folder in
**Workspaces**, choose **Default permission** and save. The New session form and
`olivares session start` use it when you name no permission, and say so. Naming a
permission yourself, or a template with `--template`, wins. Making **Full** a
folder's default takes the same authority as running Full, and every change is
recorded in the audit ledger. The New session form offers a Full default in the
warning style; a command line has no screen to show that on, so
`olivares session start` starts in Full from a folder only with
`--permission full`.

## What each permission does

The table says what each tool is handed when the session starts, in the tool's
own words. It is written from the code that builds the launch, so it is the
launch.

<!-- permission-table:start -->
| Permission | What it does | Claude Code | Codex | OpenCode | Grok Build | Gemini CLI |
|---|---|---|---|---|---|---|
| **Read only**<br>`read-only` | Reads and plans. Every change is refused. | `--permission-mode plan` | `sandbox read-only`, `approval on-request` | `permission deny` | `sandbox read-only`, `mode plan` | `mode plan` |
| **Supervised**<br>`supervised` | Asks before every action that is not a read. | `--permission-mode default` | `sandbox workspace-write`, `approval untrusted` | `permission ask` | `sandbox workspace`, `mode default` | `mode default` |
| **Auto-accept edits**<br>`auto-accept-edits` | Reads and edits files without asking. Everything else asks. | `--permission-mode acceptEdits` | `sandbox workspace-write`, `approval untrusted` | `permission ask` | `sandbox workspace`, `mode default` | `mode default` |
| **Edit files and run commands** (default)<br>`edits-and-commands` | Reads, edits files and runs commands without asking. Other tools are refused. | `--permission-mode dontAsk`, `--allowedTools Bash,Edit,Glob,Grep,LS,MultiEdit,NotebookEdit,Read,TodoWrite,Write` | `sandbox workspace-write`, `approval untrusted` | `permission ask` | `sandbox workspace`, `mode default` | `mode default` |
| **Auto**<br>`auto` | The tool's own reviewed automatic mode decides. A tool without one asks, as in Supervised. Needs run administration. | `--permission-mode auto` | No automatic mode: asks as in Supervised (`sandbox workspace-write`, `approval untrusted`) | No automatic mode: asks as in Supervised (`permission ask`) | No automatic mode: asks as in Supervised (`sandbox workspace`, `mode default`) | No automatic mode: asks as in Supervised (`mode default`) |
| **Full**<br>`full` | Nothing asks. Olivares still confines the session and records every action. Needs run administration. | `--permission-mode bypassPermissions` | `sandbox danger-full-access`, `approval untrusted` | `permission ask` | `mode default` | `mode default` |
<!-- permission-table:end -->

## Reading the table

- **Older names keep working.** `ask` is `supervised`, and `edits-only` is
  `auto-accept-edits`.
- **Auto** is the tool's own reviewed automatic mode. Only Claude Code has one.
  A session on another tool asks before each action, as in Supervised, and the
  table says so in its cell. Auto starts like every permission but Full, with no
  approval of its own: the tool's review, your live policy, the kill switch and
  the audit ledger apply to what it does.
- **Full** removes every prompt. Olivares still confines the session, applies
  your policy and records every action. The tools that have a sandbox of their
  own run without it under Full, because Olivares confines them from outside.
- **Tools other than Claude Code** take their approvals from Olivares. When one
  of them asks to do something, Olivares answers by the permission: a refusal
  under Read only, a question to a person where the permission says to ask, and
  otherwise the decision of your live policy. So a native `ask` in a cell, as for
  Auto-accept edits or Full on Codex, does not mean you are asked: Olivares
  answers it.
- **Codex's sandbox** is the one the table shows on a host where Codex can run
  it. On a host where it cannot, Olivares confines the session itself and
  starts Codex without its own sandbox; the table does not show that.
