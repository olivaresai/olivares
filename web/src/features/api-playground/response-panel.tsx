// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Clock, FileText, Hash } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { CodeEditor } from '@/components/ui/code-editor'
import { Spinner } from '@/components/ui/spinner'
import { cn } from '@/lib/utils'
import type { ResponseState } from './use-playground'

type Translator = (key: string, options?: Record<string, unknown>) => string

interface ResponsePanelProps {
  response: ResponseState | null
  isLoading: boolean
  isStreaming: boolean
}

function formatBytes(bytes: number, t: Translator): string {
  if (bytes < 1024) return t('responsePanel.size.bytes', { value: bytes })
  if (bytes < 1024 * 1024)
    return t('responsePanel.size.kilobytes', {
      value: (bytes / 1024).toFixed(1),
    })
  return t('responsePanel.size.megabytes', {
    value: (bytes / (1024 * 1024)).toFixed(1),
  })
}

function formatBody(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

function StatusBadge({ status }: { status: number }) {
  const { t } = useTranslation('apiPlayground')
  return (
    <Badge
      variant="outline"
      className={cn(
        'font-mono tabular-nums',
        status >= 200 &&
          status < 300 &&
          'border-emerald-500/50 text-emerald-600 dark:text-emerald-400',
        status >= 300 &&
          status < 400 &&
          'border-blue-500/50 text-blue-600 dark:text-blue-400',
        status >= 400 &&
          status < 500 &&
          'border-amber-500/50 text-amber-600 dark:text-amber-400',
        status >= 500 && 'border-red-500/50 text-red-600 dark:text-red-400',
        status === 0 && 'border-red-500/50 text-red-600 dark:text-red-400',
      )}
    >
      {status === 0 ? t('responsePanel.statusError') : status}
    </Badge>
  )
}

export function ResponsePanel({
  response,
  isLoading,
  isStreaming,
}: ResponsePanelProps) {
  const { t } = useTranslation('apiPlayground')
  const formattedBody = useMemo(
    () => (response ? formatBody(response.body) : ''),
    [response],
  )

  if (isLoading && !response) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner />
      </div>
    )
  }

  if (!response) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 text-muted-foreground">
        <FileText className="h-8 w-8 opacity-40" />
        <p className="text-sm">{t('responsePanel.empty')}</p>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col">
      {/* Status bar */}
      <div className="flex items-center gap-3 border-b px-3 py-2">
        <StatusBadge status={response.status} />
        <span className="text-xs text-muted-foreground">
          {response.statusText}
        </span>
        {isStreaming && (
          <Badge variant="info" role="status" aria-live="polite">
            <Spinner size="sm" aria-hidden />
            {t('streaming')}
          </Badge>
        )}
        <div className="ml-auto flex items-center gap-3 text-xs text-muted-foreground">
          <span className="inline-flex items-center gap-1">
            <Clock className="h-3 w-3" />
            {t('responsePanel.duration', { duration: response.durationMs })}
          </span>
          <span className="inline-flex items-center gap-1">
            <Hash className="h-3 w-3" />
            {formatBytes(response.size, t)}
          </span>
        </div>
      </div>

      <Tabs defaultValue="body" className="flex-1 overflow-hidden">
        <TabsList className="mx-3 mt-2">
          <TabsTrigger value="body">{t('body')}</TabsTrigger>
          <TabsTrigger value="headers">
            {t('responsePanel.headersCount', {
              count: Object.keys(response.headers).length,
            })}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="body" className="flex-1 overflow-hidden px-3 pb-3">
          <div className="h-full rounded border">
            <CodeEditor
              value={formattedBody}
              language="json"
              readOnly
              ariaLabel={t('responsePanel.bodyAria')}
            />
          </div>
        </TabsContent>

        <TabsContent value="headers" className="mx-3 space-y-1 overflow-y-auto">
          {Object.entries(response.headers).map(([key, value]) => (
            <div key={key} className="flex gap-2 text-xs">
              <code className="w-48 shrink-0 truncate font-semibold text-foreground">
                {key}
              </code>
              <code className="min-w-0 flex-1 truncate text-muted-foreground">
                {value}
              </code>
            </div>
          ))}
        </TabsContent>
      </Tabs>
    </div>
  )
}
