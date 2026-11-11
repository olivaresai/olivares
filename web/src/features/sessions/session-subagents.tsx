// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE AGENTS A SESSION SPAWNED, in its Details: each with its state and one line of what it
// answered, and the total. The same read the Activity tab makes (session-activity.tsx), shared
// through its cache; the Activity tab keeps each agent's transcript.
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { BoxedList, BoxedListRow } from '@/components/ui/boxed-list'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatInt, formatTokens } from '@/lib/format'
import { StateGlyph, summaryOf } from './session-activity'
import './i18n'

export function SessionSubagents({ run }: { run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const query = useQuery({
    queryKey: [
      ...agentOpsKeys.runActivity(activeTenant, run.run_ref),
      run.state,
    ],
    queryFn: ({ signal }) => agentOpsApi.activity(run.run_ref, { signal }),
    placeholderData: keepPreviousData,
  })
  // The engine's 4xx: it does not show this session's activity (another tool, a session
  // that held secrets) and the Activity tab says why, so the group stays out. Any other
  // failure (the network, a 5xx) is a read that failed: said here, with Retry.
  const refused = query.error instanceof ApiError && query.error.status < 500
  if (query.isError && !refused)
    return (
      <section data-testid="context-subagents">
        <h3 className="text-caption font-medium text-foreground">
          {t('context.subagents')}
        </h3>
        <QueryErrorState
          error={query.error}
          retry={() => void query.refetch()}
          title={t('activity.failed')}
          className="mt-1"
        />
      </section>
    )
  const agents = query.data?.agents ?? []
  if (!agents.length) return null
  const finished = agents.filter((a) => a.state === 'completed').length
  const tokens = agents.reduce((sum, a) => sum + (a.tokens ?? 0), 0)
  return (
    <section data-testid="context-subagents">
      <h3 className="flex items-baseline gap-2 text-caption font-medium text-foreground">
        {t('context.subagents')}
        <span className="ml-auto font-normal tabular-nums text-muted-foreground">
          {t('context.subagentsTotal', {
            finished: formatInt(finished),
            tokens: formatTokens(tokens),
          })}
        </span>
      </h3>
      <BoxedList className="mt-1">
        {agents.map((agent) => {
          const name = agent.description || agent.type || t('activity.agent')
          const summary = summaryOf(agent, agent.description ?? '')
          return (
            <BoxedListRow
              key={agent.tool_use_id}
              data-testid="context-subagent"
            >
              <StateGlyph state={agent.state} />
              <div className="min-w-0 flex-1">
                <p className="truncate text-caption text-foreground">{name}</p>
                {summary ? (
                  <p className="truncate text-caption text-muted-foreground">
                    {summary}
                  </p>
                ) : null}
              </div>
              {agent.tokens ? (
                <span className="shrink-0 text-caption tabular-nums text-muted-foreground">
                  {t('activity.agentTokens', {
                    value: formatTokens(agent.tokens),
                  })}
                </span>
              ) : null}
            </BoxedListRow>
          )
        })}
      </BoxedList>
    </section>
  )
}
