// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// AN EDITION REFUSAL IS NOT A PERMISSION REFUSAL. The person's role is fine; the product
// edition is what answers. `isForbidden` is the role refusal every view branches on, so it
// answers false for an edition refusal, and those views reach the edition line.
import { describe, expect, it } from 'vitest'
import { ApiError } from './errors'

describe('ApiError: a role refusal and an edition refusal are two answers', () => {
  it.each([
    [
      '403 edition_required',
      new ApiError(403, 'edition_required', 'm', 'r', {
        edition: 'engineer',
        capability: 'accessmap',
      }),
    ],
    [
      '403 addon_requires_license',
      new ApiError(403, 'addon_requires_license', 'm'),
    ],
    [
      'a 403 that names a paid edition',
      new ApiError(403, 'forbidden', 'm', 'r', { edition: 'business' }),
    ],
  ])('%s is an edition refusal, never a role refusal', (_, err) => {
    expect(err.isEditionRefusal).toBe(true)
    expect(err.isForbidden).toBe(false)
    expect(err.isForbiddenOrEdition).toBe(true)
  })

  it('501 business_required is an edition refusal and no 403', () => {
    const err = new ApiError(501, 'business_required', 'm')
    expect(err.isEditionRefusal).toBe(true)
    expect(err.isForbidden).toBe(false)
    expect(err.isForbiddenOrEdition).toBe(false)
  })

  it.each(['forbidden', 'step_up_required', 'tenant_admission_required'])(
    'a 403 %s is what it was: isForbidden',
    (code) => {
      const err = new ApiError(403, code, 'm')
      expect(err.isEditionRefusal).toBe(false)
      expect(err.isForbidden).toBe(true)
      expect(err.isForbiddenOrEdition).toBe(true)
    },
  )

  it('a 403 that names Community, or no edition, is a role refusal', () => {
    const err = new ApiError(403, 'forbidden', 'm', 'r', {
      edition: 'community',
    })
    expect(err.isEditionRefusal).toBe(false)
    expect(err.isForbidden).toBe(true)
  })

  it('no other status is a refusal of either kind', () => {
    const err = new ApiError(404, 'not_found', 'm')
    expect(err.isForbidden).toBe(false)
    expect(err.isForbiddenOrEdition).toBe(false)
    expect(err.isEditionRefusal).toBe(false)
  })
})
