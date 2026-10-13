// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { CodeLine } from './code-line'

/**
 * A translated or engine-written sentence whose `backticked` parts are commands: each becomes
 * the inline command chip, the rest stays text. The backticks are the markup, so no locale
 * string changes and every language reads the same way. A sentence with no backtick comes
 * back as its own text; a blank pair, or a pair that spans lines, is text, not a chip (CodeLine
 * refuses an empty command, and one odd string must not blank a screen); a missing sentence is nothing.
 */
export function withInlineCommands(
  text: string | null | undefined,
): ReactNode[] {
  if (typeof text !== 'string') return []
  return text
    .split(/`([^`\r\n]+)`/)
    .map((part, i) =>
      i % 2 === 1 && part.trim() !== '' ? (
        <CodeLine key={i} command={part} inline />
      ) : (
        part
      ),
    )
}
