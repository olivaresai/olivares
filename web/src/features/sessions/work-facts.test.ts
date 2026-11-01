// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import i18n from '@/lib/i18n'
import { formatDuration } from '@/lib/format'
import type { LiveDTO } from './types'
import { workFacts } from './work-facts'
import './i18n'

const t = i18n.getFixedT('en')

function live(over: Partial<LiveDTO>): LiveDTO {
  return {
    duration_seconds: 0,
    tool_call_count: 0,
    event_count: 0,
    cost_micro_usd: 0,
    ...over,
  } as LiveDTO
}

describe('workFacts', () => {
  it('says the events only when they are not the tool calls again', () => {
    // 0.3 read "188 tool calls · 188 events": the second figure added nothing.
    expect(
      workFacts(
        live({ tool_call_count: 188, event_count: 188 }),
        t,
        {},
        undefined,
      ),
    ).toEqual(['188 tool calls'])
    expect(
      workFacts(live({ tool_call_count: 2, event_count: 3 }), t, {}, undefined),
    ).toEqual(['2 tool calls', '3 events'])
    expect(workFacts(live({ event_count: 4 }), t, {}, undefined)).toEqual([
      '4 events',
    ])
  })

  it('names the model in words, and the time with the one formatter', () => {
    expect(
      workFacts(
        live({ duration_seconds: 699, tool_call_count: 1 }),
        t,
        { duration: formatDuration },
        'claude-opus-5-5',
      ),
    ).toEqual(['worked for 11m 39s', '1 tool call', 'Claude Opus 5.5'])
  })
})
