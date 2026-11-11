// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHETHER A VIEW OPENS AT THE EDITION HELD HERE, decided once: editionCovers copies the
// engine's license.EditionCovers (core/license/envelope.go), and useOpensAtEdition reads the
// licensed edition server-info names, never a guess, in three states: not answered (all
// open), a known value (editionCovers), a missing or unknown one (nothing above Community).
import { renderHook } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { PaidEdition } from '@/components/ui/edition-note'

const server = vi.hoisted(() => ({
  answer: { isSuccess: false, data: undefined } as {
    isSuccess: boolean
    data?: Record<string, unknown>
  },
}))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => server.answer,
}))

import { editionCovers, useOpensAtEdition } from './commercial-edition'

const answered = (data: Record<string, unknown>) => {
  server.answer = { isSuccess: true, data }
}

const opens = (view: { minEdition?: PaidEdition }) =>
  renderHook(() => useOpensAtEdition()).result.current(view)

describe('editionCovers', () => {
  it('orders the editions as the engine does', () => {
    expect(editionCovers('enterprise', 'business')).toBe(true)
    expect(editionCovers('business', 'business')).toBe(true)
    expect(editionCovers('business', 'enterprise')).toBe(false)
    expect(editionCovers('engineer', 'engineer')).toBe(true)
    expect(editionCovers('engineer', 'business')).toBe(false)
    expect(editionCovers('community', 'engineer')).toBe(false)
  })

  it('an absent minimum is held by every edition, even an unknown one', () => {
    expect(editionCovers('community')).toBe(true)
    expect(editionCovers(null)).toBe(true)
    expect(editionCovers('platinum', undefined)).toBe(true)
  })

  it('an unknown or absent licensed value holds nothing above Community', () => {
    for (const licensed of ['platinum', '', null, undefined])
      expect(editionCovers(licensed, 'engineer')).toBe(false)
  })
})

describe('useOpensAtEdition', () => {
  beforeEach(() => {
    server.answer = { isSuccess: false, data: undefined }
  })

  it('opens every view while server-info has not answered: the engine is what refuses', () => {
    expect(opens({ minEdition: 'enterprise' })).toBe(true)
  })

  it('a view with no minimum opens in every edition', () => {
    answered({ edition: 'community', license: { status: 'none' } })
    expect(opens({})).toBe(true)
  })

  it('a Community build holds nothing above Community, whatever license it shows', () => {
    answered({
      edition: 'community',
      licensed_edition: 'business',
      license: { status: 'valid' },
    })
    expect(opens({ minEdition: 'engineer' })).toBe(false)
  })

  it('a Business build opens what its licensed edition holds, and no more', () => {
    answered({
      edition: 'enterprise',
      licensed_edition: 'engineer',
      license: { status: 'valid' },
    })
    expect(opens({ minEdition: 'engineer' })).toBe(true)
    expect(opens({ minEdition: 'business' })).toBe(false)
  })

  // On main a missing value is a product this build does not know
  // (core/api/handlers_core.go), which the engine holds to Community: never guessed from
  // the support profile, however valid the license looks.
  it('a missing licensed value opens nothing above Community', () => {
    answered({
      edition: 'enterprise',
      license: { status: 'valid', support_profile: 'enterprise' },
    })
    expect(opens({ minEdition: 'engineer' })).toBe(false)
    expect(opens({})).toBe(true)
  })

  it('an unknown licensed value opens nothing above Community', () => {
    answered({
      edition: 'enterprise',
      licensed_edition: 'platinum',
      license: { status: 'valid' },
    })
    expect(opens({ minEdition: 'engineer' })).toBe(false)
    expect(opens({})).toBe(true)
  })
})
