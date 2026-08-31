// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'
import { RATE_LIMIT_SUBJECT, rateLimitsApi } from './api'

describe('rate-limit findings ceiling travels on the wire', () => {
  it('sends the exact limit and preserves the subject filter', async () => {
    let sent = ''
    globalThis.fetch = vi.fn(async (url: string) => {
      sent = String(url)
      return new Response(JSON.stringify({ items: [], has_more: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }) as never
    await rateLimitsApi.findings()
    const params = new URL(sent, 'http://test').searchParams
    expect(params.get('limit')).toBe('1000')
    expect(params.get('subject_kind')).toBe(RATE_LIMIT_SUBJECT)
  })
})
