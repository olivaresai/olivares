// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HOW TO START, ON NOW, FROM WHAT THE TOOLS THEMSELVES SAY: installed and signed in. It used
// to read provider profiles and said "No provider profile is registered" after Claude Code
// was installed and signed in (HU-19). A ready tool offers New session, the one dialog
// every entry point opens; otherwise the next setup step, in the setup wizard.
import { useNavigate } from '@tanstack/react-router'
import { Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Kbd } from '@/components/ui/kbd'
import { useNewSession } from '@/components/layout/new-session'
import {
  TOOL_NAMES,
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
  // with the tool's own login, or with an API key saved in Providers (HU 029).
  const { ready, isLoading, checking } = useReadyTools()
  if (isLoading || checking) return undefined
  return ready[0] ?? null
}

export function NowStart() {
  const { t } = useTranslation('home')
  const { can, isSuperadmin } = useAuth()
  const navigate = useNavigate()
  const newSession = useNewSession()
  const claude = useToolStatus('claude')
  const codex = useToolStatus('codex')
  const readyTools = useReadyTools()
  if (!can('sessions:run:write')) return null
  if (
    claude.isLoading ||
    codex.isLoading ||
    readyTools.isLoading ||
    readyTools.checking
  )
    return null
  const tools: [ToolKey, typeof claude.data][] = [
    ['claude', claude.data],
    ['codex', codex.data],
  ]
  // Any tool the engine can start names the line, Grok Build and OpenCode included (HU 043).
  const ready = readyTools.ready[0]
  const installed = tools.find(([, s]) => s?.installed)
  // A status this principal cannot read is not "nothing installed": offer the dialog,
  // which says what is missing.
  // Only a system administrator reads the tools' own status (useToolStatus).
  const unknown = !isSuperadmin || (claude.isError && codex.isError)
  const setup = () => void navigate({ to: '/onboarding' as never } as never)

  const [text, action] = ready
    ? [t('start.ready', { tool: TOOL_NAMES[ready] }), null]
    : unknown
      ? [null, null]
      : installed
        ? [
            t('start.signIn', { tool: TOOL_NAMES[installed[0]] }),
            t('start.signInAction'),
          ]
        : [t('start.install'), t('start.installAction')]

  return (
    <div
      className="flex min-w-0 flex-wrap items-center gap-2"
      data-testid="now-start"
    >
      {text ? (
        <p className="min-w-0 flex-1 text-body text-text-2">{text}</p>
      ) : null}
      {action ? (
        <Button variant="primary" size="sm" onClick={setup}>
          {action}
        </Button>
      ) : (
        <Button variant="primary" size="sm" onClick={newSession}>
          <Plus className="size-3.5" />
          {t('start.newSession')}
          <Kbd className="ml-1">N</Kbd>
        </Button>
      )}
    </div>
  )
}
