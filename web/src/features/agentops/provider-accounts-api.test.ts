// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The provider-account calls, at the TRANSPORT. The panel tests mock the typed client,
// so only this file can see which route, method, body, tenant and guard each call
// really hands to `http`: the list asks for one bounded page and forwards its cursor,
// the point read forwards its signal, and adopt posts a JSON document (never an empty
// body, which the engine refuses) with the tenant and the dispatch guard captured
// when the operator submitted. The keys live under the authority boundary, so the
// boundary's cancel-and-remove reaches every account read.
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/api/client', () => ({
  http: {
    get: vi.fn(),
    post: vi.fn(),
    patch: vi.fn(),
    put: vi.fn(),
    putRaw: vi.fn(),
    delete: vi.fn(),
  },
}))
import { http } from '@/lib/api/client'
import { ACCOUNT_PAGE, agentOpsApi, agentOpsKeys } from './api'

const get = vi.mocked(http.get)
const post = vi.mocked(http.post)

beforeEach(() => {
  vi.clearAllMocks()
  get.mockResolvedValue({ items: [], has_more: false })
  post.mockResolvedValue({ account_ref: 'ppf_a', name: 'claude' })
})

describe('agentOpsApi — the three provider-account routes', () => {
  it('lists one bounded page of accounts and forwards the cursor and the signal', async () => {
    const ac = new AbortController()
    await agentOpsApi.listAccounts({ cursor: 'c-2' }, { signal: ac.signal })
    expect(get).toHaveBeenCalledOnce()
    const [path, opts] = get.mock.calls[0]
    expect(path).toBe('/v1/m/sessions/provider-accounts')
    expect(opts).toMatchObject({
      query: { limit: ACCOUNT_PAGE, cursor: 'c-2' },
      signal: ac.signal,
    })
    expect(ACCOUNT_PAGE).toBeGreaterThan(0)
    expect(post).not.toHaveBeenCalled()
  })

  it('asks for the first page without a cursor', async () => {
    await agentOpsApi.listAccounts()
    const [, opts] = get.mock.calls[0]
    const query = (opts as { query: Record<string, unknown> }).query
    expect(query.limit).toBe(ACCOUNT_PAGE)
    expect(query.cursor).toBeUndefined()
  })

  it('reads one account by its encoded reference and forwards the signal', async () => {
    const ac = new AbortController()
    await agentOpsApi.getAccount('ppf/odd ref', { signal: ac.signal })
    const [path, opts] = get.mock.calls[0]
    expect(path).toBe('/v1/m/sessions/provider-accounts/ppf%2Fodd%20ref')
    expect((opts as { signal?: AbortSignal }).signal).toBe(ac.signal)
  })

  it('adopts with an explicit name: one POST to the adopt route, the tenant and the guard', async () => {
    const guard = vi.fn()
    await agentOpsApi.adoptAccount(
      'ppf_b',
      { name: 'claude-1' },
      { tenant: 't1', dispatchGuard: guard },
    )
    expect(post).toHaveBeenCalledOnce()
    const [path, body, opts] = post.mock.calls[0]
    expect(path).toBe('/v1/m/sessions/provider-accounts/ppf_b/adopt')
    expect(body).toEqual({ name: 'claude-1' })
    expect(opts).toMatchObject({ tenant: 't1', dispatchGuard: guard })
    expect(get).not.toHaveBeenCalled()
  })

  it('adopts under a generated name with an EMPTY OBJECT, never an empty body', async () => {
    await agentOpsApi.adoptAccount('ppf_b', {}, { tenant: 't1' })
    const [path, body] = post.mock.calls[0]
    expect(path).toBe('/v1/m/sessions/provider-accounts/ppf_b/adopt')
    expect(body).toEqual({})
  })
})

describe('agentOpsKeys — account keys sit under the authority boundary', () => {
  it('extends the boundary scope with the tenant and the opaque epoch', () => {
    const scope = agentOpsKeys.boundaryScope('t1', 7)
    const list = agentOpsKeys.accounts('t1', 7)
    const one = agentOpsKeys.account('t1', 7, 'ppf_a')
    expect(list.slice(0, scope.length)).toEqual([...scope])
    expect(one.slice(0, scope.length)).toEqual([...scope])
    expect(list).toContain('t1')
    expect(one).toContain('ppf_a')
  })

  it('never shares an entry across tenants or boundaries', () => {
    expect(agentOpsKeys.accounts('t1', 7)).not.toEqual(
      agentOpsKeys.accounts('t2', 7),
    )
    expect(agentOpsKeys.accounts('t1', 7)).not.toEqual(
      agentOpsKeys.accounts('t1', 8),
    )
    expect(agentOpsKeys.account('t1', 7, 'ppf_a')).not.toEqual(
      agentOpsKeys.account('t2', 7, 'ppf_a'),
    )
  })
})
