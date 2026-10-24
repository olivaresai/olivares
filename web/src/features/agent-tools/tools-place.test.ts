// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE TOOLS PLACE (#2552). Which sign-in or key a session runs as was one idea on seven
// screens. The API key, account, profile and binding pages are now doors into Tools: each
// keeps its url and its own read tier, and mounts the same room.
import type { ReactElement } from 'react'
import { describe, expect, it } from 'vitest'
import { FEATURE_VIEWS, type FeatureView } from '@/features/registry'

const viewAt = (path: string): FeatureView => {
  const view = FEATURE_VIEWS.find((v) => v.path === path)
  if (!view) throw new Error(`no view at ${path}`)
  return view
}

/** The component a view's route mounts, under its Suspense boundary. */
const room = (view: FeatureView) =>
  (view.element() as ReactElement<{ children: ReactElement }>).props.children
    .type

describe('the Tools place', () => {
  const tools = viewAt('/agent-tools')

  it('opens for every principal who reads the tool accounts, not only a deployment administrator', () => {
    expect(tools.permission).toBe('sessions:account:read')
  })

  it.each([
    ['/providers', 'sessions:provider:read'],
    ['/provider-accounts', 'sessions:account:read'],
    ['/provider-profiles', 'sessions:profile:read'],
    ['/provider-bindings', 'sessions:profile-binding:read'],
  ])('%s opens Tools under its own read tier', (path, permission) => {
    const door = viewAt(path)
    expect(door.permission).toBe(permission)
    expect(door.doorTo).toBe('agent-tools')
    expect(room(door)).toBe(room(tools))
  })
})
