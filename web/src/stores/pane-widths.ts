// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'

/**
 * The widths a person gave the console's resizable panes, in px by pane id.
 *
 * The widths are the person's own interface state and live on the engine, so they follow
 * them to another browser (features/console/pane-widths-sync.tsx reads and saves them).
 * This store is what the layouts read: a layout needs no query client, and a pane with no
 * entry keeps its own default width.
 */
export type PaneWidthPatch = Readonly<Record<string, number>>

interface PaneWidthsState {
  /** Whose widths these are, or null when nobody is signed in. */
  owner: string | null
  /** True once the engine's copy for `owner` has been read. */
  loaded: boolean
  widths: Readonly<Record<string, number>>
  /** Panes the person changed since `owner` was set: a late read must not undo them. */
  touched: ReadonlySet<string>
  /** Where a change goes (a width of 0 means "back to the default"); null when it cannot. */
  persist: ((patch: PaneWidthPatch) => void) | null
  /** Bind the store to an identity and a way to save. A new identity starts empty. */
  attach: (
    owner: string | null,
    persist: ((patch: PaneWidthPatch) => void) | null,
  ) => void
  /** The engine's copy arrived: it fills every pane the person has not touched since. */
  hydrate: (owner: string, server: PaneWidthPatch) => void
  /** A width the person settled on (end of a drag, a key press). */
  commit: (id: string, px: number) => void
  /** Back to the pane's default. */
  reset: (id: string) => void
}

export const usePaneWidthsStore = create<PaneWidthsState>()((set, get) => ({
  owner: null,
  loaded: false,
  widths: {},
  touched: new Set(),
  persist: null,
  attach: (owner, persist) =>
    set((s) =>
      owner === s.owner
        ? { persist }
        : { owner, persist, loaded: false, widths: {}, touched: new Set() },
    ),
  hydrate: (owner, server) =>
    set((s) => {
      if (owner !== s.owner || s.loaded) return s
      const widths: Record<string, number> = { ...s.widths }
      for (const [id, px] of Object.entries(server))
        if (!s.touched.has(id)) widths[id] = px
      return { loaded: true, widths }
    }),
  commit: (id, px) => {
    const width = Math.round(px)
    // The engine reads 0 as "back to the default": a pane squeezed to nothing (no room
    // for it, or no number at all) is no width the person chose: neither kept nor sent.
    if (!(width >= 1)) return
    set((s) => ({
      widths: { ...s.widths, [id]: width },
      touched: new Set(s.touched).add(id),
    }))
    get().persist?.({ [id]: width })
  },
  reset: (id) => {
    set((s) => {
      const widths = { ...s.widths }
      delete widths[id]
      return { widths, touched: new Set(s.touched).add(id) }
    })
    get().persist?.({ [id]: 0 })
  },
}))

/** The stored width of one pane, or undefined while it has none (the pane's own default). */
export function usePaneWidth(id: string): number | undefined {
  return usePaneWidthsStore((s) => s.widths[id])
}
