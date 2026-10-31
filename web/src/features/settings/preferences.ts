// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { preferenceStorage } from '@/lib/preference-storage'
import { create } from 'zustand'
import { persist, createJSONStorage } from 'zustand/middleware'
import type { EditorId } from '@/lib/editor-links'

/** How the console draws a clock. Records stay in UTC either way. */
export type ClockFormat = '24' | '12'

export interface SessionDefaults {
  tool: string
  account: string
  model: string
  folder: string
}
export interface NotificationEvents {
  needsYou: boolean
  finished: boolean
  failed: boolean
}

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
  editor: 'vscode' as EditorId,
  editorSsh: '',
  sessionNotificationsFor: null as string | null,
  sessionDefaults: {
    tool: '',
    account: '',
    model: '',
    folder: '',
  } as SessionDefaults,
  notificationEvents: {
    needsYou: true,
    finished: true,
    failed: true,
  } as NotificationEvents,
}

interface ClientSettings {
  clock: ClockFormat
  startPage: StartPageId
  confirmStop: boolean
  /** The editor that "Open in editor" links start; kept as this user's preference. */
  editor: EditorId
  /** [user@]host[:port] the editor reaches the session's folder by; empty means the host the console is served from. */
  editorSsh: string
  /** Browser permission is local; the opt-in belongs to this person and tenant. */
  sessionNotificationsFor: string | null
  sessionDefaults: SessionDefaults
  notificationEvents: NotificationEvents
  setSessionDefaults: (defaults: Partial<SessionDefaults>) => void
  setNotificationEvents: (events: Partial<NotificationEvents>) => void
  setClock: (clock: ClockFormat) => void
  setStartPage: (startPage: StartPageId) => void
  setConfirmStop: (confirmStop: boolean) => void
  setEditor: (editor: EditorId) => void
  setEditorSsh: (editorSsh: string) => void
  setSessionNotificationsFor: (owner: string | null) => void
  restore: () => void
}

/** Preferences that have no other store: clock, start page, confirm before stop, editor. */
export const useClientSettings = create<ClientSettings>()(
  persist(
    (set) => ({
      ...CLIENT_SETTING_DEFAULTS,
      setSessionDefaults: (defaults) =>
        set((s) => ({
          sessionDefaults: { ...s.sessionDefaults, ...defaults },
        })),
      setNotificationEvents: (events) =>
        set((s) => ({
          notificationEvents: { ...s.notificationEvents, ...events },
        })),
      setClock: (clock) => set({ clock }),
      setStartPage: (startPage) => set({ startPage }),
      setConfirmStop: (confirmStop) => set({ confirmStop }),
      setEditor: (editor) => set({ editor }),
      setEditorSsh: (editorSsh) => set({ editorSsh }),
      setSessionNotificationsFor: (sessionNotificationsFor) =>
        set({ sessionNotificationsFor }),
      restore: () => set({ ...CLIENT_SETTING_DEFAULTS }),
    }),
    {
      storage: createJSONStorage(() => preferenceStorage),
      name: 'olivares.settings',
      merge: (persisted, current) => {
        const saved = persisted as Partial<ClientSettings> | undefined
        return {
          ...current,
          ...saved,
          sessionDefaults: Object.fromEntries(
            Object.entries(CLIENT_SETTING_DEFAULTS.sessionDefaults).map(
              ([field, fallback]) => {
                const value =
                  saved?.sessionDefaults?.[field as keyof SessionDefaults]
                return [field, typeof value === 'string' ? value : fallback]
              },
            ),
          ) as unknown as SessionDefaults,
          notificationEvents: Object.fromEntries(
            Object.entries(CLIENT_SETTING_DEFAULTS.notificationEvents).map(
              ([event, fallback]) => {
                const value =
                  saved?.notificationEvents?.[event as keyof NotificationEvents]
                return [event, typeof value === 'boolean' ? value : fallback]
              },
            ),
          ) as unknown as NotificationEvents,
        }
      },
    },
  ),
)

export function notificationOwner(
  user: string | null | undefined,
  tenant: string | null | undefined,
): string | null {
  return user && tenant ? JSON.stringify([user, tenant]) : null
}

/** The IANA zone the operator's browser is in. */
export function localTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
}
