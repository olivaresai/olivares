// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Settings > Modules: every optional module can be turned on and off from the product
// (rule; ARCH C1). The engine keeps the selection in the deployment settings and
// applies it by restarting itself, so this screen chooses, applies, waits for the engine
// to come back and reads the result. One dense list, no tabs or cards.
import { useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { waitForEngineRestart } from '@/features/console/engine-restart'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { http, queryKeys } from '@/lib/api'
import { isOpenCoreSeam } from '@/lib/api/errors'
import { readable } from './module-names'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'

/** GET/PUT /v1/console/modules (core/api/module_selection.go). */
export interface ModuleState {
  name: string
  /** The administrator chose it. */
  selected: boolean
  /** It runs on this node now (selected, required, or always on). */
  running: boolean
  always_on?: boolean
  /** Its tables hold rows (ARCH, 09); omitted when false. */
  holds_data?: boolean
  /** The active add-ons that run it (ARCH 1b8edaf5, 09); omitted when none. It runs even
   * when not selected, and cannot be turned off while one of them is active. */
  activated_by?: string[]
  requires?: string[]
  required_by?: string[]
}
export interface ModuleSelection {
  modules: ModuleState[]
  restarting?: boolean
}

export const modulesApi = {
  get: () => http.get<ModuleSelection>('/v1/console/modules'),
  select: (selected: string[]) =>
    http.put<ModuleSelection>('/v1/console/modules', { selected }),
}

const modulesKey = ['settings', 'modules'] as const

export type ModuleRowState =
  'alwaysOn' | 'addOn' | 'keptOn' | 'on' | 'off' | 'needsRestart'

/** Whether an active add-on runs the module: then its switch is locked on. */
function byAddOn(m: ModuleState): boolean {
  return (m.activated_by?.length ?? 0) > 0
}

/** What a row says. "Needs restart" when what was chosen is not what runs: the
 * selection is recorded but the engine has not restarted into it. */
export function moduleRowState(m: ModuleState): ModuleRowState {
  if (m.always_on) return 'alwaysOn'
  if (byAddOn(m)) return 'addOn'
  if (!m.selected && m.running && (m.required_by?.length ?? 0) > 0)
    return 'keptOn'
  if (m.selected !== m.running) return 'needsRestart'
  return m.running ? 'on' : 'off'
}

/** A row's badge: its state, or what Apply will do to it. */
type RowState = ModuleRowState | 'willTurnOn' | 'willTurnOff'

const TONE: Record<RowState, 'success' | 'neutral' | 'warning' | 'accent'> = {
  alwaysOn: 'accent',
  addOn: 'accent',
  keptOn: 'neutral',
  on: 'success',
  off: 'neutral',
  needsRestart: 'warning',
  willTurnOn: 'warning',
  willTurnOff: 'warning',
}

/** Mounted for a system administrator only (Settings > Edition & modules); the engine
 * checks system:admin and the step-up itself. */
export function ModulesSettings() {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: modulesKey,
    queryFn: modulesApi.get,
    retry: false,
  })
  const [draft, setDraft] = useState<Set<string> | null>(null)
  const [restarting, setRestarting] = useState(false)
  // An add-on is named by its title from the activation catalog (Business), read only when a
  // module says an add-on runs it; a key the catalog does not name is shown as it is.
  const addOnRun = (query.data?.modules ?? []).some(byAddOn)
  const activation = useQuery({
    queryKey: consoleKeys.activation(),
    queryFn: consoleApi.getActivation,
    enabled: addOnRun,
    retry: false,
  })
  const addOnName = (key: string) =>
    activation.data?.addons.find((a) => a.key === key)?.title || key

  // The chosen set as the engine holds it; the draft starts there.
  const selected = useMemo(
    () =>
      new Set(
        (query.data?.modules ?? [])
          .filter((m) => m.selected)
          .map((m) => m.name),
      ),
    [query.data],
  )
  const choice = draft ?? selected
  const changes = useMemo(() => {
    let n = 0
    for (const m of query.data?.modules ?? [])
      if (!m.always_on && choice.has(m.name) !== m.selected) n++
    return n
  }, [query.data, choice])

  useEffect(() => {
    if (!restarting) return
    const ctl = new AbortController()
    void waitForEngineRestart({ signal: ctl.signal }).then(() => {
      if (ctl.signal.aborted) return
      setRestarting(false)
      setDraft(null)
      void queryClient.invalidateQueries({ queryKey: modulesKey })
      // The navigation and Home follow server-info modules_not_enabled.
      void queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
    })
    return () => ctl.abort()
  }, [restarting, queryClient])

  const apply = usePrivilegedMutation<void, ModuleSelection>({
    mutationFn: () =>
      modulesApi.select(
        (query.data?.modules ?? [])
          .filter((m) => !m.always_on && choice.has(m.name))
          .map((m) => m.name),
      ),
    successMessage: t('modules.applied'),
    stepUpAction: 'modules',
    onDone: (status) => {
      // Show what the engine answered; a read now could reach the stopping process.
      queryClient.setQueryData(modulesKey, { ...status, restarting: false })
      setDraft(null)
      if (status.restarting) setRestarting(true)
      else
        void queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
    },
  })

  return (
    <section
      className="mt-4 flex flex-col gap-2"
      aria-labelledby="modules-title"
      data-slot="modules-settings"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0 flex-1">
          <h2 id="modules-title" className="text-body font-semibold text-text">
            {t('modules.title')}
          </h2>
          <p className="text-caption text-text-2">{t('modules.description')}</p>
        </div>
        <Button
          variant="primary"
          size="sm"
          disabled={changes === 0 || apply.isPending || restarting}
          onClick={() => apply.mutate()}
        >
          {apply.isPending ? <Spinner size="sm" aria-hidden /> : null}
          {changes === 0
            ? t('modules.applyNone')
            : t('modules.apply', { count: changes })}
        </Button>
      </div>
      {restarting ? (
        <p
          role="status"
          className="flex items-center gap-2 text-caption text-text-2"
          data-slot="engine-restarting"
        >
          <Spinner size="sm" aria-hidden />
          {t('modules.restarting')}
        </p>
      ) : null}
      {query.isLoading ? (
        <div className="flex justify-center py-6">
          <Spinner />
        </div>
      ) : query.isError ? (
        <p className="text-body text-text-2" role="status">
          {isOpenCoreSeam(query.error)
            ? t('modules.unavailable')
            : t('modules.loadFailed')}
        </p>
      ) : (
        <ul className="divide-y divide-line" data-slot="modules-list">
          {(query.data?.modules ?? []).map((m) => {
            // Always on, or run by an active add-on: the switch is locked on.
            const fixed = !!m.always_on || byAddOn(m)
            const on = fixed || choice.has(m.name)
            const state: RowState =
              !fixed && on !== m.selected
                ? on
                  ? 'willTurnOn'
                  : 'willTurnOff'
                : moduleRowState(m)
            const name = t(`modules.names.${m.name}`, {
              defaultValue: readable(m.name),
            })
            return (
              <li
                key={m.name}
                className="flex min-w-0 items-start gap-3 py-2"
                data-module={m.name}
              >
                <Switch
                  aria-label={name}
                  checked={on}
                  disabled={fixed || restarting || apply.isPending}
                  onCheckedChange={(next) => {
                    const set = new Set(choice)
                    if (next) set.add(m.name)
                    else set.delete(m.name)
                    setDraft(set)
                  }}
                  className="mt-0.5"
                />
                <div className="flex min-w-0 flex-1 flex-col">
                  <span className="text-body font-medium text-text">
                    {name}
                  </span>
                  <span className="text-caption text-text-2">
                    {t(`modules.lines.${m.name}`, { defaultValue: '' })}
                  </span>
                  {m.holds_data &&
                  !fixed &&
                  ((m.selected && !on) || !m.running) ? (
                    // What stops acting on the data this module keeps (ARCH holds_data):
                    // when it is being turned off, or it does not run now. A module kept
                    // on by another runs, so nothing stops.
                    <span
                      className="text-caption text-warning"
                      data-slot="module-holds-data"
                    >
                      {t(`modules.whenOff.${m.name}`, {
                        defaultValue: t('modules.whenOffDefault'),
                      })}
                    </span>
                  ) : null}
                  {state === 'addOn' ? (
                    <span className="text-caption text-text-3">
                      {t('modules.activatedBy', {
                        addons: (m.activated_by ?? [])
                          .map(addOnName)
                          .join(', '),
                      })}
                    </span>
                  ) : null}
                  {state === 'keptOn' ? (
                    <span className="text-caption text-text-3">
                      {t('modules.keptOnBy', {
                        names: (m.required_by ?? [])
                          .map((r) =>
                            t(`modules.names.${r}`, {
                              defaultValue: readable(r),
                            }),
                          )
                          .join(', '),
                      })}
                    </span>
                  ) : null}
                </div>
                <Badge variant={TONE[state]} className="shrink-0">
                  {t(`modules.state.${state}`)}
                </Badge>
              </li>
            )
          })}
        </ul>
      )}
      {changes > 0 && !restarting ? (
        <p className="text-caption text-text-2">
          {t('modules.pending', { count: changes })}
        </p>
      ) : null}
    </section>
  )
}

