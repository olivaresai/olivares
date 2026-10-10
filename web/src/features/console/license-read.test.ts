// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { classifyLicenseRead } from './license-read'

describe('classifyLicenseRead — failed refresh is not current success', () => {
  const valid = {
    edition: 'community',
    hot_apply: true,
    status: 'valid',
    source: 'data_dir',
    managed_externally: false,
    max_users: 0,
    seat_limit: 0,
    seat_limited: false,
    active_users: 1,
    features: ['addon_airs'],
  }

  it('pending with no data is loading, not "no license"', () => {
    expect(
      classifyLicenseRead({
        isPending: true,
        isError: false,
        error: null,
        data: undefined,
      }),
    ).toEqual({ kind: 'loading' })
  })

  it('successful payload is current success, including omitted features', () => {
    const got = classifyLicenseRead({
      isPending: false,
      isError: false,
      error: null,
      data: { ...valid, features: undefined, status: 'none' },
    })
    expect(got.kind).toBe('success')
    if (got.kind === 'success') {
      expect(got.license.features).toBeUndefined()
      expect(got.license.status).toBe('none')
    }
  })

  it('error with prior data is failed last-success, not current entitled facts', () => {
    const got = classifyLicenseRead({
      isPending: false,
      isError: true,
      error: new ApiError(500, 'internal', 'later'),
      data: valid,
    })
    expect(got).toMatchObject({
      kind: 'failed',
      hadPriorData: true,
      status: 500,
      lastSuccess: valid,
    })
  })

  it('error with no prior data is failed unavailable, not "no license"', () => {
    const got = classifyLicenseRead({
      isPending: false,
      isError: true,
      error: new ApiError(500, 'internal', 'first'),
      data: undefined,
    })
    expect(got).toMatchObject({
      kind: 'failed',
      hadPriorData: false,
      lastSuccess: undefined,
      status: 500,
    })
  })
})
