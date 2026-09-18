<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
-->

# OpenCode ACP driver (OC1)

Owned operate form: `opencode acp --hostname 127.0.0.1 [--cwd <WorkDir>]`.
Protocol token: `opencode_acp` (bidirectional, text). Transport is the existing
`rpcConn` JSON-RPC 2.0 pump. The counterpart may open loopback HTTP for its own
ACP service; that is not an Olivares adoption interface.

The construction contract for this mapping is held with the design records.

This note records mapping limits that remain successor work: complete tool
governance, the official installers, authenticated paid turns, and a qualified
XDG_RUNTIME_DIR. Template instructions, tool restrictions and non-default
Claude permission modes have no OpenCode argv mapping and are refused before
launch rather than discarded.
