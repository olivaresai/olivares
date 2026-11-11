// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'
import { agentOpsApi } from './api'

beforeEach(() => {
  __resetRefreshState()
  configureApiClient({
    getToken: () => null,
    getCSRFToken: () => 'test-csrf',
    getTenant: () => 'active-tenant',
    onUnauthorized: vi.fn(),
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => Response.json({ path: 'a.md', size: 1, sha256: 'x' })),
  )
})

afterEach(() => vi.unstubAllGlobals())

/** The request headers of the one file write sent. */
function sentHeaders(): Headers {
  expect(fetch).toHaveBeenCalledOnce()
  const [, init] = vi.mocked(fetch).mock.calls[0]!
  return new Headers(init?.headers)
}

describe('a file write states its precondition when it has one', () => {
  it('saves over the version it read with If-Match', async () => {
    await agentOpsApi.writeFile('ws', 'a.md', 'text', { version: '"v1"' })
    const headers = sentHeaders()
    expect(headers.get('If-Match')).toBe('"v1"')
    expect(headers.get('If-None-Match')).toBeNull()
  })

  it('saves with no precondition when the read gave no version', async () => {
    // An engine whose read sends no version takes a save as before.
    await agentOpsApi.writeFile('ws', 'a.md', 'text', { version: undefined })
    const headers = sentHeaders()
    expect(headers.get('If-Match')).toBeNull()
    expect(headers.get('If-None-Match')).toBeNull()
  })

  it('creates only, with If-None-Match, for a new file', async () => {
    await agentOpsApi.writeFile('ws', 'new.md', '', { create: true })
    const headers = sentHeaders()
    expect(headers.get('If-None-Match')).toBe('*')
    expect(headers.get('If-Match')).toBeNull()
  })

  it('uploads bytes as a create only', async () => {
    await agentOpsApi.writeFile('ws', 'doc.pdf', new Blob(['%PDF']))
    expect(sentHeaders().get('If-None-Match')).toBe('*')
  })
})
