// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Settings > Modules: every optional module can be turned on and off from the product
// (rule; ARCH C1). The engine keeps the selection in the deployment settings and
// applies it by restarting itself, so this screen chooses, applies, waits for the engine
// to come back and reads the result. One dense list, no tabs or cards.
import { useId, useMemo, useRef, useState } from 'react'
import {
  useQuery,
  useQueryClient,
  type QueryClient,
} from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
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
  /** Sessions the engine runs now; the restart that applies a change stops them. */
  running_sessions?: number
}

export const modulesApi = {
  get: () => http.get<ModuleSelection>('/v1/console/modules'),
  select: (selected: string[]) =>
    http.put<ModuleSelection>('/v1/console/modules', { selected }),
}

const modulesKey = ['settings', 'modules'] as const

const restartWaits = new WeakMap<QueryClient, Promise<boolean>>()
/**
 * Waits for the engine after a change that restarted it, then makes the navigation and the
 * module list read again. It is not tied to the page that asked: leaving the page during
 * the restart (which can take minutes) must not leave the console reading the old list, so
 * the wait runs to its end and the re-read happens whether or not anyone is still looking.
 * One wait per client; it resolves false when the wait gives up, and the re-read is made
 * anyway.
 */
function waitForRestart(queryClient: QueryClient): Promise<boolean> {
  const running = restartWaits.get(queryClient)
  if (running) return running
  const wait = waitForEngineRestart()
    .then((ready) => {
      void queryClient.invalidateQueries({ queryKey: modulesKey })
      void queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
      return ready
    })
    .finally(() => restartWaits.delete(queryClient))
  restartWaits.set(queryClient, wait)
  return wait
}

type RestartPhase = 'idle' | 'waiting' | 'stuck'

/** The page's view of that wait: waiting, back (`onBack`), or given up ("stuck"). */
function useRestartWait(onBack?: () => void) {
  const queryClient = useQueryClient()
  const [phase, setPhase] = useState<RestartPhase>('idle')
  const start = () => {
    setPhase('waiting')
    void waitForRestart(queryClient).then((ready) => {
      setPhase(ready ? 'idle' : 'stuck')
      if (ready) onBack?.()
    })
  }
  return { phase, start }
}

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

/**
 * The restart that applies a module change stops every running session (#507). Apply
 * reads how many run now: with none it applies at once; otherwise it asks first and says
 * how many stop. A count it cannot read, or one the reply leaves out, still warns, without
 * a number. Only the engine knows whether a change restarts it, so Apply always asks.
 */
function useRestartWarning(apply: () => void) {
  const [warning, setWarning] = useState<{ count?: number } | null>(null)
  const [reading, setReading] = useState(false)
  // The dialog opens from code, not from a trigger, so focus goes back to the button
  // that asked; a second click while the count is read reads nothing more.
  const opener = useRef<HTMLElement | null>(null)
  const request = (event: React.MouseEvent<HTMLElement>) => {
    opener.current = event.currentTarget
    if (reading) return
    setReading(true)
    modulesApi
      .get()
      .then((now) => {
        const count = now.running_sessions
        if (count === 0) apply()
        else setWarning({ count })
      })
      .catch(() => setWarning({}))
      .finally(() => setReading(false))
  }
  const dialog = (
    <RestartWarning
      warning={warning}
      close={() => setWarning(null)}
      apply={apply}
      restoreFocus={(event) => {
        event.preventDefault()
        opener.current?.focus()
      }}
    />
  )
  return { request, reading, dialog }
}

function RestartWarning({
  warning,
  close,
  apply,
  restoreFocus,
}: {
  warning: { count?: number } | null
  close: () => void
  apply: () => void
  restoreFocus: (event: Event) => void
}) {
  const { t } = useTranslation('settings')
  return (
    <ConfirmDialog
      open={warning != null}
      onOpenChange={(open) => !open && close()}
      title={t('modules.restartWarning.title')}
      description={
        warning?.count == null
          ? t('modules.restartWarning.unknown')
          : t('modules.restartWarning.stop', { count: warning.count })
      }
      confirmLabel={t('modules.restartWarning.confirm')}
      onCloseAutoFocus={restoreFocus}
      onConfirm={() => {
        close()
        apply()
      }}
    />
  )
}

/** Mounted for a system administrator only (Settings > Edition & modules); the engine
 * checks system:admin and the step-up itself. */
