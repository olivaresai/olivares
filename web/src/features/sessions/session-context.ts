// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { agentOpsApi } from '@/features/agentops/api'
import type { RunDTO, RunMessagesParams } from '@/features/agentops/types'
import type { RequestOptions } from '@/lib/api/client'
import type { ConversationItem } from './conversation-frames'
export const CONTEXT_MAX_BYTES = 256 * 1024

/** Where a fork may end: a finished turn, or the limit that stopped one. */
export function endsTurn(item: ConversationItem): boolean {
  return item.kind === 'result' || item.systemKind === 'rate_limit'
}

/** A portable context snapshot, reviewed as the new session's first message.
 * Tool rows are the same shortened descriptions the conversation displays. */
export function forkContext(
  source: string,
  items: readonly ConversationItem[],
  end: string,
): string {
  const index = items.findIndex((i) => i.id === end)
  if (index < 0 || !endsTurn(items[index])) throw new Error('unfinished turn')
  const text =
    `Context from session ${source} through turn ${end} (tool output shortened):\n\n` +
    items
      .slice(0, index + 1)
      .map(
        (i) =>
          `[${i.kind}]\n${i.text ?? i.toolCommand ?? i.summary}${i.toolResultSummary ? '\n' + i.toolResultSummary : ''}`,
      )
      .join('\n\n')
  if (new TextEncoder().encode(text).length > CONTEXT_MAX_BYTES)
    throw new Error('context too large')
  return text
}

/** What a copy reads: the text, how many transcript records it had to leave out, and
 * how many messages the engine cut at its 64 KiB bound. */
export interface Copied {
  text: string
  unreadable: number
  clipped: number
}

/** A copy refused for a reason the person is told in words (a `rowActions` key). */
export class CopyRefused extends Error {
  readonly key: string
  constructor(key: string) {
    super(key)
    this.key = key
  }
}

/**
 * The session's conversation as plain text, oldest first, every page of it: each
 * message under its sender's name and a blank line between messages.
 *
 * ⛔ NOTHING IS COPIED SHORTER THAN IT SAYS. The records the engine could not read are
 *    counted and said; a message a page cut at 64 KiB is read again by id, whole, and
 *    one still cut (an engine that cuts it there too) is counted and said; a message without its
 *    whole text (an engine that ignores `text=1`) refuses the copy instead of pasting
 *    first lines; and a transcript that stops being readable after the first page, or
 *    hands back the cursor it was asked for, refuses it too.
 */
export async function transcriptText(
  run: RunDTO,
  you: string,
  assistant: string,
  scope?: Pick<RequestOptions, 'tenant' | 'signal' | 'dispatchGuard'>,
  /** Each message under a Markdown heading, for a file the person keeps. */
  markdown = false,
): Promise<Copied> {
  const read = (params: RunMessagesParams) =>
    scope
      ? agentOpsApi.messages(run.run_ref, params, scope)
      : agentOpsApi.messages(run.run_ref, params)
  const parts: string[] = []
  let unreadable = 0
  let clipped = 0
  let cursor: string | undefined
  do {
    const page = await read({
      role: 'all',
      order: 'asc',
      text: 1,
      limit: 50,
      cursor,
    })
    if (page.transcript !== 'read')
      throw new CopyRefused(
        cursor === undefined
          ? 'rowActions.noTranscript'
          : 'rowActions.transcriptChanged',
      )
    unreadable += page.unreadable ?? 0
    for (const m of page.items) {
      if (m.text === undefined && m.first_line !== '')
        throw new CopyRefused('rowActions.noWholeText')
      let text = m.text
      if (m.clipped) {
        // A page cuts each text at 64 KiB; read by id, the message is whole.
        const whole = (await read({ id: m.id })).items[0]
        if (whole?.text !== undefined && !whole.clipped) text = whole.text
        else clipped++
      }
      const who = m.role === 'user' ? you : assistant
      parts.push(
        markdown ? `## ${who}\n\n${text ?? ''}\n` : `${who}:\n${text ?? ''}\n`,
      )
    }
    // A page that points back at itself would be read forever.
    if (page.next !== undefined && page.next === cursor)
      throw new CopyRefused('rowActions.transcriptChanged')
    cursor = page.next
  } while (cursor)
  if (parts.length === 0) throw new CopyRefused('rowActions.noTranscript')
  return { text: parts.join('\n'), unreadable, clipped }
}
