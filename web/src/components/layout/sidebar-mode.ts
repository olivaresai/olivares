// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIDEBAR'S TWO WIDTHS (console 1.0): FULL (240 px, navigation with
// labels) and RAIL (56 px, the same destinations as icons). Folding never hides it.
//
// Who decides:
//   · the PERSON, with the fold button, Mod+B or the palette. Their choice is
//     `sidebarCollapsed` in `olivares.prefs` (the browser copy, shown at once; a value
//     stored before 1.0 meant "hidden" and now reads as the rail) and a private row on
//     the engine beside their favorites (GET/PUT /v1/m/consoleviews/ui-state), which
//     wins once read, so the choice follows them to another browser;
//   · a WORK PAGE, which always shows the rail: its own list is the one sidebar there.
import { useEffect, useRef } from 'react'
import { create } from 'zustand'
import { savedViewsApi } from '@/features/saved-views/api'
import { moduleOn, useModulesStore } from '@/stores/modules'
import { usePersonalNavigation } from '@/features/navigation/personal-navigation'
import { usePreferencesStore, type SidebarChoice } from '@/stores/preferences'
import { frameFor } from './page-frames'

export type SidebarMode = 'full' | 'rail'

/** The width the sidebar takes on a page of this frame. */
export function sidebarModeFor(
  frame: 'work' | 'document',
  folded: boolean,
): SidebarMode {
  return frame === 'work' || folded ? 'rail' : 'full'
}

const asSidebar = (folded: boolean): SidebarMode => (folded ? 'rail' : 'full')

/** The navigation drawn over a work page: open on the page it was opened on, so leaving
 * that page closes it. Never stored. */
export const useNavOverlay = create<{
  openOn: string | null
  returnFocusTo: HTMLElement | null
  setOpenOn: (pathname: string | null, opener?: HTMLElement | null) => void
}>()((set) => ({
  openOn: null,
  returnFocusTo: null,
  setOpenOn: (openOn, opener) =>
    set((s) => {
      const control = opener ?? document.activeElement
      return {
        openOn,
        returnFocusTo:
          openOn !== null && s.openOn === null && control instanceof HTMLElement
            ? control
            : s.returnFocusTo,
      }
    }),
}))

/**
 * THE ONE FOLD GESTURE, shared by the sidebar's button, Mod+B and the palette. On a
 * document page it folds or unfolds the sidebar and that is the person's stored choice;
 * on a work page the sidebar is the rail whatever is stored, so the gesture opens or closes
 * the navigation over the page and the stored choice is left alone.
 */
export function foldOrOverlay(pathname: string, opener?: HTMLElement | null) {
  if (frameFor(pathname) === 'work') {
    const overlay = useNavOverlay.getState()
    overlay.setOpenOn(overlay.openOn === pathname ? null : pathname, opener)
    return
  }
  usePreferencesStore.getState().toggleSidebar()
}

/** Sidebar and Favorites share the verified, revocable personal-navigation lifetime. */
export interface SidebarScope {
  generation: number
  key: string | null
  tenant: string | null
  live: () => boolean
  subscribe: (listener: () => void) => () => void
}

export function SidebarSync(): null {
  const personal = usePersonalNavigation()
  useSidebarSync(personal?.scope ?? null)
  return null
}

/** Pending browser copies stay with their owner. Every request and completion is bound
 * to the same verified lifetime; module-off keeps the browser copy without requests. */
export function useSidebarSync(scope: SidebarScope | null) {
  // A gesture while identity is being verified remains quarantined until the same
  // person and credential generation are established. It never enters another copy.
  const gesture = useRef<{
    key: string
    generation: number
    collapsed: boolean
  } | null>(null)
  const modules = useModulesStore((s) => s.off)
  const on = !modules.has('consoleviews')
  const generation = scope?.generation ?? null
  const key = scope?.key ?? null
  const tenant = scope?.tenant ?? null
  const lifetime = scope?.live
  const subscribe = scope?.subscribe
  useEffect(() => {
    const prefs = () => usePreferencesStore.getState()
    if (!key || !tenant || !lifetime || !subscribe || generation === null) {
      gesture.current = null
      return
    }
    if (
      gesture.current?.key !== key ||
      gesture.current?.generation !== generation
    )
      gesture.current = null
    if (!lifetime())
      return usePreferencesStore.subscribe((s, prev) => {
        if (!s.sidebarOwner && s.sidebarCollapsed !== prev.sidebarCollapsed) {
          gesture.current = { key, generation, collapsed: s.sidebarCollapsed }
        }
      })
    const calls = new AbortController()
    const live = () => !calls.signal.aborted && lifetime()
    const retire = () => {
      if (lifetime()) return
      calls.abort()
      if (prefs().sidebarOwner === key) prefs().setSidebarOwner(null)
    }
    const stopLifetime = subscribe(retire)
    const browserGesture = gesture.current
    gesture.current = null
    prefs().setSidebarOwner(key)
    if (browserGesture) {
      prefs().setSidebarCollapsed(browserGesture.collapsed)
      prefs().setSidebarUnsent(true)
    }
    const guard = () => {
      if (!live() || !moduleOn('consoleviews')) calls.abort()
      calls.signal.throwIfAborted()
    }
    const options = {
      tenant,
      signal: calls.signal,
      dispatchGuard: guard,
      sessionEffects: 'none' as const,
    }
    let queue: Promise<unknown> = Promise.resolve()
    const save = (choice: SidebarChoice) => {
      queue = queue
        .then(() => {
          guard()
          return savedViewsApi.saveUiState(
            { sidebar: asSidebar(choice.collapsed) },
            options,
          )
        })
        .then(
          () => {
            if (live()) prefs().sidebarSaved(key, choice)
          },
          () => {
            // The owned browser copy was marked pending at the gesture, before dispatch.
          },
        )
    }
    let read = false
    let applying = false
    const stop = usePreferencesStore.subscribe((s, prev) => {
      if (
        applying ||
        !live() ||
        s.sidebarOwner !== key ||
        s.sidebarCollapsed === prev.sidebarCollapsed
      )
        return
      if (on && read) save(s.sidebarChoices[key])
    })
    const stopModule = useModulesStore.subscribe(() => {
      if (moduleOn('consoleviews') !== on) calls.abort()
    })
    if (on) {
      savedViewsApi
        .uiState(options)
        .then((engine) => {
          if (!live()) return
          read = true
          const choice = prefs().sidebarChoices[key]
          if (choice.pending) save(choice)
          else if (
            engine.stored &&
            (engine.sidebar === 'full' || engine.sidebar === 'rail')
          ) {
            applying = true
            prefs().setSidebarCollapsed(engine.sidebar === 'rail')
            applying = false
          } else if (choice.collapsed) {
            prefs().setSidebarUnsent(true)
            save(prefs().sidebarChoices[key])
          }
        })
        .catch(() => {
          // A failed read must not erase a gesture made while it was pending.
          if (live()) read = true
        })
    }
    return () => {
      calls.abort()
      stop()
      stopLifetime()
      stopModule()
      if (prefs().sidebarOwner === key) prefs().setSidebarOwner(null)
    }
  }, [key, tenant, generation, lifetime, subscribe, on, modules])
}
