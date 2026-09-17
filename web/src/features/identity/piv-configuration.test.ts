// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { Whoami } from '@/lib/api/types'
import { isPivKnownUnconfigured } from './piv-configuration'

const base: Whoami = {
  kind: 'user',
  user_id: 'u1',
  actor: 'u1',
  display_name: 'Operator',
  superadmin: false,
  grants: [],
}

describe('isPivKnownUnconfigured', () => {
  it('is true only for exact false', () => {
    expect(
      isPivKnownUnconfigured({
        ...base,
        authentication_configuration: { piv_configured: false },
      }),
    ).toBe(true)
  })

  it('does not treat true, absent field, or absent principal as unconfigured', () => {
    expect(
      isPivKnownUnconfigured({
        ...base,
        authentication_configuration: { piv_configured: true },
      }),
    ).toBe(false)
    expect(isPivKnownUnconfigured(base)).toBe(false)
    expect(
      isPivKnownUnconfigured({
        ...base,
        authentication_configuration: undefined,
      }),
    ).toBe(false)
    expect(isPivKnownUnconfigured(null)).toBe(false)
    expect(isPivKnownUnconfigured(undefined)).toBe(false)
  })
})
