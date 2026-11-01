// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  Fragment,
  useEffect,
  useSyncExternalStore,
  type ReactNode,
} from 'react'
import {
  preferenceStorage,
  PREFERENCE_KEYS,
  type PreferencePatch,
} from '@/lib/preference-storage'
import { usePreferencesStore } from '@/stores/preferences'
import { useClientSettings } from '@/features/settings/preferences'
import { useWorkspaceStore } from '@/stores/workspace'
import { usePlayground } from '@/features/api-playground/use-playground'
import i18n, { setLanguage, LANGUAGE_CODES } from '@/lib/i18n'
import { moduleOn } from '@/stores/modules'
import { toast } from '@/components/ui/toaster'
import { useUiState } from './use-ui-state'

const stores = [
  {
    reset: () =>
      usePreferencesStore.setState(usePreferencesStore.getInitialState(), true),
    hydrate: () => usePreferencesStore.persist.rehydrate(),
  },
  {
    reset: () =>
      useClientSettings.setState(useClientSettings.getInitialState(), true),
    hydrate: () => useClientSettings.persist.rehydrate(),
  },
  {
    reset: () =>
      useWorkspaceStore.setState(useWorkspaceStore.getInitialState(), true),
    hydrate: () => useWorkspaceStore.persist.rehydrate(),
  },
  {
    reset: () => usePlayground.setState(usePlayground.getInitialState(), true),
    hydrate: () => usePlayground.persist.rehydrate(),
  },
]

/** Hydrate before mounting preference consumers, once per verified user/tenant lifetime. */
export function PreferencesSync({ children }: { children: ReactNode }) {
  const { available, query, save, scope } = useUiState()
  const owner = available ? (scope?.key ?? null) : null
  const loaded = available && query.data !== undefined
  const ready = useSyncExternalStore(
    preferenceStorage.subscribe,
    preferenceStorage.getOwner,
    preferenceStorage.getOwner,
  )
  useEffect(() => {
    if (
      (available && query.isError && query.data === undefined) ||
      (scope?.key && !available && !scope.pending && moduleOn('consoleviews'))
    )
      toast.error(i18n.t('nav:personal.saveFailed'))
  }, [available, query.isError, query.data, scope?.key, scope?.pending])
  useEffect(() => {
    if (!owner || !scope?.live() || !query.data) return
    const identity = JSON.parse(owner.slice(owner.indexOf(':') + 1)) as string[]
    const stored = query.data.preferences ?? {}
    let active = true
    let sent = Promise.resolve()
    let unsaved: PreferencePatch = {}
    const pending: PreferencePatch = {}
    // Read only this person's legacy partitions; global preferences move once.
    const migrated: string[] = []
    const values = { ...stored }
    for (const name of PREFERENCE_KEYS) {
      const scoped = `${name}:${JSON.stringify(identity)}`
      for (const key of [scoped, name]) {
        try {
          const legacy = localStorage.getItem(key)
          if (legacy !== null) {
            migrated.push(key)
            if (!(name in values)) values[name] = pending[name] = legacy
          }
        } catch {
          /* Browser storage may be unavailable. */
        }
      }
    }
    const persist = (preferences: PreferencePatch) => {
      if (!active || !scope.live()) return
      sent = sent
        .then(async () => {
          if (!active || !scope.live()) return
          unsaved = { ...unsaved, ...preferences }
          await save({ preferences: unsaved })
          unsaved = {}
          for (const key of migrated) {
            try {
              localStorage.removeItem(key)
            } catch {
              /* Retry migration on the next load. */
            }
          }
        })
        .catch(() => {
          if (active && scope.live())
            toast.error(i18n.t('nav:personal.saveFailed'))
        })
    }
    preferenceStorage.attach(null, {}, identity)
    for (const store of stores) store.reset()
    preferenceStorage.attach(null, values, identity)
    for (const store of stores) void store.hydrate()
    const language = preferenceStorage.getItem('olivares.lang')
    preferenceStorage.attach(persist, values, identity)
    void setLanguage(
      LANGUAGE_CODES.find(
        (code) => code === (language ?? navigator.language).split('-')[0],
      ) ?? 'en',
      false,
    )
    if (Object.keys(pending).length) persist(pending)
    else
      for (const key of migrated) {
        try {
          localStorage.removeItem(key)
        } catch {
          /* Retry next load. */
        }
      }
    return () => {
      active = false
      preferenceStorage.attach(null, {})
      for (const store of stores) store.reset()
    }
    // A save refreshes query.data; it must not rehydrate and remount the screen.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [owner, loaded, scope?.generation, scope?.live])
  if (scope?.pending && moduleOn('consoleviews')) return null
  if (owner && query.isPending) return null
  if (owner && loaded && ready !== owner.slice(owner.indexOf(':') + 1))
    return null
  return <Fragment key={ready ?? 'temporary'}>{children}</Fragment>
}
