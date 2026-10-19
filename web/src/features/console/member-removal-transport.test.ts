// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The People tab tests mock `consoleApi`, so they check what the screen asks the client
// for and not what the client sends. This one reads the wire: a member is removed with
// `DELETE /v1/members/{id}` (every edition), not through the SCIM server, which is Business.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { consoleApi } from './api'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'

beforeEach(() => {
  vi.restoreAllMocks()
  useSessionStore.setState({ csrfToken: 'tok' } as never)
  useTenantStore.setState({ activeTenant: 'acme' } as never)
})

describe('consoleApi.removeMember — lo que sale por el cable', () => {
  it('sends DELETE /v1/members/{id} with the id escaped, and never touches /scim/', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await consoleApi.removeMember('u/1')

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(new URL(url as string, 'https://x').pathname).toBe(
      '/v1/members/u%2F1',
    )
    expect((init as RequestInit).method).toBe('DELETE')
    expect(String(url)).not.toContain('scim')
  })
})
