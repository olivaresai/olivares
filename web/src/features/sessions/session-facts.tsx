// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION, AS A PERSON READS IT, at the head of Details (the regression design): its state,
// workspace, tool, account, model and access in words, then the account's 5-hour and weekly
// use. A fact the engine did not send is left out, never replaced by an engine reference;
// the references stay in the pane's References block below.
import { useQuery } from '@tanstack/react-query'
import { useId } from 'react'
import { useTranslation } from 'react-i18next'
import { KvList, KvRow } from '@/components/ui/kv'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { AccountUsage as AdditionalUsage } from '@/features/agentops/account-usage'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { modelName } from '@/features/agentops/model-name'
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
  const titleId = useId()
  const live = session.live
  const driver = run?.provider_driver || live?.provider
  const model = run?.model_ref || run?.usage_model_ref
  const effort = run?.effort
    ? t(`agentops:effort.${run.effort}`, { defaultValue: run.effort })
    : ''
  const cost = live?.cost_micro_usd
  return (
    <>
      <section aria-labelledby={titleId} data-testid="session-facts">
        <h3 id={titleId} className="text-body font-semibold text-text">
          {t('context.facts.title')}
        </h3>
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
              {[
                modelName(model),
                effort.charAt(0).toLocaleUpperCase() + effort.slice(1),
              ]
                .filter(Boolean)
                .join(' · ')}
            </KvRow>
          ) : null}
          {run ? <AccessRow run={run} /> : null}
          {cost ? (
            <KvRow label={t('context.facts.cost')} align="start">
              {formatMicroUsd(cost, { locale: i18n.language })}
            </KvRow>
          ) : null}
        </KvList>
      </section>
      {accountRef ? <AccountUsage profileRef={accountRef} /> : null}
    </>
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

/** The windows Details names, by the engine's stable window name. */
const WINDOWS = { five_hour: 'fiveHour', seven_day: 'week' } as const

/** The 5-hour and weekly use of the session's own account, as its tool last reported it;
 * nothing when it reported none, or when the profile is not an account. */
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
  const windows = (account.data?.usage ?? []).flatMap((u) => {
    const key = WINDOWS[u.window as keyof typeof WINDOWS]
    return key && u.utilization !== undefined
      ? [{ key, percent: Math.round(u.utilization) }]
      : []
  })
  const additional = account.data?.usage?.filter((u) => !(u.window in WINDOWS))
  if (windows.length === 0 && !additional?.length) return null
  return (
    <section
      aria-labelledby={titleId}
      data-testid="session-account-usage"
      className="flex flex-col gap-2"
    >
      <h3 id={titleId} className="text-body font-semibold text-text">
        {t('context.usage.title')}
      </h3>
      {windows.map((w) => {
        const label = t(`context.usage.${w.key}`, { percent: w.percent })
        return (
          <div key={w.key} className="flex flex-col gap-1">
            <meter
              min={0}
              max={100}
              value={w.percent}
              aria-valuenow={w.percent}
              aria-label={label}
              className="h-1.5 w-32"
            />
            <span className="text-caption tabular-nums text-text-2">
              {label}
            </span>
          </div>
        )
      })}
      <AdditionalUsage usage={additional} />
    </section>
  )
}
