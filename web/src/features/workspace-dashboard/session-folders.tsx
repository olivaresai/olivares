// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useId, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { XScroll } from '@/components/data/scroll-edges'
import { StaticTable } from '@/components/data/static-table'
import { Button } from '@/components/ui/button'
import { PathText } from '@/components/ui/path-text'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from '@/components/ui/sheet'
import { WorkspaceWiki } from '@/features/agentops/workspace-wiki'
import type { WorkspaceDTO } from '@/features/agentops/types'
import { ListTruncationBadge } from '@/features/_intel'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { useViewAccess } from '@/features/navigation/authorization'
import { fold, viewById } from '@/features/navigation/model'
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
  const boundary = useAuthBoundary()
  const sessionsOn = useModuleOn('sessions')
  if (!tenant || !sessionsOn || !can('sessions:workspace:read')) return null
  return (
    <SessionFoldersList
      key={boundary.epoch}
      tenant={tenant}
      epoch={boundary.epoch}
    />
  )
}

function SessionFoldersList({
  tenant,
  epoch,
}: {
  tenant: string
  epoch: number
}) {
  const { t } = useTranslation('workspaceDashboard')
  const headingId = useId()
  const [wiki, setWiki] = useState<WorkspaceDTO | null>(null)
  const { navigable } = useViewAccess()
  const manageable = FOLDERS_VIEW !== undefined && navigable(FOLDERS_VIEW)
  const params = { state: 'active', limit: FOLDERS } as const
  const query = useQuery({
    queryKey: [
      ...agentOpsKeys.boundaryScope(tenant, epoch),
      'workspaces',
      params,
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.listWorkspaces(params, { tenant, signal }),
    staleTime: 30_000,
  })
  const all = query.data?.items ?? []
  // Find a folder by its name or path. Focused on entry, so opening Files and typing is the
  // whole search: one click to arrive, one to open the folder's wiki.
  const [find, setFind] = useState('')
  const filterRef = useRef<HTMLInputElement>(null)
  useEffect(() => filterRef.current?.focus(), [])
  const needle = fold(find.trim())
  const rows = needle
    ? all.filter(
        (w) =>
          fold(folderName(w)).includes(needle) ||
          fold(w.root_path).includes(needle),
      )
    : all

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
      <input
        ref={filterRef}
        type="search"
        value={find}
        onChange={(e) => setFind(e.target.value)}
        aria-label={t('sessionFolders.filter')}
        placeholder={t('sessionFolders.filter')}
        className="h-9 w-full max-w-sm rounded-ctl border border-ctl-border bg-background px-3 text-body outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-focus"
      />
      <ListTruncationBadge
        query={query}
        filas={all.length}
        label={t('truncation.label', { n: all.length })}
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
      ) : all.length === 0 ? (
        <p role="status" className="text-body text-text-2">
          {t('sessionFolders.empty')}
        </p>
      ) : rows.length === 0 ? (
        <p role="status" className="text-body text-text-2">
          {t('sessionFolders.noMatch', { query: find.trim() })}
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
                  {/* w-full max-w-0: the column takes the width that is left (max-w-0 alone
                      gives it none), so a long folder is cut in its middle at any viewport
                      instead of widening the table. */}
                  <td className="w-full max-w-0">
                    <div className="flex items-center justify-between gap-3">
                      {/* No copy action here: its 24 px (32 px under a finger) is the room the
                          path needs at 390 px, and the Name column already says which folder
                          it is. The whole path is the hover, and a selection copies it. */}
                      <PathText value={w.root_path} copy={false} />
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={t('sessionFolders.wiki', {
                          folder: folderName(w),
                        })}
                        onClick={() => setWiki(w)}
                      >
                        {t('agentops:wiki.title')}
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </StaticTable>
        </XScroll>
      )}
      <Sheet
        open={wiki !== null}
        onOpenChange={(open) => !open && setWiki(null)}
      >
        <SheetContent className="flex w-full flex-col gap-4 overflow-y-auto sm:max-w-3xl">
          {wiki && (
            <>
              <SheetHeader>
                <SheetTitle>{folderName(wiki)}</SheetTitle>
                <SheetDescription>
                  <PathText value={wiki.root_path} copy={false} wrap />
                </SheetDescription>
              </SheetHeader>
              <WorkspaceWiki workspace={wiki} />
            </>
          )}
        </SheetContent>
      </Sheet>
    </section>
  )
}
