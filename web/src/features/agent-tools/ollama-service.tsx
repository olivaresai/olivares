// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// OLLAMA ON THIS SERVER (HU-R17). The installed Ollama runs as the engine's own
// service: Start and Stop it here, see where it answers (its endpoint is added to
// Providers), and download a model by name with Ollama's own progress.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { firstHourKeys } from '@/features/first-hour/api'
import { providerKeys } from '@/features/providers/api'
import { ApiError } from '@/lib/api/errors'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { useTenantStore } from '@/stores/tenant'
import { agentToolsApi, type OllamaPull, type OllamaStatus } from './api'
import './i18n'

const STATUS_KEY = ['agent-tools', 'ollama'] as const

function message(err: unknown): string {
  return err instanceof ApiError || err instanceof Error
    ? err.message
    : String(err)
}

/** A small model measured working with OpenCode and Codex on this Ollama (real use, 26.10.1):
 * the one a first download is offered. */
const RECOMMENDED_MODEL = 'qwen2.5:0.5b'

export function OllamaService() {
  const { t } = useTranslation('agentTools')
  const qc = useQueryClient()
  // The organization the person works in gets the endpoint in its Providers, and only it.
  const tenant = useTenantStore((s) => s.activeTenant)
  const status = useQuery({
    queryKey: STATUS_KEY,
    queryFn: ({ signal }) => agentToolsApi.ollama(signal),
    refetchInterval: (query) =>
      query.state.data?.state === 'starting' ? 1000 : false,
  })
  const readinessChanged = useCallback(
    () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: firstHourKeys.all(tenant) }),
        qc.invalidateQueries({ queryKey: providerKeys.all(tenant) }),
      ]),
    [qc, tenant],
  )
  // Exact: the download progress lives under this key, and a finished download
  // refreshes the status; without `exact` it would refetch itself without end.
  const refresh = () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: STATUS_KEY, exact: true }),
      readinessChanged(),
    ])
  // The engine registers this server in Providers once it is up, after the start request
  // returned: reaching running is when the readiness reads must ask again.
  const serverState = status.data?.state
  const lastState = useRef(serverState)
  useEffect(() => {
    if (serverState === 'running' && lastState.current !== serverState)
      void readinessChanged()
    lastState.current = serverState
  }, [serverState, readinessChanged])
  const start = usePrivilegedMutation<void, OllamaStatus>({
    mutationFn: (_, authority) => agentToolsApi.ollamaStart(tenant, authority),
    stepUpAction: 'console',
    successMessage: t('ollama.startRequested'),
    onDone: () => void refresh(),
  })
  const stop = useMutation({
    mutationFn: () => agentToolsApi.ollamaStop(),
    onSettled: () => void refresh(),
  })
  // HU2-07: the field starts on a small model a first session can use, not empty.
  const [name, setName] = useState(RECOMMENDED_MODEL)
  const [pullID, setPullID] = useState<string | null>(null)
  const pull = usePrivilegedMutation<string, OllamaPull>({
    mutationFn: (model, authority) =>
      agentToolsApi.ollamaPull(model, authority),
    stepUpAction: 'console',
    successMessage: t('ollama.downloadRequested'),
    onDone: (p) => setPullID(p.id),
  })
  const progress = useQuery({
    queryKey: ['agent-tools', 'ollama', 'pull', pullID],
    queryFn: async ({ signal }) => {
      const p = await agentToolsApi.ollamaPullStatus(pullID!, signal)
      if (p.state !== 'running') void refresh()
      return p
    },
    enabled: !!pullID,
    refetchInterval: (query) =>
      query.state.data?.state === 'running' || !query.state.data ? 1000 : false,
  })

  const s = status.data
  if (!s || !s.installed) return null
  const state = s.state
  const onPull = (e: FormEvent) => {
    e.preventDefault()
    const model = name.trim()
    if (model && !pull.isPending) pull.mutate(model)
  }
  const p = progress.data
  const percent =
    p && p.total > 0 ? Math.floor((p.completed / p.total) * 100) : null

  return (
    <div
      className="flex flex-col gap-3 rounded-md border border-border p-3"
      data-testid="ollama-service"
    >
      <div className="flex flex-wrap items-center gap-3">
        <span className="text-body text-foreground">
          {state === 'running'
            ? t('ollama.running', { endpoint: s.endpoint })
            : state === 'starting'
              ? t('ollama.starting')
              : state === 'failed'
                ? t('ollama.failed')
                : t('ollama.stopped')}
        </span>
        {state === 'running' || state === 'starting' ? (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => stop.mutate()}
            disabled={stop.isPending}
          >
            {t('ollama.stop')}
          </Button>
        ) : (
          <Button
            variant="primary"
            size="sm"
            onClick={() => start.mutate()}
            disabled={start.isPending}
          >
            {start.isPending ? (
              <Loader2 aria-hidden className="animate-spin" />
            ) : null}
            {t('ollama.start')}
          </Button>
        )}
      </div>
      {s.message ? (
        <p className="text-caption text-muted-foreground" role="status">
          {s.message}
        </p>
      ) : null}
      {start.error ? (
        <p className="text-caption text-danger" role="alert">
          {message(start.error)}
        </p>
      ) : null}
      {state === 'running' ? (
        <>
          <p className="text-caption text-muted-foreground">
            {t('ollama.hint')}
          </p>
          {s.models.length === 0 ? (
            <p className="text-caption text-muted-foreground">
              {t('ollama.noModels')}
            </p>
          ) : (
            <ul className="flex flex-wrap gap-2">
              {s.models.map((m) => (
                <li key={m} className="font-mono text-caption text-foreground">
                  {m}
                </li>
              ))}
            </ul>
          )}
          <form onSubmit={onPull} className="flex flex-wrap items-end gap-2">
            <Field
              label={t('ollama.model')}
              htmlFor="ollama-model"
              className="min-w-0 flex-1 basis-64"
              description={
                <>
                  {t('ollama.modelHint')}{' '}
                  <code className="break-all">
                    hf.co/bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M
                  </code>
                </>
              }
            >
              <Input
                id="ollama-model"
                value={name}
                onChange={(e) => setName(e.target.value)}
                spellCheck={false}
                mono
              />
            </Field>
            <Button
              type="submit"
              variant="secondary"
              size="sm"
              disabled={
                !name.trim() || pull.isPending || p?.state === 'running'
              }
            >
              {t('ollama.download')}
            </Button>
          </form>
          {pull.error ? (
            <p className="text-caption text-danger" role="alert">
              {message(pull.error)}
            </p>
          ) : null}
          {p ? (
            <p
              className={
                p.state === 'failed'
                  ? 'text-caption text-danger'
                  : 'text-caption text-muted-foreground'
              }
              role="status"
            >
              {p.state === 'succeeded'
                ? t('ollama.downloaded', { model: p.model })
                : p.state === 'failed'
                  ? t('ollama.downloadFailed', {
                      model: p.model,
                      error: p.error ?? '',
                    })
                  : percent !== null
                    ? t('ollama.downloadingPercent', {
                        model: p.model,
                        percent,
                      })
                    : t('ollama.downloading', { model: p.model })}
            </p>
          ) : null}
        </>
      ) : null}
    </div>
  )
}
