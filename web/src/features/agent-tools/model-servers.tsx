// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// LLAMA.CPP AND VLLM ON THIS SERVER. Like Ollama, the engine runs them as its own services:
// each row says whether the program is installed, starts it on a model the person names (the
// endpoint is added to the signed-in organization's Providers once it answers) and stops it.
// The state, endpoint and models are the engine's (GET /v1/m/agenttools/model-servers).
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { MonoMark } from '@/components/ui/mono-mark'
import { firstHourKeys } from '@/features/first-hour/api'
import { providerKeys } from '@/features/providers/api'
import { ApiError } from '@/lib/api/errors'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { useTenantStore } from '@/stores/tenant'
import { agentToolsApi, type ModelServerStatus } from './api'
import './i18n'

const STATUS_KEY = ['agent-tools', 'model-servers'] as const

/** The program the engine looks for on its PATH, by server kind (modelServerSpecs). */
const PROGRAMS: Record<string, string> = {
  'llama-cpp': 'llama-server',
  vllm: 'vllm',
}

function message(err: unknown): string {
  return err instanceof ApiError || err instanceof Error
    ? err.message
    : String(err)
}

export function ModelServers() {
  const qc = useQueryClient()
  const tenant = useTenantStore((s) => s.activeTenant)
  const status = useQuery({
    queryKey: STATUS_KEY,
    queryFn: ({ signal }) => agentToolsApi.modelServers(signal),
    refetchInterval: (query) =>
      query.state.data?.servers.some((s) => s.state === 'starting')
        ? 1000
        : false,
  })
  const readinessChanged = useCallback(
    () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: firstHourKeys.all(tenant) }),
        qc.invalidateQueries({ queryKey: providerKeys.all(tenant) }),
      ]),
    [qc, tenant],
  )
  const refresh = useCallback(
    () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: STATUS_KEY, exact: true }),
        readinessChanged(),
      ]),
    [qc, readinessChanged],
  )
  // The engine registers a server in Providers once it answers, after the start request
  // returned: a server reaching running is when the readiness reads must ask again.
  const running = (status.data?.servers ?? [])
    .filter((s) => s.state === 'running')
    .map((s) => s.kind)
    .join(',')
  const lastRunning = useRef(running)
  useEffect(() => {
    if (running && running !== lastRunning.current) void readinessChanged()
    lastRunning.current = running
  }, [running, readinessChanged])

  // A person who may not read the servers (the route is for system administrators) sees no row.
  if (!status.data) return null
  return (
    <>
      {status.data.servers.map((server) => (
        <ModelServerRow
          key={server.kind}
          server={server}
          tenant={tenant}
          onChanged={() => void refresh()}
        />
      ))}
    </>
  )
}

function ModelServerRow({
  server,
  tenant,
  onChanged,
}: {
  server: ModelServerStatus
  tenant: string | null
  onChanged: () => void
}) {
  const { t } = useTranslation('agentTools')
  const [model, setModel] = useState('')
  const start = usePrivilegedMutation<string, ModelServerStatus>({
    mutationFn: (ref, authority) =>
      agentToolsApi.modelServerStart(server.kind, ref, tenant!, authority),
    stepUpAction: 'console',
    successMessage: t('modelServers.startRequested', { name: server.name }),
    onDone: onChanged,
  })
  const stop = useMutation({
    mutationFn: () => agentToolsApi.modelServerStop(server.kind),
    onSettled: onChanged,
  })
  const state = server.state
  const busy = state === 'running' || state === 'starting'
  const ref = model.trim()
  const onStart = (e: FormEvent) => {
    e.preventDefault()
    if (ref && tenant && !start.isPending) start.mutate(ref)
  }
  const id = `model-server-${server.kind}`
  return (
    <section
      aria-labelledby={`${id}-name`}
      data-testid={id}
      className="flex flex-col gap-3 overflow-hidden rounded-card border border-line bg-surface p-4"
    >
      <div className="flex flex-wrap items-center gap-3">
        <MonoMark name={server.name} className="size-6" />
        <h2 id={`${id}-name`} className="text-body-l font-semibold text-text">
          {server.name}
        </h2>
        <span className="flex-1" />
        {busy ? (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => stop.mutate()}
            disabled={stop.isPending}
          >
            {t('modelServers.stop')}
          </Button>
        ) : null}
      </div>
      {!server.installed && !busy ? (
        <p className="text-caption text-muted-foreground">
          {t('modelServers.notInstalled', {
            name: server.name,
            program: PROGRAMS[server.kind] ?? server.kind,
          })}
        </p>
      ) : (
        <span className="text-body text-foreground">
          {state === 'running'
            ? t('modelServers.running', { endpoint: server.endpoint })
            : state === 'starting'
              ? t('modelServers.starting', { model: server.model ?? '' })
              : state === 'failed'
                ? t('modelServers.failed')
                : t('modelServers.stopped')}
        </span>
      )}
      {server.message ? (
        <p
          className="break-words text-caption text-muted-foreground"
          role="status"
        >
          {server.message}
        </p>
      ) : null}
      {start.error ? (
        <p className="text-caption text-danger" role="alert">
          {message(start.error)}
        </p>
      ) : null}
      {stop.error ? (
        <p className="text-caption text-danger" role="alert">
          {message(stop.error)}
        </p>
      ) : null}
      {state === 'running' ? (
        <>
          {server.message ? null : (
            <p className="text-caption text-muted-foreground">
              {t('modelServers.hint')}
            </p>
          )}
          {server.models.length > 0 ? (
            <ul className="flex flex-wrap gap-2">
              {server.models.map((m) => (
                <li key={m} className="font-mono text-caption text-foreground">
                  {m}
                </li>
              ))}
            </ul>
          ) : null}
        </>
      ) : null}
      {server.installed && !busy ? (
        <form onSubmit={onStart} className="flex flex-wrap items-end gap-2">
          <Field
            label={t('modelServers.model')}
            htmlFor={`${id}-model`}
            className="min-w-0 flex-1 basis-64"
            description={
              server.kind === 'vllm'
                ? t('modelServers.modelHintVllm')
                : t('modelServers.modelHintLlamaCpp')
            }
          >
            <Input
              id={`${id}-model`}
              value={model}
              onChange={(e) => setModel(e.target.value)}
              spellCheck={false}
              mono
            />
          </Field>
          <Button
            type="submit"
            variant="primary"
            size="sm"
            disabled={!ref || !tenant || start.isPending}
          >
            {start.isPending ? (
              <Loader2 aria-hidden className="animate-spin" />
            ) : null}
            {t('modelServers.start')}
          </Button>
        </form>
      ) : null}
    </section>
  )
}
