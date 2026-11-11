// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SETTINGS STORE LOADS IN ANY ORDER. Almost every module reads it, the registry's own
// views among them (through the session's context), so it reads nothing that reads the
// registry back: whichever module loads first, the registry ends whole. The registry
// first and the keybindings first are the orders that broke when the store read the
// keybindings (a view, or the table's COST_VIEW, still half built); the settings first
// and the session context first held even then, and guard the order the console loads.
import { describe, expect, it, vi } from 'vitest'

async function registryAfter(first: () => Promise<unknown>) {
  vi.resetModules()
  await first()
  const { COST_VIEW, FEATURE_VIEWS } = await import('@/features/registry')
  expect(FEATURE_VIEWS.every((view) => typeof view?.id === 'string')).toBe(true)
  expect(typeof COST_VIEW.id).toBe('string')
}

describe('the settings, the keybindings and the registry load in any order', () => {
  it('loads the settings first', () =>
    registryAfter(() => import('@/features/settings/preferences')))

  it('loads the session context first', () =>
    registryAfter(() => import('@/lib/auth/context')))

  it('loads the registry first', () =>
    registryAfter(() => import('@/features/registry')))

  it('loads the keybindings first', () =>
    registryAfter(() => import('@/lib/keybindings/model')))
})