export function ModulesSettings() {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: modulesKey,
    queryFn: () => modulesApi.get(),
    retry: false,
  })
  const [draft, setDraft] = useState<Set<string> | null>(null)
  const restart = useRestartWait(() => setDraft(null))
  const restarting = restart.phase === 'waiting'
  const applyReasonId = useId()
  // An add-on is named by its title from the activation catalog (Business), read only when a
  // module says an add-on runs it; a key the catalog does not name is shown as it is.
  const addOnRun = (query.data?.modules ?? []).some(byAddOn)
  const activation = useQuery({
    queryKey: consoleKeys.activation(),
    queryFn: () => consoleApi.getActivation(),
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
      if (status.restarting) restart.start()
      else
        void queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
    },
  })

  const warning = useRestartWarning(() => apply.mutate())

  const applyReason = restarting
    ? t('modules.restarting')
    : restart.phase === 'stuck'
      ? t('modules.stillRestarting')
      : apply.isPending
        ? t('modules.saving')
        : query.isLoading
          ? t('common:states.loading')
          : query.isError
            ? t(
                isOpenCoreSeam(query.error)
                  ? 'modules.unavailable'
                  : 'modules.loadFailed',
              )
            : changes === 0
              ? t('modules.noChanges')
              : undefined

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
          disabled={!!applyReason}
          aria-describedby={applyReason ? applyReasonId : undefined}
          onClick={warning.request}
        >
          {apply.isPending || warning.reading ? (
            <Spinner size="sm" aria-hidden />
          ) : null}
          {changes === 0
            ? t('modules.applyNone')
            : t('modules.apply', { count: changes })}
        </Button>
      </div>
      {applyReason ? (
        <p
          id={applyReasonId}
          role="status"
          className="flex items-center gap-2 text-caption text-text-2"
          data-slot={restarting ? 'engine-restarting' : undefined}
        >
          {restarting ? <Spinner size="sm" aria-hidden /> : null}
          {applyReason}
        </p>
      ) : null}
      {query.isLoading ? (
        <div className="flex justify-center py-6">
          <Spinner />
        </div>
      ) : query.isError ? null : (
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
      {warning.dialog}
    </section>
  )
}

/**
 * The enable action a dormant module's page offers an administrator (EU): add this module
 * to the selection with the same PUT, wait for the restart, then read server-info so the
 * page and the navigation come back.
 */
export function TurnOnModule({
  module,
  plain = false,
}: {
  module: string
  /** On the module's own page the view is already named: the button says "Turn on" at the
   * default size. In a panel's one-line notice it names the module, small. */
  plain?: boolean
}) {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const restart = useRestartWait()
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
      if (status.restarting) restart.start()
      else
        void queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
    },
  })
  const warning = useRestartWarning(() => turnOn.mutate())
  const name = t(`modules.names.${module}`, { defaultValue: readable(module) })
  if (restart.phase === 'waiting')
    return (
      <p
        role="status"
        className="flex items-center gap-2 text-caption text-text-2"
      >
        <Spinner size="sm" aria-hidden />
        {t('modules.restarting')}
      </p>
    )
  if (restart.phase === 'stuck')
    return (
      <p role="status" className="text-caption text-text-2">
        {t('modules.stillRestarting')}
      </p>
    )
  return (
    <>
      <Button
        variant="primary"
        size={plain ? 'base' : 'sm'}
        disabled={turnOn.isPending}
        onClick={warning.request}
      >
        {turnOn.isPending || warning.reading ? (
          <Spinner size="sm" aria-hidden />
        ) : null}
        {plain ? t('modules.turnOnPlain') : t('modules.turnOn', { name })}
      </Button>
      {warning.dialog}
    </>
  )
}

/**
 * The one action of a view's page when its module is off. It turns the module on exactly as
 * Settings does, unless that takes more than this one module: the engine runs a module with
 * the modules it needs, so the page then says which ones come with it and links to
 * Settings instead of acting. A read that fails leaves the choice to the engine, which
 * answers the turn-on itself.
 */
export function TurnOnPage({ module }: { module: string }) {
  const { t } = useTranslation('settings')
  const query = useQuery({
    queryKey: modulesKey,
    queryFn: () => modulesApi.get(),
    retry: false,
  })
  if (query.isLoading) return <Spinner size="sm" />
  const modules = query.data?.modules ?? []
  const also = (modules.find((m) => m.name === module)?.requires ?? []).filter(
    (r) => !modules.find((m) => m.name === r)?.running,
  )
  if (also.length === 0) return <TurnOnModule module={module} plain />
  const names = also
    .map((r) => t(`modules.names.${r}`, { defaultValue: readable(r) }))
    .join(', ')
  return (
    <p className="text-body text-text-2">
      {t('modules.alsoTurnsOn', { names })}{' '}
      <Link
        to="/settings"
        search={{ section: 'edition' } as never}
        className="underline underline-offset-[3px] hover:text-text"
      >
        {t('modules.openEdition')}
      </Link>
    </p>
  )
}
