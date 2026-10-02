// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The tools a session runs, by their own product names (not translated). The drivers are
// the engine's (modules/sessions resolveTools): the console names them and starts them,
// it does not decide which exist.
export const SESSION_TOOLS = ['claude', 'codex', 'grok', 'opencode'] as const
export type SessionTool = (typeof SESSION_TOOLS)[number]

export const TOOL_NAMES: Readonly<Record<SessionTool, string>> = {
  claude: 'Claude Code',
  codex: 'Codex',
  grok: 'Grok Build',
  opencode: 'OpenCode',
}

/** A driver's product name; an unknown driver is named by itself. */
export function toolName(driver: string): string {
  return (TOOL_NAMES as Record<string, string>)[driver] ?? driver
}
