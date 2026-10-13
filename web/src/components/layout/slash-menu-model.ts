// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { RunCommandDTO } from '@/features/agentops/types'

/** Why the menu has no entries to show: read in progress, not readable, nothing reported, or nothing matches. */
export type SlashMenuEmpty = 'loading' | 'failed' | 'unreported' | 'nomatch'

/**
 * The word being typed after a leading "/", or null when the draft is not a command in
 * the making: the menu is for the first word of the message, so it closes at the first
 * space or line break, and a path such as /usr/bin/env is not a command.
 */
export function slashQuery(draft: string): string | null {
  const match = /^\/([^\s/]*)$/.exec(draft)
  return match ? match[1] : null
}

/** 0 = the command's name is what was typed (an agent is typed as @agent-name, so it never is), 1 = it starts with it (a plugin's name counts after its "plugin:" prefix), 2 = it contains it, 3 = only the description does. */
function rank(command: RunCommandDTO, query: string): number {
  const name = command.name.toLowerCase()
  const bare = name.slice(name.lastIndexOf(':') + 1)
  if (command.kind !== 'agent' && name === query) return 0
  if (name.startsWith(query) || bare.startsWith(query)) return 1
  if (name.includes(query)) return 2
  return command.description.toLowerCase().includes(query) ? 3 : -1
}

/**
 * The commands that match what is typed: the name typed in full first, then names that
 * start with it, names that contain it, and descriptions that do. Inside a rank the tool's own order stays,
 * because the tool, not the console, knows what comes first.
 */
export function filterCommands(
  commands: readonly RunCommandDTO[],
  query: string,
): RunCommandDTO[] {
  const q = query.toLowerCase()
  return commands
    .map((command) => ({ command, rank: rank(command, q) }))
    .filter((entry) => entry.rank >= 0)
    .sort((a, b) => a.rank - b.rank)
    .map((entry) => entry.command)
}

/** What a person reads for an entry: what choosing it types, without the trailing space. */
export function commandLabel(command: RunCommandDTO): string {
  return insertText(command).trimEnd()
}

/** What choosing an entry puts in the field: a command or skill is typed as /name, an agent is mentioned the way the tool mentions it. */
export function insertText(command: RunCommandDTO): string {
  return command.kind === 'agent'
    ? `@agent-${command.name} `
    : `/${command.name} `
}

/** The DOM id of one option of the menu, which the text field points at while it is active. */
export function slashOptionId(listId: string, index: number): string {
  return `${listId}-option-${index}`
}
