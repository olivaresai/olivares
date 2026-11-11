// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The one thing the conversation may ask of the session's terminal: run this command. The
// drawer (terminal-drawer.tsx) provides it where the person may use a terminal in the
// session's folder; elsewhere there is none and a code block shows no Run button.
import { createContext, useContext } from 'react'

export interface RunInTerminal {
  run: (command: string) => void
}

export const RunInTerminalContext = createContext<RunInTerminal | null>(null)

/** The way to run a command in the session's terminal; null where there is none. */
export function useRunInTerminal(): RunInTerminal | null {
  return useContext(RunInTerminalContext)
}

// A command is typed into a shell and then Enter is pressed, so what it holds acts as keys:
// ESC[201~ ends a bracketed paste early, other controls edit or run, and bidirectional or
// zero-width characters hide what runs. A tab, a newline and a carriage return are the only
// controls a command needs.
const UNSAFE_COMMAND =
  // eslint-disable-next-line no-control-regex
  /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2066-\u2069]/

/** Whether `command` holds a control, bidirectional or zero-width character; it is never sent. */
export function unsafeCommand(command: string): boolean {
  return UNSAFE_COMMAND.test(command)
}
