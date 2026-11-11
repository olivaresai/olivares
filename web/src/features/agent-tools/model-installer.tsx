// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// INSTALL A LOCAL MODEL. One control for every screen that offers it (the Models page and
// the AI tools panel): a short list of suggestions or a typed name, Ollama's own progress,
// Cancel, and Ollama's own last line when it fails. When Ollama is not running the same
// control starts it and says so. InstalledModels lists what the server holds, with its
// size, and removes a model after a confirmation.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ApiError } from '@/lib/api/errors'
import { formatBytes } from '@/lib/format'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { agentToolsApi, type OllamaPull, type OllamaStatus } from './api'
import type { Ollama } from './use-ollama'
import './i18n'

/** A small model measured working with OpenCode and Codex on this Ollama (real use, 26.10.1):
 * the one a first download is offered. */
export const RECOMMENDED_MODEL = 'qwen2.5:0.5b'
/** Small models of Ollama's own library, offered as one-click names. */
const SUGGESTED_MODELS = [
  RECOMMENDED_MODEL,
  'llama3.2:1b',
  'gemma3:1b',
  'qwen2.5-coder:1.5b',
] as const
const ACTIVE_PULL_KEY = ['agent-tools', 'ollama', 'active-pull'] as const

function message(err: unknown): string {
  return err instanceof ApiError || err instanceof Error
    ? err.message
    : String(err)
}

export function ModelInstaller({ ollama }: { ollama: Ollama }) {
  const { t } = useTranslation('agentTools')
  const qc = useQueryClient()
  const { status, start, refresh } = ollama
  // acceptance: the field starts on a small model a first session can use, not empty.
  const [name, setName] = useState(RECOMMENDED_MODEL)
  const activePull = useQuery<string | null>({
    queryKey: ACTIVE_PULL_KEY,
    queryFn: () => null,
    enabled: false,
    gcTime: Infinity,
  })
  const pullID = activePull.data ?? null
  const pull = usePrivilegedMutation<string, OllamaPull>({
    mutationFn: (model, authority) =>
      agentToolsApi.ollamaPull(model, authority),
    stepUpAction: 'console',
    successMessage: t('ollama.downloadRequested'),
    onDone: (p) => qc.setQueryData(ACTIVE_PULL_KEY, p.id),
  })
  const progressKey = ['agent-tools', 'ollama', 'pull', pullID] as const
  const progress = useQuery({
    queryKey: progressKey,
    queryFn: async ({ signal }) => {
      const p = await agentToolsApi.ollamaPullStatus(pullID!, signal)
      if (p.state !== 'running') void refresh()
      return p
    },
    enabled: !!pullID,
    refetchInterval: (query) =>
      query.state.data?.state === 'running' || !query.state.data ? 1000 : false,
  })
  const cancel = useMutation({
    mutationFn: (id: string) => agentToolsApi.ollamaPullCancel(id),
    onSuccess: (p) => {
      qc.setQueryData(progressKey, p)
      void refresh()
    },
  })

  const s = status.data
  if (!s) return null
  const p = progress.data
  const downloading =
    pull.isPending ||
    (!!pullID && (progress.isError || !p || p.state === 'running'))
  const percent =
    p && p.total > 0 ? Math.floor((p.completed / p.total) * 100) : null
  const installed = new Set(s.models)
  const onPull = (e: FormEvent) => {
    e.preventDefault()
    const model = name.trim()
    if (model && !downloading) pull.mutate(model)
  }

  return (
    <div className="flex flex-col gap-3">
      <div
        role="group"
        aria-label={t('ollama.suggested')}
        className="flex flex-wrap gap-2"
      >
        {SUGGESTED_MODELS.filter((m) => !installed.has(m)).map((m) => (
          <Button
            key={m}
            type="button"
            variant="secondary"
            size="sm"
            className="font-mono"
            aria-pressed={name.trim() === m}
            onClick={() => setName(m)}
          >
            {m}
          </Button>
        ))}
      </div>
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
        {s.state === 'running' ? (
          <Button
            type="submit"
            variant="secondary"
            size="sm"
            disabled={!name.trim() || downloading}
          >
            {t('ollama.download')}
          </Button>
        ) : (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={() => start.mutate()}
            disabled={start.isPending || s.state === 'starting'}
          >
            {start.isPending || s.state === 'starting' ? (
              <Loader2 aria-hidden className="animate-spin" />
            ) : null}
            {t('ollama.startOllama')}
          </Button>
        )}
      </form>
      {s.state !== 'running' ? (
        <p className="text-caption text-muted-foreground">
          {t('ollama.notRunning')}
        </p>
      ) : null}
      {s.state !== 'running' && s.message ? (
        <p className="text-caption text-muted-foreground" role="status">
          {s.message}
        </p>
      ) : null}
      {start.error ? (
        <p className="text-caption text-danger" role="alert">
          {message(start.error)}
        </p>
      ) : null}
      {pull.error ? (
        <p className="text-caption text-danger" role="alert">
          {message(pull.error)}
        </p>
      ) : null}
      {cancel.error ? (
        <p className="text-caption text-danger" role="alert">
          {message(cancel.error)}
        </p>
      ) : null}
      {progress.isError ? (
        <div className="flex flex-wrap items-center gap-2" role="alert">
          <p className="text-caption text-danger">
            {t('ollama.progressFailed', { error: message(progress.error) })}
          </p>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={() => void progress.refetch()}
          >
            {t('profiles.retry')}
          </Button>
          {progress.error instanceof ApiError &&
          progress.error.status === 404 ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => qc.setQueryData(ACTIVE_PULL_KEY, null)}
            >
              {t('ollama.clearDownload')}
            </Button>
          ) : null}
        </div>
      ) : null}
      {p?.state === 'running' ? (
        <div className="flex flex-col gap-2">
          <div className="flex flex-wrap items-center gap-3">
            {percent === null ? (
              <Loader2
                aria-hidden
                className="size-4 shrink-0 animate-spin text-muted-foreground motion-reduce:animate-none"
              />
            ) : null}
            <div
              className="h-2 min-w-32 flex-1 overflow-hidden rounded-full bg-muted"
              role="progressbar"
              aria-label={t('ollama.progressAria', { model: p.model })}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={percent ?? undefined}
            >
              {percent === null ? null : (
                <div
                  className="h-full rounded-full bg-accent-text transition-[width] duration-300 ease-out"
                  style={{ width: `${percent}%` }}
                />
              )}
            </div>
            <Button
              type="button"
              variant="secondary"
              size="sm"
              aria-label={t('ollama.cancelAria', { model: p.model })}
              disabled={cancel.isPending}
              onClick={() => cancel.mutate(p.id)}
            >
              {t('ollama.cancel')}
            </Button>
          </div>
          <p className="text-caption tabular-nums text-muted-foreground">
            {percent !== null
              ? t('ollama.downloadingPercent', { model: p.model, percent })
              : t('ollama.downloading', { model: p.model })}
          </p>
          {p.status ? (
            <p className="text-caption text-muted-foreground">{p.status}</p>
          ) : null}
        </div>
      ) : p ? (
        <p
          className={
            p.state === 'failed' || p.error
              ? 'text-caption text-danger'
              : 'text-caption text-muted-foreground'
          }
          role={p.state === 'failed' || p.error ? 'alert' : 'status'}
        >
          {p.state === 'succeeded'
            ? p.error
              ? t('ollama.downloadSyncWarning', {
                  model: p.model,
                  error: p.error,
                })
              : t('ollama.downloaded', { model: p.model })
            : t('ollama.downloadFailed', {
                model: p.model,
                error: p.error ?? '',
              })}
        </p>
      ) : null}
    </div>
  )
}

