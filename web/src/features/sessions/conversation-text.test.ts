// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The session-start line is the first thing a session thread shows. It named itself in
// English in every console language ("Session started · claude-opus-5 · 4 tools", FILM3
// review 2026-10-16), because the frame mapper built the sentence. The mapper now keeps
// the facts (model, tool count) and the sentence comes from the sessions catalog.
import { describe, expect, it } from 'vitest'

import i18n, { LANGUAGE_CODES } from '@/lib/i18n'
import './i18n'
import { mapConversationFrames } from './conversation-frames'
import { systemText } from './conversation-text'

const init = (tools: string[]) =>
  JSON.stringify({
    type: 'system',
    subtype: 'init',
    cwd: '/home/operator/project',
    tools,
    model: 'claude-opus-5',
    uuid: '63eb7de3-80c1-4123-ab73-16fd414fcf60',
  })

function startLine(lng: string, tools: string[]): string {
  const [item] = mapConversationFrames([init(tools)])
  return systemText(i18n.getFixedT(lng), item!)
}

describe('session-start line', () => {
  it('keeps the English wording, with the count agreeing with its noun', () => {
    expect(startLine('en', ['Bash', 'Read', 'Write', 'Task'])).toBe(
      'Session started · Claude Opus 5 · 4 tools',
    )
    expect(startLine('en', ['Bash'])).toBe(
      'Session started · Claude Opus 5 · 1 tool',
    )
  })

  it.each(LANGUAGE_CODES.filter((l) => l !== 'en'))(
    'is not English in %s',
    (lng) => {
      const line = startLine(lng, ['Bash', 'Read', 'Write', 'Task'])
      expect(line).not.toMatch(/Session started|\btools\b/)
      expect(line).toContain('Claude Opus 5')
      expect(line).toContain('4')
    },
  )

  it('says nothing about tools when the frame lists none', () => {
    const withTools = startLine('de', ['Bash', 'Read'])
    expect(withTools.split(' · ')).toHaveLength(3)
    expect(startLine('de', []).split(' · ')).toHaveLength(2)
  })
})

// dev#3119: a limit that stopped the turn is one sentence in the person's words, with the
// reset time; the window names come from the catalog in every language.
describe('the limit that stopped the turn', () => {
  const stop = (info: Record<string, unknown>) =>
    mapConversationFrames([
      JSON.stringify({
        type: 'rate_limit_event',
        rate_limit_info: { status: 'rejected', ...info },
      }),
    ])[0]!
  const now = new Date('2026-10-23T12:00:00Z')

  it('names the window, how much was used and the weekday it resets', () => {
    const resetsAt = Date.parse('2026-10-26T03:00:00Z') / 1000
    const item = stop({
      rateLimitType: 'seven_day',
      utilization: 0.93,
      resetsAt,
    })
    const time = new Date(resetsAt * 1000).toLocaleString(undefined, {
      weekday: 'short',
      hour: '2-digit',
      minute: '2-digit',
    })
    expect(systemText(i18n.getFixedT('en'), item, now)).toBe(
      `Claude Code reached its weekly limit (93% used); it resets ${time}`,
    )
  })

  it('says only the time when it resets today, and nothing it does not know', () => {
    const resetsAt = Date.parse('2026-10-23T15:45:00Z') / 1000
    const today = new Date(resetsAt * 1000)
    const sameDay = today.toDateString() === now.toDateString()
    const line = systemText(
      i18n.getFixedT('en'),
      stop({ rateLimitType: 'five_hour', resetsAt }),
      now,
    )
    expect(line).toMatch(/^Claude Code reached its 5-hour limit; it resets /)
    // Today, wherever the test runs, is the time alone; another day adds the weekday.
    expect(line).toContain(
      today.toLocaleString(
        undefined,
        sameDay
          ? { hour: '2-digit', minute: '2-digit' }
          : { weekday: 'short', hour: '2-digit', minute: '2-digit' },
      ),
    )
    expect(systemText(i18n.getFixedT('en'), stop({}), now)).toBe(
      'Claude Code reached its usage limit',
    )
  })

  it.each(LANGUAGE_CODES.filter((l) => l !== 'en'))(
    'is not English in %s',
    (lng) => {
      const line = systemText(
        i18n.getFixedT(lng),
        stop({ rateLimitType: 'seven_day', utilization: 0.5 }),
        now,
      )
      expect(line).not.toMatch(/reached|weekly|used/)
      expect(line).toContain('50')
    },
  )
})
