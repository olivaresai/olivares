// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useId } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { StaticTable } from '@/components/data/static-table'
import { formatInt } from '@/lib/format'
import { cuentaConSuelo } from './count-floor'
import { workspaceDashboardApi, workspaceDashboardKeys } from './api'
import './i18n'

/**
 * What a workspace holds, from the engine's one contents read: every kind that
 * declares workspace lineage, counted through the workspace confinement. A kind
 * appears here by declaring lineage, with no console code. Admin tier only
 * (`tenant:admin`): callers render it only for principals who hold it.
 */
export function WorkspaceContents({
  tenant,
  workspaceId,
}: {
  tenant: string
  workspaceId: string
}) {
  const { t } = useTranslation('workspaceDashboard')
  const headingId = useId()
  const query = useQuery({
    queryKey: workspaceDashboardKeys.contents(tenant, workspaceId),
    queryFn: ({ signal }) =>
      workspaceDashboardApi.contents(workspaceId, { tenant, signal }),
    staleTime: 30_000,
  })
  const held = query.data?.kinds.filter((row) => row.count > 0) ?? []

  return (
    <section aria-labelledby={headingId} className="flex flex-col gap-2">
      <h2
        id={headingId}
        className="text-overline text-muted-foreground uppercase"
      >
        {t('contents.title')}
      </h2>
      {query.isError ? (
        <p role="alert" className="text-body text-text-2">
          {t('contents.unavailable')}
        </p>
      ) : !query.data ? (
        <p role="status" className="text-body text-text-2">
          {t('contents.loading')}
        </p>
      ) : held.length === 0 ? (
        <p role="status" className="text-body text-text-2">
          {t('contents.empty')}
        </p>
      ) : (
        <StaticTable oneLine>
          <thead>
            <tr>
              <th>{t('kind')}</th>
              <th>{t('contents.count')}</th>
            </tr>
          </thead>
          <tbody>
            {held.map((row) => (
              <tr key={row.kind}>
                <td className="font-mono text-caption">{row.kind}</td>
                <td>{cuentaConSuelo(row.count, row.capped, formatInt)}</td>
              </tr>
            ))}
          </tbody>
        </StaticTable>
      )}
    </section>
  )
}
