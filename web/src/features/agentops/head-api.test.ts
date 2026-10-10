// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The two reads that can ask for git HEAD's text of a file send `rev=HEAD`
// only when asked, so every existing caller still reads the current content.
import { beforeEach, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { agentOpsApi } from './api'

const fetcher = vi.fn()

function requested() {
  const url = String(fetcher.mock.calls.at(-1)?.[0])
  const parsed = new URL(url, 'http://engine.test')
  return { path: parsed.pathname, query: parsed.searchParams }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.stubGlobal('fetch', fetcher)
  fetcher.mockImplementation(
    async () =>
      new Response('{}', {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
  )
  configureApiClient({
    getToken: () => null,
    getCSRFToken: () => null,
    getTenant: () => 'tenant-current',
    onUnauthorized: vi.fn(),
    refreshSession: vi.fn(),
  })
})

it('reads a workspace file at HEAD only when asked', async () => {
  await agentOpsApi.readFile('ws_1', 'docs/a b.md')
  expect(requested().path).toMatch(/\/workspaces\/ws_1\/files\/raw$/)
  expect(requested().query.get('path')).toBe('docs/a b.md')
  expect(requested().query.has('rev')).toBe(false)

  await agentOpsApi.readFile('ws_1', 'docs/a b.md', 'HEAD')
  expect(requested().query.get('path')).toBe('docs/a b.md')
  expect(requested().query.get('rev')).toBe('HEAD')
})

it("reads a session's changed file at HEAD only when asked", async () => {
  await agentOpsApi.changedFile('run_1', 'README.md')
  expect(requested().path).toMatch(/\/runs\/run_1\/changes\/file$/)
  expect(requested().query.has('rev')).toBe(false)

  await agentOpsApi.changedFile('run_1', 'README.md', { rev: 'HEAD' })
  expect(requested().query.get('path')).toBe('README.md')
  expect(requested().query.get('rev')).toBe('HEAD')
})
