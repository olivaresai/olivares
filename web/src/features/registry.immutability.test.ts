// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { FEATURE_VIEWS, type FeatureView } from './registry'

describe('the shared navigation permission registry', () => {
  it('rejects array mutations through aliases without replacing or reordering views', () => {
    const alias = FEATURE_VIEWS as FeatureView[]
    const before = [...alias]
    const replacement = { ...before[0], permission: 'untrusted:permission' }
    try {
      expect(Reflect.set(alias, '0', replacement)).toBe(false)
      expect(() => alias.push(replacement)).toThrow(TypeError)
      expect(() => alias.splice(0, 1, replacement)).toThrow(TypeError)
      expect(alias).toEqual(before)
    } finally {
      // Keep the red test isolated when run against the old mutable producer.
      if (!Object.isFrozen(alias)) alias.splice(0, alias.length, ...before)
    }
  })

  it('rejects permission replacement through an element alias', () => {
    const alias = FEATURE_VIEWS.find((view) => view.permission !== undefined)!
    const before = alias.permission
    try {
      expect(Reflect.set(alias, 'permission', 'untrusted:permission')).toBe(
        false,
      )
      expect(() =>
        Object.defineProperty(alias, 'permission', {
          value: 'untrusted:permission',
        }),
      ).toThrow(TypeError)
      expect(alias.permission).toBe(before)
    } finally {
      if (!Object.isFrozen(alias)) alias.permission = before
    }
  })

  it('seals every exported record, including entries without a permission', () => {
    expect(FEATURE_VIEWS.length).toBeGreaterThan(0)
    expect(Object.isFrozen(FEATURE_VIEWS)).toBe(true)
    for (const view of FEATURE_VIEWS)
      expect(Object.isFrozen(view), view.id).toBe(true)
  })
})
