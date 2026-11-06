// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// OLLAMA ON THIS SERVER (HU-R17). The installed Ollama runs as the engine's own
// service: Start and Stop it here, see where it answers (its endpoint is added to
// Providers), and download a model by name with Ollama's own progress.
import { Loader2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ApiError } from '@/lib/api/errors'
import { InstalledModels, ModelInstaller } from './model-installer'
import { useOllama } from './use-ollama'

import './i18n'

function message(err: unknown): string {
  return err instanceof ApiError || err instanceof Error
    ? err.message
    : String(err)
}

export function OllamaService() {
  const { t } = useTranslation('agentTools')
  const ollama = useOllama()
  const { status, start, stop } = ollama

  const s = status.data
  if (!s || !s.installed) return null
  const state = s.state

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
          <InstalledModels ollama={ollama} />
          <ModelInstaller ollama={ollama} />
        </>
      ) : null}
    </div>
  )
}
