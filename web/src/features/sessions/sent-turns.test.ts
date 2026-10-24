// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import nativeHistory from './fixtures/codex-native-history.json'
import { mapConversationFrames } from './conversation-frames'
import { settleTurns } from './sent-turns'

const user = (text: string) =>
  JSON.stringify({ type: 'user', message: { role: 'user', content: text } })

// Review of #2855: a note is placed by the frames the session had shown when it was
// sent, which never move; the rows of the shown window do (it keeps the newest frames).
describe('settleTurns', () => {
  it('settles a note when the window drops older rows before the echo comes', () => {
    // Sent after frames 1-3 (a, b, c); frame 1 then left the window, and the echo is 4.
    const items = mapConversationFrames(
      [user('b'), user('c'), user('continue')],
      undefined,
      [2, 3, 4],
    )
    expect(
      settleTurns(items, [{ text: 'continue', after: 3 }]).pending,
    ).toEqual([])
  })

  it('does not take an earlier row of the same words for the echo', () => {
    const items = mapConversationFrames(
      [user('continue'), user('b')],
      undefined,
      [1, 2],
    )
    const note = { text: 'continue', after: 2 }
    expect(settleTurns(items, [note]).pending).toEqual([note])
  })

  it('takes a stored Codex row the ring replays for the echo', () => {
    const replayed = nativeHistory.turns[1]!
    const items = mapConversationFrames(
      [
        JSON.stringify(nativeHistory),
        JSON.stringify({
          method: 'item/completed',
          params: { item: replayed.items[0], turnId: replayed.id },
        }),
      ],
      undefined,
      [undefined, 4],
    )
    expect(
      settleTurns(items, [{ text: 'earlier console prompt', after: 3 }])
        .pending,
    ).toEqual([])
  })

  it('settles a note by one more row of its words where the frame that showed it is gone', () => {
    const items = mapConversationFrames(
      [user('continue'), user('continue')],
      undefined,
      [undefined, 2],
    )
    expect(
      settleTurns(items, [{ text: 'continue', after: 5, shown: 1 }]).pending,
    ).toEqual([])
  })

  it('does not settle a note by as many rows of its words as were shown', () => {
    const items = mapConversationFrames([user('continue')], undefined, [
      undefined,
    ])
    const note = { text: 'continue', after: 5, shown: 1 }
    expect(settleTurns(items, [note]).pending).toEqual([note])
  })
})
