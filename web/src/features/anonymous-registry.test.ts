// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { ANONYMOUS_VIEWS } from './anonymous-registry'
import { ANONYMOUS_EXTENSION_ROUTES, EXTENSION_ROUTES } from './extensions'
import { FEATURE_VIEWS } from './registry'
import census from './route-census.json'

describe('anonymous extension registry', () => {
  it('seals the shared array and every route and login-choice record', () => {
    expect(Object.isFrozen(ANONYMOUS_VIEWS)).toBe(true)
    expect(Reflect.set(ANONYMOUS_VIEWS, 'length', 0)).toBe(false)
    for (const view of ANONYMOUS_VIEWS) {
      expect(Object.isFrozen(view), view.id).toBe(true)
      expect(Reflect.set(view, 'path', '/untrusted')).toBe(false)
      expect(Reflect.set(view, 'loginLabel', () => 'Untrusted')).toBe(false)
    }
  })

  it('uses distinct identities across authenticated and anonymous views', () => {
    const ids = [...FEATURE_VIEWS, ...ANONYMOUS_VIEWS].map(({ id }) => id)
    expect(ids.filter((id, index) => ids.indexOf(id) !== index)).toEqual([])
    expect(ANONYMOUS_VIEWS.filter(({ id }) => id.trim() === '')).toEqual([])
  })

  it('has independent witnesses for exactly the declared anonymous views', () => {
    const identity = (view: { id: string; path: string }) =>
      `${view.id}:${view.path}`
    expect(ANONYMOUS_EXTENSION_ROUTES.map(identity).sort()).toEqual(
      ANONYMOUS_VIEWS.map(identity).sort(),
    )
    expect(
      ANONYMOUS_EXTENSION_ROUTES.filter(({ heading }) => heading.trim() === ''),
    ).toEqual([])
  })

  it('publishes static absolute paths that do not shadow another route', () => {
    const paths = [
      ...census.paths,
      ...EXTENSION_ROUTES.map(({ path }) => path),
      ...ANONYMOUS_EXTENSION_ROUTES.map(({ path }) => path),
    ]
    expect(
      paths.filter((path, index) => paths.indexOf(path) !== index),
    ).toEqual([])
    for (const { path } of ANONYMOUS_VIEWS) {
      expect(path).toMatch(/^\/[a-zA-Z0-9_-]+(?:\/[a-zA-Z0-9_-]+)*$/)
      expect(path.endsWith('/')).toBe(false)
    }
  })
})
