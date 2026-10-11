// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIDEBAR'S WIDTH IS A PERSON'S CHOICE, AND A WORK PAGE'S RULE. Folding gives the
// rail (56 px), never nothing; a work page (Sessions, the pages that declare the work
// frame) always shows the rail, because its own list is the one sidebar there; and the
// choice is the person's, kept on the engine beside their favorites, so it follows them
// to another browser.
import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const engine = vi.hoisted(() => ({
  on: true,
  stored: undefined as undefined | { stored: boolean; sidebar?: string },
  fail: false,
  saves: [] as unknown[],
  failSave: false,
}))
import { useModulesStore } from '@/stores/modules'
vi.mock('@/features/saved-views/api', () => ({
  savedViewsApi: {
    uiState: () =>
      engine.fail
        ? Promise.reject(new Error('engine down'))
        : Promise.resolve(engine.stored ?? { stored: false }),
    saveUiState: (state: unknown) => {
      engine.saves.push(state)
      return engine.failSave
        ? Promise.reject(new Error('engine down'))
        : Promise.resolve({ ...(state as object), stored: true })
    },
  },
}))

const live = () => true
const subscribe = () => () => {}
const scope = {
  key: 'sidebar:t1:u1',
  tenant: 't1',
  generation: 0,
  live,
  subscribe,
}

import { usePreferencesStore } from '@/stores/preferences'
import {
  foldOrOverlay,
  sidebarModeFor,
  useNavOverlay,
  useSidebarSync,
} from './sidebar-mode'

beforeEach(() => {
  engine.on = true
  useModulesStore.getState().setOff([])
  engine.stored = undefined
  engine.fail = false
  engine.saves = []
  engine.failSave = false
  usePreferencesStore.setState({
    sidebarCollapsed: false,
    sidebarUnsent: false,
    sidebarOwner: null,
    sidebarChoices: {},
  })
  useNavOverlay.setState({ openOn: null })
})
afterEach(() => usePreferencesStore.setState({ sidebarCollapsed: false }))

describe('sidebarModeFor', () => {
  it('is full on a document page until the person folds it, then the rail', () => {
    expect(sidebarModeFor('document', false)).toBe('full')
    expect(sidebarModeFor('document', true)).toBe('rail')
  })

  it('is the rail on a work page whatever the person stored', () => {
    expect(sidebarModeFor('work', false)).toBe('rail')
    expect(sidebarModeFor('work', true)).toBe('rail')
  })
})

describe('foldOrOverlay: the one fold gesture', () => {
  it('folds and unfolds the stored choice on a document page', () => {
    foldOrOverlay('/audit')
    expect(usePreferencesStore.getState().sidebarCollapsed).toBe(true)
    foldOrOverlay('/audit')
    expect(usePreferencesStore.getState().sidebarCollapsed).toBe(false)
  })

  it('on a work page opens and closes the navigation over it and never touches the stored choice', () => {
    foldOrOverlay('/sessions')
    expect(useNavOverlay.getState().openOn).toBe('/sessions')
    expect(usePreferencesStore.getState().sidebarCollapsed).toBe(false)
    foldOrOverlay('/sessions')
    expect(useNavOverlay.getState().openOn).toBeNull()
    expect(usePreferencesStore.getState().sidebarCollapsed).toBe(false)
  })
})

describe('the choice follows the person (engine copy)', () => {
  it('takes the stored engine choice over the browser copy', async () => {
    engine.stored = { stored: true, sidebar: 'rail' }
    renderHook(() => useSidebarSync(scope))
    await waitFor(() =>
      expect(usePreferencesStore.getState().sidebarCollapsed).toBe(true),
    )
    expect(engine.saves).toEqual([])
  })

  it('moves a folded browser copy to the engine once when nothing is stored', async () => {
    usePreferencesStore.setState({ sidebarCollapsed: true })
    renderHook(() => useSidebarSync(scope))
    await waitFor(() => expect(engine.saves).toEqual([{ sidebar: 'rail' }]))
  })

  it('saves each toggle after the engine copy is read', async () => {
    engine.stored = { stored: true, sidebar: 'full' }
    renderHook(() => useSidebarSync(scope))
    await waitFor(() => expect(engine.saves).toEqual([]))
    await new Promise((r) => setTimeout(r, 0))
    usePreferencesStore.getState().toggleSidebar()
    await waitFor(() => expect(engine.saves).toEqual([{ sidebar: 'rail' }]))
  })

  it('keeps a toggle made before the engine answered, and sends it', async () => {
    engine.stored = { stored: true, sidebar: 'full' }
    renderHook(() => useSidebarSync(scope))
    usePreferencesStore.getState().toggleSidebar()
    await waitFor(() => expect(engine.saves).toEqual([{ sidebar: 'rail' }]))
    expect(usePreferencesStore.getState().sidebarCollapsed).toBe(true)
  })

  it('sends a change whose save failed at the next read instead of taking the engine copy', async () => {
    engine.stored = { stored: true, sidebar: 'full' }
    engine.failSave = true
    const first = renderHook(() => useSidebarSync(scope))
    await new Promise((r) => setTimeout(r, 0))
    usePreferencesStore.getState().toggleSidebar()
    await waitFor(() =>
      expect(usePreferencesStore.getState().sidebarUnsent).toBe(true),
    )
    first.unmount()

    engine.failSave = false
    engine.saves = []
    renderHook(() => useSidebarSync(scope))
    await waitFor(() => expect(engine.saves).toEqual([{ sidebar: 'rail' }]))
    expect(usePreferencesStore.getState().sidebarCollapsed).toBe(true)
    await waitFor(() =>
      expect(usePreferencesStore.getState().sidebarUnsent).toBe(false),
    )
  })

  it('leaves the browser copy alone when the module is off or the engine cannot answer', async () => {
    engine.on = false
    useModulesStore.getState().setOff(['consoleviews'])
    usePreferencesStore.setState({ sidebarCollapsed: true })
    const off = renderHook(() => useSidebarSync(scope))
    usePreferencesStore.getState().toggleSidebar()
    expect(engine.saves).toEqual([])
    off.unmount()

    engine.on = true
    useModulesStore.getState().setOff([])
    engine.fail = true
    renderHook(() => useSidebarSync(scope))
    await new Promise((r) => setTimeout(r, 0))
    expect(usePreferencesStore.getState().sidebarCollapsed).toBe(false)
  })

  it('reads nothing for a principal that is not a person', () => {
    engine.stored = { stored: true, sidebar: 'rail' }
    renderHook(() => useSidebarSync(null))
    expect(usePreferencesStore.getState().sidebarCollapsed).toBe(false)
  })
})
