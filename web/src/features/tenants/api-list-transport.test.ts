// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'
import { tenantsApi } from './api'

describe('the complete tenant roster does not pretend to paginate', () => {
  it('sends no decorative limit', async () => {
    let sent = ''
    globalThis.fetch = vi.fn(async (url: string) => {
      sent = String(url)
      return new Response(JSON.stringify({ items: [], has_more: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }) as never
    await tenantsApi.list()
    expect(new URL(sent, 'http://test').searchParams.get('limit')).toBeNull()
  })
})
