// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'
import { persist } from 'zustand/middleware'

/** How the console draws a clock. Records stay in UTC either way. */
export type ClockFormat = '24' | '12'

/**
 * Where a signed-in operator lands. Ids are registry view ids, resolved to a
 * path at the moment of use so a renamed route does not strand the preference.
 */
export const START_PAGE_IDS = [
  'home',
  'sessions',
  'workspaceDashboard',
  'providers',
  'deploy',
] as const

export type StartPageId = (typeof START_PAGE_IDS)[number]

export const CLIENT_SETTING_DEFAULTS = {
  clock: '24' as ClockFormat,
  startPage: 'home' as StartPageId,
  confirmStop: true,
}

interface ClientSettings {
  clock: ClockFormat
  startPage: StartPageId
  confirmStop: boolean
  setClock: (clock: ClockFormat) => void
  setStartPage: (startPage: StartPageId) => void
  setConfirmStop: (confirmStop: boolean) => void
  restore: () => void
}

/** Preferences that have no other store: clock, start page, confirm before stop. */
export const useClientSettings = create<ClientSettings>()(
  persist(
    (set) => ({
      ...CLIENT_SETTING_DEFAULTS,
      setClock: (clock) => set({ clock }),
      setStartPage: (startPage) => set({ startPage }),
      setConfirmStop: (confirmStop) => set({ confirmStop }),
      restore: () => set({ ...CLIENT_SETTING_DEFAULTS }),
    }),
    { name: 'olivares.settings' },
  ),
)

/** The IANA zone the operator's browser is in. */
export function localTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
}
