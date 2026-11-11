// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { preferenceStorage } from '@/lib/preference-storage'
import { create } from 'zustand'
import { persist, createJSONStorage } from 'zustand/middleware'
import type { EditorId } from '@/lib/editor-links'

const object = (value: unknown): Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {}

/** How the console draws a clock. Records stay in UTC either way. */
export type ClockFormat = '24' | '12'

export interface SessionDefaults {
  tool: string
  account: string
  model: string
  effort: string
  folder: string
}
/** A command a person added to a folder's project scripts. */
export interface AddedScript {
  name: string
  command: string
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

/** Which key sends a message from the session composer: Enter (Shift+Enter breaks a line),
 * or Ctrl+Enter (Enter breaks a line). */
export type SendKey = 'enter' | 'modEnter'
/** What the send key does while the tool works: line the message up behind the turn, or
 * steer it into the turn. The other choice is the same key with Ctrl. */
export type SendWhileRunning = 'queue' | 'steer'

export const CLIENT_SETTING_DEFAULTS = {
  sendKey: 'enter' as SendKey,
  sendWhileRunning: 'queue' as SendWhileRunning,
  clock: '24' as ClockFormat,
  startPage: 'sessions' as StartPageId,
  confirmStop: true,
  editor: 'vscode' as EditorId,
  editorSsh: '',
  sessionNotificationsFor: null as string | null,
  projectScripts: {} as Record<string, AddedScript[]>,
  sessionDefaults: {
    tool: '',
    account: '',
    model: '',
    effort: '',
    folder: '',
  } as SessionDefaults,
  notificationEvents: {
    needsYou: true,
    finished: true,
    failed: true,
  } as NotificationEvents,
  inAppEvents: {
    needsYou: true,
    finished: true,
    failed: true,
  } as NotificationEvents,
  /** Settings › Keyboard: the keys a person chose for a row of the table, by `ruleId`,
   * as text. The keybindings read them against the table (lib/keybindings/effective.ts):
   * this store imports nothing of theirs, since the registry's views read it. */
  keyOverrides: {} as Record<string, string>,
}

interface ClientSettings {
  sendKey: SendKey
  sendWhileRunning: SendWhileRunning
  clock: ClockFormat
  startPage: StartPageId
  confirmStop: boolean
  /** The editor that "Open in editor" links start; kept as this user's preference. */
  editor: EditorId
  /** [user@]host[:port] the editor reaches the session's folder by; empty means the host the console is served from. */
  editorSsh: string
  /** Browser permission is local; the opt-in belongs to this person and tenant. */
  sessionNotificationsFor: string | null
  /** The scripts this person added, by the folder's workspace reference. */
  projectScripts: Record<string, AddedScript[]>
  sessionDefaults: SessionDefaults
  /** The session events this browser shows as system notifications. */
  notificationEvents: NotificationEvents
  /** The session events the console tells in a toast while a tab is in view. */
  inAppEvents: NotificationEvents
  keyOverrides: Record<string, string>
  /** Keeps these key edits, checked by `setKeyOverride` (lib/keybindings/effective.ts);
   * false, and nothing kept, when the saved settings cannot hold them. */
  replaceKeyOverrides: (keyOverrides: Record<string, string>) => boolean
  /** Gives every row back the table's keys at once: two rows that swapped keys refuse
   * each other's reset one by one. */
  resetKeyOverrides: () => void
  setSessionDefaults: (defaults: Partial<SessionDefaults>) => void
  setNotificationEvents: (events: Partial<NotificationEvents>) => void
  setInAppEvents: (events: Partial<NotificationEvents>) => void
  setSendKey: (sendKey: SendKey) => void
  setSendWhileRunning: (sendWhileRunning: SendWhileRunning) => void
  setClock: (clock: ClockFormat) => void
  setStartPage: (startPage: StartPageId) => void
  setConfirmStop: (confirmStop: boolean) => void
  setEditor: (editor: EditorId) => void
  setEditorSsh: (editorSsh: string) => void
  setSessionNotificationsFor: (owner: string | null) => void
  setProjectScripts: (workspaceRef: string, scripts: AddedScript[]) => void
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
      setInAppEvents: (events) =>
        set((s) => ({ inAppEvents: { ...s.inAppEvents, ...events } })),
      replaceKeyOverrides: (keyOverrides) => {
        if (!settingsFit({ keyOverrides })) return false
        set({ keyOverrides })
        return true
      },
      resetKeyOverrides: () => set({ keyOverrides: {} }),
      setSendKey: (sendKey) => set({ sendKey }),
      setSendWhileRunning: (sendWhileRunning) => set({ sendWhileRunning }),
      setClock: (clock) => set({ clock }),
      setStartPage: (startPage) => set({ startPage }),
      setConfirmStop: (confirmStop) => set({ confirmStop }),
      setEditor: (editor) => set({ editor }),
      setEditorSsh: (editorSsh) => set({ editorSsh }),
      setSessionNotificationsFor: (sessionNotificationsFor) =>
        set({ sessionNotificationsFor }),
      setProjectScripts: (workspaceRef, scripts) =>
        set((s) => ({
          projectScripts: { ...s.projectScripts, [workspaceRef]: scripts },
        })),
      // Restoring the preferences keeps the scripts a person added: they are not a preference.
      restore: () =>
        set((s) => ({
          ...CLIENT_SETTING_DEFAULTS,
          projectScripts: s.projectScripts,
        })),
    }),
    {
      storage: createJSONStorage(() => preferenceStorage),
      name: 'olivares.settings',
      merge: (persisted, current) => {
        const saved = persisted as Partial<ClientSettings> | undefined
        return {
          ...current,
          ...saved,
          // Anything but the other choice is the default: a stored blob is untrusted.
          sendKey: saved?.sendKey === 'modEnter' ? 'modEnter' : 'enter',
          sendWhileRunning:
            saved?.sendWhileRunning === 'steer' ? 'steer' : 'queue',
          projectScripts: Object.fromEntries(
            Object.entries(object(saved?.projectScripts)).map(
              ([folder, list]) => [
                folder,
                (Array.isArray(list) ? list : []).filter(
                  (item): item is AddedScript =>
                    typeof item?.name === 'string' &&
                    typeof item?.command === 'string',
                ),
              ],
            ),
          ),
          // Key edits stored as text; the keybindings check each against the table.
          keyOverrides: Object.fromEntries(
            Object.entries(object(saved?.keyOverrides)).filter(
              (edit): edit is [string, string] => typeof edit[1] === 'string',
            ),
          ),
          sessionDefaults: Object.fromEntries(
            Object.entries(CLIENT_SETTING_DEFAULTS.sessionDefaults).map(
              ([field, fallback]) => {
                const value =
                  saved?.sessionDefaults?.[field as keyof SessionDefaults]
                return [field, typeof value === 'string' ? value : fallback]
              },
            ),
          ) as unknown as SessionDefaults,
          notificationEvents: eventChoices(saved?.notificationEvents),
          inAppEvents: eventChoices(saved?.inAppEvents),
        }
      },
    },
  ),
)

/** The server keeps one preference up to 16384 bytes; stay under it, and say no past it. */
const MAX_VALUE_BYTES = 16_000

/** Whether the settings, with this change, still fit that bound. */
function settingsFit(change: Partial<ClientSettings>) {
  // As the store saves itself: its state and version, as JSON.
  const value = JSON.stringify({
    state: { ...useClientSettings.getState(), ...change },
    version: 0,
  })
  return new TextEncoder().encode(value).length <= MAX_VALUE_BYTES
}

/** Whether the settings, with these scripts kept for this folder, still fit that bound. */
export function scriptsFit(workspaceRef: string, scripts: AddedScript[]) {
  const { projectScripts } = useClientSettings.getState()
  return settingsFit({
    projectScripts: { ...projectScripts, [workspaceRef]: scripts },
  })
}

/** A stored choice per event; anything but a boolean is the default. */
function eventChoices(saved: unknown): NotificationEvents {
  const stored = object(saved)
  return Object.fromEntries(
    Object.entries(CLIENT_SETTING_DEFAULTS.notificationEvents).map(
      ([event, fallback]) => {
        const value = stored[event]
        return [event, typeof value === 'boolean' ? value : fallback]
      },
    ),
  ) as unknown as NotificationEvents
}

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
