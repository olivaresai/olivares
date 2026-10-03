// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { ApiError } from './errors'
import { createQueryClient } from './query'

describe('the query retry policy', () => {
  const retry = createQueryClient().getDefaultOptions().queries!.retry as (
    failureCount: number,
    error: unknown,
  ) => boolean

  it('retries what can pass, never what cannot (a 501 is not transient)', () => {
    // N2's PostgreSQL run without the admin pool: /v1/system/orgs answered 501 and the
    // console asked again and again.
    expect(
      retry(0, new ApiError(501, 'not_implemented', 'no admin pool')),
    ).toBe(false)
    expect(retry(0, new ApiError(404, 'not_found', 'gone'))).toBe(false)
    expect(retry(0, new ApiError(403, 'forbidden', 'no'))).toBe(false)
    expect(retry(0, new ApiError(503, 'unavailable', 'busy'))).toBe(true)
    expect(retry(0, new TypeError('network'))).toBe(true)
    expect(retry(2, new ApiError(503, 'unavailable', 'busy'))).toBe(false)
  })
})
