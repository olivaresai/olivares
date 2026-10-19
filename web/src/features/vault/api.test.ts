// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { http } from '@/lib/api/client'
import { DEFAULT_PASSWORD_OPTIONS, vaultApi } from './api'

const scope = expect.objectContaining({ query: { scope: 'tenant' } })

beforeEach(() => {
  vi.restoreAllMocks()
  vi.spyOn(http, 'get').mockResolvedValue({ secrets: [] })
  vi.spyOn(http, 'put').mockResolvedValue({})
  vi.spyOn(http, 'post').mockResolvedValue({})
  vi.spyOn(http, 'delete').mockResolvedValue(undefined)
})

// The view tests replace vaultApi; these pin what actually goes on the wire.
describe('vaultApi', () => {
  it('reads and writes the organization secrets in the tenant scope', async () => {
    await vaultApi.list({ tenant: 't1' })
    expect(http.get).toHaveBeenCalledWith(
      '/v1/console/secrets',
      expect.objectContaining({ tenant: 't1', query: { scope: 'tenant' } }),
    )

    await vaultApi.put(
      'vault/u1/bank',
      { type: 'note', note: 'x', username: '' },
      'a line',
      { tenant: 't1' },
    )
    expect(http.put).toHaveBeenCalledWith(
      '/v1/console/secrets',
      {
        name: 'vault/u1/bank',
        value: '{"type":"note","note":"x"}',
        description: 'a line',
      },
      expect.objectContaining({ tenant: 't1', query: { scope: 'tenant' } }),
    )

    await vaultApi.remove('vault/u1/bank', { tenant: 't1' })
    expect(http.delete).toHaveBeenCalledWith(
      '/v1/console/secrets',
      { name: 'vault/u1/bank' },
      expect.objectContaining({ tenant: 't1', query: { scope: 'tenant' } }),
    )
  })

  it('reveals, asks for a code and generates through the vault verbs', async () => {
    await vaultApi.reveal('vault/u1/bank', { tenant: 't1' })
    expect(http.post).toHaveBeenLastCalledWith(
      '/v1/console/secrets/reveal',
      { name: 'vault/u1/bank' },
      expect.objectContaining({ tenant: 't1', query: { scope: 'tenant' } }),
    )

    await vaultApi.code('vault/u1/bank', { tenant: 't1' })
    expect(http.post).toHaveBeenLastCalledWith(
      '/v1/console/secrets/totp',
      { name: 'vault/u1/bank' },
      scope,
    )

    await vaultApi.generate(DEFAULT_PASSWORD_OPTIONS, { tenant: 't1' })
    expect(http.post).toHaveBeenLastCalledWith(
      '/v1/console/secrets/generate',
      { length: 20, lower: true, upper: true, digits: true, symbols: true },
      scope,
    )
  })
})
