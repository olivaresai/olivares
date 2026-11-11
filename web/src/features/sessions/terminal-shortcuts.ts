// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The keys of the terminal drawer are rows of the console's one keybinding table: the drawer
// reads them from the page and the terminal reads them to leave them alone (a key the drawer
// owns is not typed into the shell). None is a key a shell needs.
import { resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'

export type TerminalCommand =
  | 'terminal.new'
  | 'terminal.previous'
  | 'terminal.next'
  | 'terminal.close'
  | 'terminal.search'
  | 'terminal.links'
  | 'terminal.copy'
  | 'terminal.selectAll'
  | 'terminal.scrollTop'
  | 'terminal.scrollBottom'

export function terminalShortcut(e: KeyboardEvent): TerminalCommand | null {
  const command = resolveBinding(KEYBINDINGS, e, { terminalFocused: true })
  return command?.startsWith('terminal.') ? (command as TerminalCommand) : null
}
