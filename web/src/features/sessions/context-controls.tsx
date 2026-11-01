// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import { ContextRing } from './context-ring'
import './i18n'

export function ContextControls({
  run,
  busy,
  beforeSend,
  onBeforeSend,
  onCompact,
}: {
  run: RunDTO
  busy: boolean
  beforeSend: boolean
  onBeforeSend: (value: boolean) => void
  onCompact: () => void
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const [open, setOpen] = useState(false)
  const writable = can('sessions:run:write') && run.provider_driver === 'claude'
  const { data, isError } = useQuery({
    queryKey: agentOpsKeys.runCommands(activeTenant, run.run_ref),
    queryFn: ({ signal }) => agentOpsApi.commands(run.run_ref, { signal }),
    enabled: open && writable && run.state === 'idle',
    staleTime: 0,
    retry: false,
  })
  const supported =
    writable &&
    data?.reported &&
    !data.stale &&
    !isError &&
    data.commands.some(
      (c) =>
        c.name === 'compact' && c.kind === 'command' && c.source === 'built-in',
    )
  const disabled = !supported || busy || run.state !== 'idle'
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="sm" aria-label={t('compact.context')}>
          <ContextRing run={run} />
          <span>{t('compact.context')}</span>
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="end"
        aria-label={t('compact.context')}
        className="w-72 space-y-3"
      >
        <p className="text-caption text-muted-foreground">
          {t('compact.hint')}
        </p>
        <Button
          variant="secondary"
          size="sm"
          disabled={disabled}
          onClick={onCompact}
        >
          {busy ? t('compact.working') : t('compact.now')}
        </Button>
        <label className="flex items-center gap-2 text-caption">
          <input
            type="checkbox"
            checked={beforeSend}
            disabled={busy || (!supported && !beforeSend)}
            onChange={(e) => onBeforeSend(e.target.checked)}
          />
          {t('compact.beforeSend')}
        </label>
        {disabled && !busy && (
          <p className="text-caption text-muted-foreground">
            {t('compact.unavailable')}
          </p>
        )}
        {busy && (
          <p role="status" className="text-caption">
            {t('compact.working')}
          </p>
        )}
      </PopoverContent>
    </Popover>
  )
}
