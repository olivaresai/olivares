// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The provider-account door is its OWN route, gated on the account read tier alone. The
// profile and binding doors require their own tiers, and the sessions workspace requires
// run or live read, so a member holding only `sessions:account:read` would otherwise
// have no screen for a permission the engine declares. The route guard checks exactly
// the one permission an entry declares, which is why the entry must name this one.
import { createRouter } from '@tanstack/react-router'
import { describe, expect, it } from 'vitest'
import { routeTree } from '@/app/routes'
import { FEATURE_VIEWS } from '@/features/registry'

describe('the provider-account door', () => {
  const view = FEATURE_VIEWS.find((v) => v.path === '/provider-accounts')

  it('is registered under the account read tier, beside the profile and binding doors', () => {
    expect(view).toBeDefined()
    expect(view?.id).toBe('providerAccounts')
    expect(view?.permission).toBe('sessions:account:read')
    expect(view?.hub).toBe('operate')
    expect(view?.navigation).toEqual({
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'environments',
    })
  })

  it('is mounted by the router', () => {
    const router = createRouter({ routeTree })
    expect(Object.keys(router.routesById)).toContain('/app/provider-accounts')
  })
})
