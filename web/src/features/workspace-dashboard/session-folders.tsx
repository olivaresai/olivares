// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useId } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { XScroll } from '@/components/data/scroll-edges'
import { StaticTable } from '@/components/data/static-table'
import { Button } from '@/components/ui/button'
import { ListTruncationBadge } from '@/features/_intel'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useViewAccess } from '@/features/navigation/authorization'
import { viewById } from '@/features/navigation/model'
import { TAB_PARAM } from '@/features/sessions/session-address'
import { useAuth } from '@/lib/auth/context'
import { useModuleOn } from '@/stores/modules'
import './i18n'

/** Enough to see what sessions use; Sessions lists them all. */
const FOLDERS = 20

/** The door whose permission (sessions:run:read) the Workspaces tab needs; /sessions asks
 * for sessions:live:read and turns that tab away. */
const FOLDERS_VIEW = viewById('agentops')

/** The name a folder was registered under, else the last segment of its path. */
const folderName = (w: { name?: string; root_path: string }) =>
  w.name || w.root_path.split('/').filter(Boolean).pop() || w.root_path

/**
 * The folders sessions work in, beside the organization workspace the page is about: a
 * workspace groups agents and people, a folder is a directory on this server. Shown only
 * where the sessions module runs and the principal may read its folders; the link to
 * manage them only where Sessions opens.
 */
export function SessionFolders({ tenant }: { tenant: string | null }) {
  const { can } = useAuth()
  const sessionsOn = useModuleOn('sessions')
  if (!tenant || !sessionsOn || !can('sessions:workspace:read')) return null
  return <SessionFoldersList tenant={tenant} />
}

function SessionFoldersList({ tenant }: { tenant: string }) {
  const { t } = useTranslation('workspaceDashboard')
  const headingId = useId()
  const { navigable } = useViewAccess()
  const manageable = FOLDERS_VIEW !== undefined && navigable(FOLDERS_VIEW)
  const params = { state: 'active', limit: FOLDERS } as const
  const query = useQuery({
    queryKey: agentOpsKeys.workspaces(tenant, params),
    queryFn: ({ signal }) =>
      agentOpsApi.listWorkspaces(params, { tenant, signal }),
    staleTime: 30_000,
  })
  const rows = query.data?.items ?? []

  return (
    <section aria-labelledby={headingId} className="flex flex-col gap-2">
      <div className="flex min-h-8 items-center justify-between gap-2">
        <h2
          id={headingId}
          className="text-overline text-muted-foreground uppercase"
        >
          {t('sessionFolders.title')}
        </h2>
        {manageable && (
          <Button variant="ghost" size="sm" asChild>
            <Link
              to={'/agentops' as never}
              search={{ [TAB_PARAM]: 'workspaces' } as never}
            >
              {t('sessionFolders.manage')}
            </Link>
          </Button>
        )}
      </div>
      <p className="text-body text-text-2">{t('sessionFolders.intro')}</p>
      <ListTruncationBadge
        query={query}
        filas={rows.length}
        label={t('truncation.label', { n: rows.length })}
        hint={t('sessionFolders.truncationHint')}
        className="px-0 pt-0 pb-1"
      />
      {query.isError ? (
        <p role="alert" className="text-body text-text-2">
          {t('sessionFolders.unavailable')}
        </p>
      ) : !query.data ? (
        <p role="status" className="text-body text-text-2">
          {t('sessionFolders.loading')}
        </p>
      ) : rows.length === 0 ? (
        <p role="status" className="text-body text-text-2">
          {t('sessionFolders.empty')}
        </p>
      ) : (
        <XScroll contentKey={rows.length}>
          <StaticTable oneLine>
            <thead>
              <tr>
                <th>{t('name')}</th>
                <th>{t('sessionFolders.folder')}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((w) => (
                <tr key={w.workspace_ref}>
                  <td className="font-medium">{folderName(w)}</td>
                  <td className="font-mono text-caption">{w.root_path}</td>
                </tr>
              ))}
            </tbody>
          </StaticTable>
        </XScroll>
      )}
    </section>
  )
}
