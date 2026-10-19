// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HOW A MESSAGE FINDS ITS ROW (#2129). The index, the search and the time anchor are the
// engine's (GET /runs/{ref}/messages); what is here is only how an index entry finds its
// row in the conversation this view holds.
import type { RunMessageDTO } from '@/features/agentops/types'
import type { ConversationItem } from './conversation-frames'

/** The engine's messageFirstLineMax: an index line is cut at this many characters. */
const FIRST_LINE_MAX = 160

/** The first line of a message as the engine indexes it: the first line with words,
 * trimmed, cut at 160 characters with an ellipsis. A row and its index entry agree on it
 * because both come from the same text. */
export function indexLine(text: string): string {
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (line === '') continue
    const chars = Array.from(line)
    return chars.length > FIRST_LINE_MAX
      ? chars.slice(0, FIRST_LINE_MAX).join('') + '…'
      : line
  }
  return ''
}

const KIND_OF = { user: 'operator', assistant: 'assistant' } as const

/**
 * The row of the conversation that is `message`, or null when this view does not hold it.
 * A row is the message when it is the same sender's and its first line is the same.
 * Equal words repeat ("continue"), and the stream a view holds is the newest part of the
 * conversation, so the engine says how many later messages repeat these words
 * (`later_same`) and the row is that many from the end among the equal rows.
 */
export function itemForMessage(
  items: readonly ConversationItem[],
  message: Pick<RunMessageDTO, 'role' | 'first_line' | 'later_same'>,
): ConversationItem | null {
  const kind = KIND_OF[message.role]
  const same = items.filter(
    (item) =>
      item.kind === kind &&
      indexLine(item.text ?? item.summary) === message.first_line,
  )
  return same[same.length - 1 - (message.later_same ?? 0)] ?? null
}

/** At most `max` entries of `items`, evenly spread, the first and the last always among
 * them: a rail of a few dozen marks stands for a session of thousands of messages. */
export function sampleMarks<T>(items: readonly T[], max: number): T[] {
  if (items.length <= max || max < 2) return items.slice(0, Math.max(max, 0))
  const out: T[] = []
  for (let i = 0; i < max; i++)
    out.push(items[Math.round((i * (items.length - 1)) / (max - 1))]!)
  return out
}

/** A `datetime-local` value ("2026-10-14T10:30"), which the browser reads in the
 * person's own zone, as the instant the engine's `at` takes. Empty or unreadable is
 * undefined. */
export function instantOf(local: string): string | undefined {
  if (local === '') return undefined
  const at = new Date(local)
  return Number.isNaN(at.getTime()) ? undefined : at.toISOString()
}
