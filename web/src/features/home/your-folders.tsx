// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// YOUR FOLDERS: the folders a person is given, each with what a session may
// do there, and one Start that opens New session on it. It reads the folders
// the New session form reads (one cache entry), so Start opens the form on a
// folder the form already holds.
import { useId, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Folder } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { Button, buttonVariants } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { PathText } from '@/components/ui/path-text'
import { Skeleton } from '@/components/ui/skeleton'
import { Tag } from '@/components/ui/tag'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import type { WorkspaceDTO } from '@/features/agentops/types'
import { useNewSessionDialog } from '@/features/first-hour/new-session-store'
import {
  registeredFoldersQuery,
  sessionWrites,
} from '@/features/first-hour/folders-query'
import { cn } from '@/lib/utils'
import './i18n'

/** How many folders, newest first, show before Show all: the person's sessions
 * stay in view on a server where every registered folder is shared. */
const FOLDER_ROWS = 8

function FolderRow({
  folder,
  autoFocus,
}: {
  folder: WorkspaceDTO
  /** The first row Show all opened takes the focus Show all held. */
  autoFocus?: boolean
}) {
  const { t } = useTranslation('home')
  const levelId = useId()
  const name = folder.name || folder.root_path
  return (
    <li className="border-b border-border last:border-b-0">
      {/* THE WHOLE ROW IS THE TARGET, 44 px high at phone width: it opens
          New session on this folder. The level is a word and the button's
          description, never a colour alone. It says what a session may do
          there by the rule the New session form uses: a read-only mount is
          read only, and every other mount writes. */}
      <button
        type="button"
        autoFocus={autoFocus}
        aria-label={t('folders.startIn', { folder: name })}
        aria-describedby={levelId}
        onClick={(event) =>
          useNewSessionDialog.setState({
            open: true,
            advanced: null,
            draft: { workspace: folder.workspace_ref },
            opener: event.currentTarget,
          })
        }
        className="flex min-h-[var(--console-list-row-height)] w-full min-w-0 items-center gap-2 px-3 py-1.5 text-left outline-none transition-colors hover:bg-muted focus-visible:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset max-sm:min-h-11"
      >
        <Folder aria-hidden className="size-4 shrink-0 text-muted-foreground" />
        {folder.name && folder.name !== folder.root_path ? (
          <span className="min-w-0 truncate text-body text-foreground">
            {folder.name}
          </span>
        ) : (
          <PathText value={folder.root_path} copy={false} />
        )}
        <Tag id={levelId} className="shrink-0">
          {sessionWrites(folder.mount_mode)
            ? t('folders.canWork')
            : t('folders.readOnly')}
        </Tag>
        <span
          aria-hidden
          className={cn(
            buttonVariants({ variant: 'secondary', size: 'sm' }),
            'ml-auto shrink-0',
          )}
        >
          {t('folders.start')}
        </span>
      </button>
    </li>
  )
}

export function YourFolders() {
  const { t } = useTranslation('home')
  const headingId = useId()
  const { tenant, epoch } = useAuthBoundary()
  const folders = useQuery(registeredFoldersQuery(tenant, epoch))
  const [all, setAll] = useState(false)
  const rows = (folders.data?.items ?? []).filter(
    (w) => w.state === 'active' && w.root_path,
  )
  const shown = all ? rows : rows.slice(0, FOLDER_ROWS)

  return (
    <section aria-labelledby={headingId}>
      <h2
        id={headingId}
        className="mb-1.5 text-heading font-semibold text-text"
      >
        {t('folders.title')}
      </h2>
      {folders.isError ? (
        // A failed read is not an empty list, and the two never look alike. A
        // refusal is not a failure either: it says who can grant it, no Retry.
        <QueryErrorState
          error={folders.error}
          title={t('folders.unavailableTitle')}
          description={t('folders.unavailableDescription')}
          retry={() => void folders.refetch()}
        />
      ) : folders.isPending ? (
        <div role="status" className="flex flex-col gap-3">
          <span className="sr-only">{t('folders.loading')}</span>
          {Array.from({ length: 2 }, (_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : rows.length === 0 ? (
        // Nothing to start here: who gives a folder is the one next step.
        <EmptyState
          icon={<Folder />}
          title={t('folders.emptyTitle')}
          description={t('folders.emptyDescription')}
        />
      ) : (
        <>
          <ul>
            {shown.map((w, i) => (
              <FolderRow
                key={w.workspace_ref}
                folder={w}
                autoFocus={all && i === FOLDER_ROWS}
              />
            ))}
          </ul>
          {shown.length < rows.length ? (
            <Button variant="ghost" size="sm" onClick={() => setAll(true)}>
              {t('folders.showAll', { total: rows.length })}
            </Button>
          ) : null}
        </>
      )}
    </section>
  )
}
