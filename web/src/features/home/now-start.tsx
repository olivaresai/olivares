// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HOW TO START, ON NOW, FROM WHAT THE TOOLS THEMSELVES SAY: installed and signed in. It used
// to read provider profiles and said "No provider profile is registered" after Claude Code
// was installed and signed in. A ready tool offers New session, the one dialog
// every entry point opens; otherwise the next setup step, in the setup wizard.
//
// It holds its place from the first paint (#3121): until the tools answer it offers New
// session, which the dialog then completes or explains, so the line never arrives late and
// pushes the page down. The verb comes first and the sentence after it.
import { useNavigate } from '@tanstack/react-router'
import { Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Kbd } from '@/components/ui/kbd'
import { useNewSession } from '@/components/layout/new-session'
import {
  toolName,
  type SessionTool,
  type ToolKey,
} from '@/features/first-hour/api'
import { useReadyTools, useToolStatus } from '@/features/first-hour/first-hour'
import { useAuth } from '@/lib/auth/context'
import './i18n'

/** The first coding tool that is installed and signed in: `null` when none is,
 * `undefined` while the status is still loading. */
export function useReadyTool(): SessionTool | null | undefined {
  // The first-hour rule, the one the New session dialog uses: installed and signed in
  // with the tool's own login, or with an API key saved in Providers.
  const { ready, isLoading } = useReadyTools()
  if (isLoading) return undefined
  return ready[0] ?? null
}

/** `quiet` while a request waiting for this person carries Estate's one orange control:
 * the start line then steps back to a secondary button. */
export function NowStart({ quiet = false }: { quiet?: boolean } = {}) {
  const { t } = useTranslation('home')
  const { can, isSuperadmin } = useAuth()
  const navigate = useNavigate()
  const newSession = useNewSession()
  const claude = useToolStatus('claude')
  const codex = useToolStatus('codex')
  const readyTools = useReadyTools()
  if (!can('sessions:run:write')) return null
  const loading = claude.isLoading || codex.isLoading || readyTools.isLoading
  const tools: [ToolKey, typeof claude.data][] = [
    ['claude', claude.data],
    ['codex', codex.data],
  ]
  // Any tool the engine can start names the line, Grok Build and OpenCode included.
  const ready = readyTools.ready[0]
  const installed = tools.find(([, s]) => s?.installed)
  // The key's refusal leads to Providers, so only a person who can open Providers gets it.
  const refusal =
    installed &&
    can('sessions:provider:read') &&
    readyTools.boundKey(installed[0])
      ? readyTools.refusal(installed[0])
      : undefined
  // A status this principal cannot read is not "nothing installed": offer the dialog,
  // which says what is missing.
  // Only a system administrator reads the tools' own status (useToolStatus).
  const unknown = !isSuperadmin || (claude.isError && codex.isError)
  const setup = () =>
    void navigate({ to: refusal ? '/providers' : '/onboarding' } as never)

  const [text, action] = loading
    ? [null, null]
    : ready
      ? [t('start.ready', { tool: toolName(ready) }), null]
      : unknown
        ? [null, null]
        : refusal
          ? [refusal, t('firstHour:start.openProviders')]
          : installed
            ? [
                t('start.signIn', { tool: toolName(installed[0]!) }),
                t('start.signInAction'),
              ]
            : [t('start.install'), t('start.installAction')]

  return (
    <div
      className="flex min-h-9 min-w-0 items-center gap-3"
      data-testid="now-start"
    >
      {action ? (
        <Button
          variant={quiet ? 'secondary' : 'primary'}
          size="sm"
          onClick={setup}
        >
          {action}
        </Button>
      ) : (
        <Button
          variant={quiet ? 'secondary' : 'primary'}
          size="sm"
          onClick={newSession}
        >
          <Plus className="size-3.5" />
          {t('start.newSession')}
          <Kbd className="ml-1">N</Kbd>
        </Button>
      )}
      {text ? <p className="min-w-0 text-body text-text-2">{text}</p> : null}
    </div>
  )
}
