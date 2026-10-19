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
      'Session started · claude-opus-5 · 4 tools',
    )
    expect(startLine('en', ['Bash'])).toBe(
      'Session started · claude-opus-5 · 1 tool',
    )
  })

  it.each(LANGUAGE_CODES.filter((l) => l !== 'en'))(
    'is not English in %s',
    (lng) => {
      const line = startLine(lng, ['Bash', 'Read', 'Write', 'Task'])
      expect(line).not.toMatch(/Session started|\btools\b/)
      expect(line).toContain('claude-opus-5')
      expect(line).toContain('4')
    },
  )

  it('says nothing about tools when the frame lists none', () => {
    const withTools = startLine('de', ['Bash', 'Read'])
    expect(withTools.split(' · ')).toHaveLength(3)
    expect(startLine('de', []).split(' · ')).toHaveLength(2)
  })
})
