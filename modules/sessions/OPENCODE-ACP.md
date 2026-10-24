<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
-->

# OpenCode ACP driver (OC1)

Owned operate form: `opencode acp --hostname 127.0.0.1 --print-logs --log-level ERROR [--cwd <WorkDir>]`.
The driver reads the tool's own "stream error" log lines on stderr: when the
conversation's model call failed and the turn shows nothing more for 15 s, the turn is
cancelled and the session says why in the tool's words (#2937). Other stderr is shown
as before.
Protocol token: `opencode_acp` (bidirectional, text). Transport is the existing
`rpcConn` JSON-RPC 2.0 pump. The counterpart may open loopback HTTP for its own
ACP service; that is not an Olivares adoption interface.

The construction contract for this mapping is held with the design records.

This note records mapping limits that remain successor work: complete tool
governance, the official installers, authenticated paid turns, and a qualified
XDG_RUNTIME_DIR. Template instructions, tool restrictions and non-default
Claude permission modes have no OpenCode argv mapping and are refused before
launch rather than discarded.

Subagents. OpenCode's ACP agent forwards a permission ask only from a session it
created, so a subagent's ask (task -> general -> bash) never reaches the client and its
turn waits forever, and what the global rules allow it goes unaudited (measured on
1.19.4, #3083). Only the build agent may ask, and it denies `task`, so OpenCode offers
no task tool; the global rules, which every other agent has, deny everything, so an
agent that still starts gets no tool and ends at once. OpenCode's built-in `/review` is a
subtask command, run as a subagent with the build agent's rules whatever the task rule
says, so a `/review` prompt is refused with 400 before it reaches the tool. A command the
account's or the session folder's own OpenCode configuration marks as a subtask is not
seen here (#3076).

Reads and searches. The build agent's `read`, `glob` and `grep` (and `list`, which 1.19.4
does not offer) ask, so live policy decides each one as a read and the audit records it;
every preset allows a read, so no person is asked for one inside the session folder. A
read names its file (#2890). A search names the folder it searches: its `path`, or, when
it names none, the session folder, where OpenCode then searches (#3058). Only an ask
shaped as OpenCode's own glob or grep is taken as a search of the session folder; an MCP
tool that OpenCode also calls a search (`context7_*`) asks with no input and is not.
A folder outside the session's also gets OpenCode's own `external_directory` ask, which
goes to a person under the ask preset.

File writes. After an approved edit OpenCode asks its client to write the file
(`fs/write_text_file`), whether or not the client advertised it, so the client answers
within the session folder (`acp_fs.go`): the jail of the governed file API, minus the Git
hooks and configuration, the read-only folders and the engine folders the child cannot
change. It writes only for a live turn, and one write at a time. A read-only session or
workspace, a container session and a host that is not Linux refuse it with the protocol's
own error. Reading (`fs/read_text_file`) stays refused, and the client
still advertises no file system.

Command arguments. OpenCode expands a `/command`'s arguments into its template, then runs each
``!`shell` `` in the result and reads each `@path`, before and despite the session's `bash`,
`read` and `external_directory` rules (measured on 1.19.3). It finds the command after trimming
any leading Unicode space. One character is enough: a typed backtick closes an opener the template
leaves open, arguments are joined after the split, and OpenCode reads `$` in them as replacement
patterns. The driver refuses a `/command` prompt that contains a backtick, `$` or `@` with 400,
before it reaches the tool (`openCodeSession.Input`). A delivered command with any ``!` ``, closed
or not, or with a `$ARGUMENTS` or `$1` placeholder next to `!`, a backtick or `@`, or after a `$`
and capital letters (OpenCode replaces `$1` first, so `$ARG$1UMENTS` forms a new `$ARGUMENTS`), is
refused at delivery. A prompt without the leading `/command` is not expanded and is sent as typed. Commands
in the session folder's own `.opencode` are not screened yet (#3076).
