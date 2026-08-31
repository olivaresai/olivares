// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { workspaceDashboardApi } from './api'

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
const query = () => new URL(sent, 'http://test').searchParams
afterEach(() => (sent = ''))

describe('workspace dashboard preview ceilings travel on the wire', () => {
  it.each([
    ['agents', () => workspaceDashboardApi.agents('ws-1')],
    ['groups', () => workspaceDashboardApi.groups('ws-1')],
  ])('%s sends limit=10 without losing its workspace', async (_name, call) => {
    capture()
    await call()
    expect(query().get('limit')).toBe('10')
    expect(query().get('workspace_id')).toBe('ws-1')
  })
})
