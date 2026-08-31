// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { sandboxApi } from './api'

let sent = ''
function capture() {
  globalThis.fetch = vi.fn(async (url: string) => {
    sent = String(url)
    return new Response(JSON.stringify({ items: [], has_more: false }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as never
}
const limit = () => new URL(sent, 'http://test').searchParams.get('limit')
afterEach(() => (sent = ''))

describe('sandbox list ceilings travel on the wire', () => {
  it.each([
    ['scenarios', () => sandboxApi.scenarios()],
    ['runs', () => sandboxApi.runs()],
    ['comparisons', () => sandboxApi.comparisons()],
  ])('%s sends limit=1000', async (_name, call) => {
    capture()
    await call()
    expect(limit()).toBe('1000')
  })

  it('outputs remains the complete listAll route', async () => {
    capture()
    await sandboxApi.outputs('run-1')
    expect(limit()).toBeNull()
  })
})