/** What the server holds, one row per model with its size and a Remove. */
export function InstalledModels({ ollama }: { ollama: Ollama }) {
  const { t } = useTranslation('agentTools')
  const { status, refresh } = ollama
  const [removing, setRemoving] = useState<string | null>(null)
  const remove = usePrivilegedMutation<string, OllamaStatus>({
    mutationFn: (model, authority) =>
      agentToolsApi.ollamaRemove(model, authority),
    stepUpAction: 'console',
    successMessage: (_, model) => t('ollama.removed', { model }),
    onDone: () => void refresh(),
  })
  const s = status.data
  if (!s || s.state !== 'running') return null
  if (s.models_error)
    return (
      <p className="text-caption text-danger" role="alert">
        {s.models_error}
      </p>
    )
  if (s.models.length === 0)
    return (
      <p className="text-caption text-muted-foreground">
        {t('ollama.noModels')}
      </p>
    )
  return (
    <div className="flex flex-col gap-2">
      <h3
        id="ollama-installed-models"
        className="text-caption font-medium text-foreground"
      >
        {t('ollama.installedTitle')}
      </h3>
      <ul
        aria-labelledby="ollama-installed-models"
        className="flex flex-col divide-y divide-border rounded-md border border-border"
      >
        {s.models.map((m) => (
          <li key={m} className="flex flex-wrap items-center gap-3 px-3 py-2">
            <span className="min-w-0 flex-1 break-all font-mono text-caption text-foreground">
              {m}
            </span>
            {s.model_sizes?.[m] !== undefined ? (
              <span className="text-caption tabular-nums text-muted-foreground">
                {formatBytes(s.model_sizes[m])}
              </span>
            ) : null}
            <Button
              type="button"
              variant="ghost"
              size="sm"
              aria-label={t('ollama.removeAria', { model: m })}
              onClick={() => setRemoving(m)}
            >
              {t('ollama.remove')}
            </Button>
          </li>
        ))}
      </ul>
      <ConfirmDialog
        open={removing !== null}
        onOpenChange={(open) => {
          if (!open) setRemoving(null)
        }}
        title={t('ollama.removeTitle', { model: removing ?? '' })}
        description={t('ollama.removeBody')}
        confirmLabel={t('ollama.removeConfirm')}
        tone="danger"
        pending={remove.isPending}
        onConfirm={() => {
          if (removing) remove.mutate(removing)
          setRemoving(null)
        }}
      />
    </div>
  )
}
