// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION, AS A PERSON READS IT, at the head of Details (the regression design): its state,
// workspace, tool, account, model and access in words, then the account's share of the week
// and of five hours. A fact the engine did not send is left out, never replaced by an engine reference;
// the references stay in the pane's References block below.
import { useQuery } from '@tanstack/react-query'
import { useId } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight } from 'lucide-react'
import { KvList, KvRow } from '@/components/ui/kv'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import {
  AccountUsage as UsageLine,
  sharesOf,
} from '@/features/agentops/account-usage'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { useModelName } from '@/features/agentops/model-choices'
import { toolName } from '@/features/agentops/engine-tools'
import type { RunDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import { formatMicroUsd } from '@/lib/format'
import type { UnifiedSession } from './provenance'
import { useAccessWords } from './session-meter'
import { StateWord } from './session-state-word'
import './i18n'

export function SessionFacts({
  session,
  run,
  account,
  accountRef,
  workspace,
}: {
  session: UnifiedSession
  run?: RunDTO
  /** The account's name (the provider profile), when the person may read it. */
  account: string | null
  /** Omitted for a known unnamed profile; otherwise keeps the account-read path. */
  accountRef?: string
  /** The workspace's name, when the person may read it. */
  workspace: string | null
}) {
  const { t, i18n } = useTranslation(['sessions', 'agentops'])
  const modelName = useModelName(
    run?.provider_profile_ref ?? session.profileRef,
  )
  const titleId = useId()
  const live = session.live
  const driver = run?.provider_driver || live?.provider
  const model = run?.model_ref || run?.usage_model_ref
  const effort = run?.effort
    ? t(`agentops:effort.${run.effort}`, { defaultValue: run.effort })
    : ''
  const cost = live?.cost_micro_usd
  // ONE PLACE PER FACT: the header and the composer already say the tool, account, model
  // and access, and the status bar the account's use. Details shows what they do not
  // (state, workspace, cost); the rest waits one click away under "Session ›".
  return (
    <section
      aria-label={t('context.facts.title')}
      data-testid="session-facts"
      className="flex flex-col gap-2"
    >
      <KvList className="[&_dt]:w-24 [&_dt]:shrink-0">
        <KvRow label={t('context.facts.state')} align="start">
          {/* The word and its age, apart, as the list's row spaces them. */}
          <span className="inline-flex items-baseline gap-1.5">
            <StateWord session={session} />
          </span>
        </KvRow>
        {workspace ? (
          <KvRow label={t('context.facts.workspace')} align="start">
            {workspace}
          </KvRow>
        ) : null}
        {cost ? (
          <KvRow label={t('context.facts.cost')} align="start">
            {formatMicroUsd(cost, { locale: i18n.language })}
          </KvRow>
        ) : null}
      </KvList>
      <details data-testid="session-facts-more" className="group">
        <summary
          id={titleId}
          className="flex cursor-pointer list-none items-center gap-1 text-caption font-medium text-foreground"
        >
          {t('context.facts.title')}
          <ChevronRight
            aria-hidden
            className="size-3.5 text-muted-foreground transition-transform group-open:rotate-90 motion-reduce:transition-none"
          />
        </summary>
        <KvList className="mt-1 [&_dt]:w-24 [&_dt]:shrink-0">
          {driver ? (
            <KvRow label={t('context.facts.tool')} align="start">
              {toolName(driver)}
            </KvRow>
          ) : null}
          {account ? (
            <KvRow label={t('context.facts.account')} align="start">
              {account}
            </KvRow>
          ) : null}
          {model ? (
            <KvRow label={t('context.facts.model')} align="start">
              {[modelName(model), effort].filter(Boolean).join(' · ')}
            </KvRow>
          ) : null}
          {run ? <AccessRow run={run} /> : null}
        </KvList>
        {accountRef ? <AccountUsage profileRef={accountRef} /> : null}
      </details>
    </section>
  )
}

function AccessRow({ run }: { run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const words = useAccessWords(run)
  return (
    <KvRow label={t('context.facts.access')} align="start">
      {words}
    </KvRow>
  )
}

/** The session's own account's share of the week and of five hours, as its tool last
 * reported it, in the words every place that names the account uses; nothing when it
 * reported neither, or when the profile is not an account. */
function AccountUsage({ profileRef }: { profileRef: string }) {
  const { t } = useTranslation('sessions')
  const titleId = useId()
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const account = useQuery({
    queryKey: [
      ...agentOpsKeys.account(activeTenant, boundary.epoch, profileRef),
      'usage',
    ],
    queryFn: ({ signal }) => agentOpsApi.getAccount(profileRef, { signal }),
    enabled: can('sessions:account:read') && !!activeTenant,
    // An unnamed profile answers not found: no usage, and nothing to ask again.
    retry: false,
    staleTime: 60_000,
  })
  const usage = account.data?.usage
  if (sharesOf(usage).length === 0) return null
  return (
    <section
      aria-labelledby={titleId}
      data-testid="session-account-usage"
      className="flex flex-col gap-2"
    >
      <h3 id={titleId} className="text-body font-semibold text-text">
        {t('context.usage.title')}
      </h3>
      <UsageLine usage={usage} />
    </section>
  )
}
