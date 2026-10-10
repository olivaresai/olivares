// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The setup wizard: three steps from a fresh install to a running agent session —
// install an agent tool, sign it in with its own login, start a session in a folder
// (features/first-hour). The default workspace already exists and counts. Inviting
// people, an identity provider, policies, MCP servers and budgets are optional next
// suggestions that link to their own pages; none of them is pending or blocking, and
// one whose module is off on this installation is not offered.
// Dismiss hides the guide on this browser and is reversible.
import './i18n'
import { Link } from '@tanstack/react-router'
import { Rocket } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { PageHeader } from '@/components/ui/page-header'
import { FirstHourSteps } from '@/features/first-hour/first-hour'
import '@/features/first-hour/i18n'
import { useAuth } from '@/lib/auth/context'
import { COST_VIEW } from '@/features/registry'
import { useViewAccess } from '@/features/navigation/authorization'
import { useModuleEnabled } from '@/stores/modules'

const DISMISS_KEY = 'olivares.onboarding.dismissed'

// `view` names the registry view whose module must run for the suggestion to be offered:
// a page of a module that is off has nothing to show (stores/modules.ts), and a fresh
// install runs with most modules off.
const NEXT: readonly { key: string; to: string; view?: string }[] = [
  { key: 'invite', to: '/console?tab=people' },
  { key: 'identity', to: '/console?tab=sso' },
  // Managed settings, not the page's default Drift tab: drift reads the Security
  // module's findings, and a fresh install runs without Security.
  {
    key: 'policies',
    to: '/claude-policy?tab=managed-settings',
    view: 'claudePolicy',
  },
  { key: 'mcp', to: '/console?tab=mcpGateway' },
  // Stored budgets remain useful after an edition change; Community offers their read view.
  { key: 'budgets', to: COST_VIEW.path, view: COST_VIEW.id },
]

/** A command with the one button that copies it. */
function CopyCommand({ command }: { command: string }) {
  const { t } = useTranslation('firstHour')
  const [copied, setCopied] = useState(false)
  return (
    <div className="flex w-fit max-w-full items-center gap-2 rounded-[6px] bg-surface py-1 pl-2 pr-1">
      <code className="min-w-0 overflow-x-auto text-mono text-text select-all">
        {command}
      </code>
      <Button
        variant="ghost"
        size="sm"
        onClick={() => {
          void navigator.clipboard
            ?.writeText(command)
            .then(() => setCopied(true))
        }}
      >
        {copied ? t('next.copied') : t('next.copy')}
      </Button>
    </div>
  )
}

export function OnboardingView() {
  const { t } = useTranslation(['onboarding', 'firstHour'])
  const { can } = useAuth()
  const access = useViewAccess()
  const moduleEnabled = useModuleEnabled()
  const [dismissed, setDismissed] = useState<boolean>(() => {
    try {
      return localStorage.getItem(DISMISS_KEY) === 'true'
    } catch {
      return false
    }
  })
  const remember = (value: boolean) => {
    try {
      if (value) localStorage.setItem(DISMISS_KEY, 'true')
      else localStorage.removeItem(DISMISS_KEY)
    } catch {
      // localStorage unavailable: the choice still holds for this visit
    }
    setDismissed(value)
  }

  if (!can('system:admin')) return <ForbiddenState />
  if (dismissed) {
    return (
      <div className="flex flex-col gap-4 p-6">
        <EmptyState
          icon={<Rocket />}
          title={t('dismissed.title')}
          description={t('dismissed.description')}
          action={
            <Button variant="secondary" onClick={() => remember(false)}>
              {t('dismissed.resume')}
            </Button>
          }
        />
      </div>
    )
  }
  return (
    <div className="mx-auto flex w-full max-w-[920px] flex-col gap-5 p-6">
      <PageHeader
        icon={Rocket}
        title={t('title')}
        description={t('subtitle')}
        actions={
          <Button variant="ghost" size="sm" onClick={() => remember(true)}>
            {t('dismiss')}
          </Button>
        }
      />
      <FirstHourSteps />
      <section
        className="flex flex-col gap-2"
        aria-labelledby="first-hour-next"
      >
        <h2 id="first-hour-next" className="text-body font-semibold text-text">
          {t('firstHour:next.title')}
        </h2>
        <ul className="flex flex-wrap gap-x-5 gap-y-1.5">
          {NEXT.filter((n) =>
            n.key === 'budgets'
              ? access.navigable(COST_VIEW)
              : moduleEnabled(undefined, n.view),
          ).map((n) => (
            <li key={n.key}>
              <Link
                to={n.to as '/'}
                className="text-body text-accent-text underline underline-offset-2"
              >
                {n.key === 'budgets' && COST_VIEW.id === 'storedBudgets'
                  ? t('nav:items.storedBudgets')
                  : t(`firstHour:next.${n.key}`)}
              </Link>
            </li>
          ))}
        </ul>
        <p className="text-caption text-text-2">
          {t('firstHour:next.cliHint')}
        </p>
        <CopyCommand command="curl -fsSL https://olivares.ai/olivares/install.sh | sh" />
      </section>
    </div>
  )
}
