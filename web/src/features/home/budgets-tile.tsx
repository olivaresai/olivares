// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// BUDGETS ON NOW (console remake 26.10, CONCEPT-IA "spend vs budget"): how many enabled
// budgets there are and whether any is PROVEN over its limit. It is a financial claim, so it
// reads only the canonical amount the Cost page reads (BudgetStatus.amount through
// `budgetAmount`), under the Cost page's own cache keys:
//
//  - "over limit" only where `over_limit.result` is `proven` on a known amount (BudgetCard's
//    rule), never from the legacy percentages;
//  - a status that failed, is missing its amount or cannot prove the crossing is counted as
//    "amount unknown", never as within the limit;
//  - "none over its limit" only when every enabled budget was read and each is known and not
//    over. With more enabled budgets than this tile reads, it says how many it read.
import { useModuleOn } from '@/stores/modules'
import { useQueries, useQuery } from '@tanstack/react-query'
import { Wallet } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { finopsApi, finopsKeys } from '@/features/finops/api'
import { useAuth } from '@/lib/auth/context'
import { formatInt } from '@/lib/format'
import { BUDGET_STATUS_READS, budgetVerdict } from './budget-verdict'
import { EstateTile, type TileState } from './components'
import './i18n'

export function BudgetsTile() {
  // Budgets live in finops: while it is off there is nothing to read (ARCH C1).
  const finopsOn = useModuleOn('finops')
  const { t } = useTranslation('home')
  const { activeTenant } = useAuth()
  const budgetsQ = useQuery({
    queryKey: finopsKeys.budgets(activeTenant),
    queryFn: () => finopsApi.budgets(),
    enabled: !!activeTenant && finopsOn,
  })
  const enabled = (budgetsQ.data?.items ?? []).filter((b) => b.enabled)
  const read = enabled.slice(0, BUDGET_STATUS_READS)
  const statusQs = useQueries({
    queries: read.map((b) => ({
      queryKey: finopsKeys.budgetStatus(activeTenant, b.id),
      queryFn: () => finopsApi.budgetStatus(b.id, { tenant: activeTenant }),
      enabled: !!activeTenant && finopsOn,
    })),
  })

  const state: TileState = budgetsQ.isError
    ? 'unavailable'
    : budgetsQ.isLoading || statusQs.some((q) => q.isLoading)
      ? 'loading'
      : 'ready'
  const verdicts = statusQs.map((q) =>
    q.isSuccess ? budgetVerdict(q.data) : ('unknown' as const),
  )
  const over = verdicts.filter((v) => v === 'over').length
  const unknown = verdicts.filter((v) => v === 'unknown').length
  // The list itself may be a page; with more on the server, no "none over" is claimed.
  const partial = enabled.length > read.length || !!budgetsQ.data?.has_more

  const caption =
    enabled.length === 0
      ? t('tiles.budgets.none')
      : over > 0
        ? t('tiles.budgets.over', { count: over })
        : unknown > 0
          ? t('tiles.budgets.unknown', { count: unknown })
          : partial
            ? t('tiles.budgets.partial', { n: read.length })
            : t('tiles.budgets.within')

  return (
    <EstateTile
      compact
      to="/finops"
      icon={<Wallet />}
      label={t('tiles.budgets.label')}
      state={state}
      value={formatInt(enabled.length)}
      tone={over > 0 ? 'danger' : unknown > 0 ? 'warning' : undefined}
      caption={caption}
    />
  )
}
