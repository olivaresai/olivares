// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { sendBlockedReason, sessionTurnBody } from './session-turn'
import type { RunDTO } from './types'

const claude: Pick<RunDTO, 'provider_driver'> = { provider_driver: 'claude' }
const codex: Pick<RunDTO, 'provider_driver'> = { provider_driver: 'codex' }
const legacy: Pick<RunDTO, 'provider_driver'> = {}

describe('sessionTurnBody — a turn is a sentence', () => {
  it('sends a Codex run as {text}, the same body as agent session input --text', () => {
    expect(sessionTurnBody(codex, 'review the remaining tests')).toEqual({
      text: 'review the remaining tests',
    })
  })

  it('wraps a Claude sentence as a user frame on {line}, never asking for NDJSON', () => {
    const body = sessionTurnBody(claude, 'Reply with the single word OK')
    expect(body).toEqual({
      line: JSON.stringify({
        type: 'user',
        message: { role: 'user', content: 'Reply with the single word OK' },
      }),
    })
    expect(JSON.stringify(body)).not.toMatch(/NDJSON/i)
  })

  it('keeps a legacy run with no profile on the wrapped sentence, not a raw typed line', () => {
    expect(sessionTurnBody(legacy, 'continue')).toEqual({
      line: JSON.stringify({
        type: 'user',
        message: { role: 'user', content: 'continue' },
      }),
    })
  })

  it('does not decide from the text: NDJSON-looking input to a Codex run is still {text}', () => {
    expect(sessionTurnBody(codex, '{"type":"user"}')).toEqual({
      text: '{"type":"user"}',
    })
  })

  it('sends the advanced disclosure as a raw {line} for any driver', () => {
    const wire = '{"type":"user","message":"continue"}'
    expect(sessionTurnBody(claude, wire, true)).toEqual({ line: wire })
    expect(sessionTurnBody(codex, wire, true)).toEqual({ line: wire })
  })
})

describe('sendBlockedReason — a disabled Send says why', () => {
  it('names the session state first: nothing can be sent to a session that is not live', () => {
    expect(
      sendBlockedReason({ live: false, draft: 'hello', sending: false }),
    ).toBe('live.blocked.notLive')
    expect(sendBlockedReason({ live: false, draft: '', sending: true })).toBe(
      'live.blocked.notLive',
    )
  })

  it('names the launch approval a held session waits on, not start or resume', () => {
    expect(
      sendBlockedReason({
        live: false,
        draft: 'hello',
        sending: false,
        waitingApproval: true,
      }),
    ).toBe('live.blocked.approval')
  })

  it('says a sentence is on its way while the last one is being sent', () => {
    expect(
      sendBlockedReason({ live: true, draft: 'hello', sending: true }),
    ).toBe('live.blocked.sending')
    // Typing cannot help while a sentence is on its way: sending wins over empty.
    expect(sendBlockedReason({ live: true, draft: '', sending: true })).toBe(
      'live.blocked.sending',
    )
  })

  it('asks for a sentence when the box is empty or blank', () => {
    expect(sendBlockedReason({ live: true, draft: '', sending: false })).toBe(
      'live.blocked.empty',
    )
    expect(
      sendBlockedReason({ live: true, draft: '   ', sending: false }),
    ).toBe('live.blocked.empty')
  })

  it('has no reason when Send can act', () => {
    expect(
      sendBlockedReason({ live: true, draft: 'hello', sending: false }),
    ).toBeNull()
  })
})
