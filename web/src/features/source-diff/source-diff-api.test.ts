// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { http } from '@/lib/api/client'
import {
  classifyDiffError,
  diffRequestPath,
  readContentDiff,
} from './source-diff-api'

vi.mock('@/lib/api/client', () => ({
  http: { get: vi.fn() },
}))

const query = {
  source: 'github-1',
  host: 'github',
  repository: 'acme/web',
  base: 'main',
  head: 'deadbeef',
}

describe('source diff request', () => {
  beforeEach(() => {
    vi.mocked(http.get).mockReset()
  })

  it('sends source, host, repository, base, and head, and no other query', () => {
    const path = diffRequestPath(query)
    const params = new URL(path, 'http://console.local').searchParams
    expect([...params.keys()].sort()).toEqual([
      'base',
      'head',
      'host',
      'repository',
      'source',
    ])
    expect(params.get('source')).toBe('github-1')
    expect(params.get('host')).toBe('github')
    expect(params.get('repository')).toBe('acme/web')
    expect(params.get('base')).toBe('main')
    expect(params.get('head')).toBe('deadbeef')
  })

  it('calls that path', async () => {
    vi.mocked(http.get).mockResolvedValue({
      ...query,
      truncated: false,
      files: [],
    })
    await readContentDiff(query)
    expect(http.get).toHaveBeenCalledWith(diffRequestPath(query))
  })
})

describe('source diff errors', () => {
  it('uses the code, not the message, for both 403s', () => {
    const host = classifyDiffError(
      new ApiError(403, 'git_host_forbidden', 'forbidden'),
      null,
    )
    const permission = classifyDiffError(
      new ApiError(403, 'forbidden', 'git host refused the read'),
      null,
    )
    expect(host.kind).toBe('hostForbidden')
    expect(permission.kind).toBe('forbidden')
  })

  it('names 422, 503, 501, 404, 502, and 400 by code', () => {
    expect(
      classifyDiffError(new ApiError(422, 'diff_too_large', 'no'), null).kind,
    ).toBe('tooLarge')
    expect(
      classifyDiffError(new ApiError(503, 'rate_limited', 'no'), null).kind,
    ).toBe('rateLimited')
    expect(
      classifyDiffError(
        new ApiError(501, 'git_host_diff_unavailable', 'no'),
        null,
      ).kind,
    ).toBe('unavailable')
    expect(
      classifyDiffError(new ApiError(404, 'unknown_ref', 'no'), null).kind,
    ).toBe('unknownRef')
    expect(
      classifyDiffError(new ApiError(502, 'upstream', 'no'), null).kind,
    ).toBe('upstream')
    expect(
      classifyDiffError(new ApiError(400, 'bad_request', 'no'), null).kind,
    ).toBe('badRequest')
  })

  it('keeps Retry-After when the code is rate_limited', () => {
    const now = 1_700_000_000_000
    const failure = classifyDiffError(
      new ApiError(503, 'rate_limited', 'slow down'),
      '30',
      now,
    )
    expect(failure.retryAt).toBe(now + 30_000)
  })

  it('ignores Retry-After on any other code', () => {
    const failure = classifyDiffError(
      new ApiError(502, 'upstream', '30'),
      '30',
      0,
    )
    expect(failure.retryAt).toBeUndefined()
  })

  it('reads each failing response and leaves fetch unchanged', async () => {
    const original = globalThis.fetch
    const now = 1_700_000_000_000
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    let calls = 0
    vi.mocked(http.get).mockImplementation(async () => {
      const mine = ++calls
      await gate
      throw new ApiError(
        503,
        'rate_limited',
        'slow',
        undefined,
        {},
        undefined,
        mine === 1 ? '30' : '12',
      )
    })
    const pending = Promise.allSettled([
      readContentDiff(query, now),
      readContentDiff(query, now),
    ])
    release()
    const settled = await pending
    expect(globalThis.fetch).toBe(original)
    const waits = settled.map((item) => {
      expect(item.status).toBe('rejected')
      return item.status === 'rejected' ? item.reason.retryAt : 0
    })
    expect(waits.sort()).toEqual([now + 12_000, now + 30_000])
  })
})