/**
 * The enable action a dormant module's page offers an administrator (EU): add this module
 * to the selection with the same PUT, wait for the restart, then read server-info so the
 * page and the navigation come back.
 */
export function TurnOnModule({ module }: { module: string }) {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const [restarting, setRestarting] = useState(false)
  useEffect(() => {
    if (!restarting) return
    const ctl = new AbortController()
    void waitForEngineRestart({ signal: ctl.signal }).then(() => {
      if (ctl.signal.aborted) return
      setRestarting(false)
      void queryClient.invalidateQueries({ queryKey: modulesKey })
      void queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
    })
    return () => ctl.abort()
  }, [restarting, queryClient])
  const turnOn = usePrivilegedMutation<void, ModuleSelection>({
    mutationFn: async () => {
      const current = await modulesApi.get()
      const selected = current.modules
        .filter((m) => m.selected && !m.always_on)
        .map((m) => m.name)
      return modulesApi.select([...new Set([...selected, module])])
    },
    successMessage: t('modules.applied'),
    stepUpAction: 'modules',
    onDone: (status) => {
      if (status.restarting) setRestarting(true)
      else
        void queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
    },
  })
  const name = t(`modules.names.${module}`, { defaultValue: readable(module) })
  if (restarting)
    return (
      <p
        role="status"
        className="flex items-center gap-2 text-caption text-text-2"
      >
        <Spinner size="sm" aria-hidden />
        {t('modules.restarting')}
      </p>
    )
  return (
    <Button
      variant="primary"
      size="sm"
      disabled={turnOn.isPending}
      onClick={() => turnOn.mutate()}
    >
      {turnOn.isPending ? <Spinner size="sm" aria-hidden /> : null}
      {t('modules.turnOn', { name })}
    </Button>
  )
}
