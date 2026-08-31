// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery, type QueryKey } from '@tanstack/react-query'
import { History } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { CodeDiff } from '@/components/ui/code-diff'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { cn } from '@/lib/utils'
import { RelTime } from './rel-time'

export type RevisionOperation = 'create' | 'update' | 'delete' | 'restore'

export interface EntityRevision<TSnapshot> {
  id: string
  op: RevisionOperation
  snapshot: TSnapshot
  actor: string
  actor_kind: string
  at: string
}

export interface RevisionsResponse<TSnapshot> {
  items: EntityRevision<TSnapshot>[]
  cursor?: string
  has_more: boolean
}

export interface RevisionListParams {
  cursor?: string
  limit?: number
}

export interface RevisionsSheetLabels {
  title: string
  description: string
  caption?: string
  empty: string
  loading: string
  loadMore: string
  compareTitle: string
  selectTwo: string
  selectRevision: (operation: string, actor: string) => string
  originalLabel: string
  modifiedLabel: string
  restore: string
  restoreTitle: string
  restoreDescription: string
  restoreConfirm: string
  restoreSuccess: string
  operations: Record<RevisionOperation, string>
}

export interface RevisionsSheetProps<TSnapshot, TEntity> {
  open: boolean
  onOpenChange: (open: boolean) => void
  queryKey: QueryKey
  listRevisions: (
    params?: RevisionListParams,
  ) => Promise<RevisionsResponse<TSnapshot>>
  restoreRevision: (revisionId: string) => Promise<TEntity>
  invalidateKeys: QueryKey[]
  canWrite: boolean
  /** Deleted entities are evidence-only: their delete revision cannot resurrect them. */
  entityDeleted?: boolean
  labels: RevisionsSheetLabels
}

const PAGE_SIZE = 50

const OP_VARIANT: Record<RevisionOperation, BadgeVariant> = {
  create: 'success',
  update: 'info',
  delete: 'danger',
  restore: 'accent',
}

/**
 * Shared chronological revision browser for restorable module configuration.
 * The backend remains authoritative: snapshots are displayed verbatim and restore
 * is always confirmed, audited, invalidated, and toasted through the privileged
 * mutation pattern.
 */
export function RevisionsSheet<TSnapshot, TEntity>({
  open,
  onOpenChange,
  queryKey,
  listRevisions,
  restoreRevision,
  invalidateKeys,
  canWrite,
  entityDeleted = false,
  labels,
}: RevisionsSheetProps<TSnapshot, TEntity>) {
  const [selectedIds, setSelectedIds] = useState<string[] | null>(null)
  const [restoreTarget, setRestoreTarget] =
    useState<EntityRevision<TSnapshot> | null>(null)

  const revisionsQ = useInfiniteQuery({
    queryKey,
    queryFn: ({ pageParam }) =>
      listRevisions({ cursor: pageParam, limit: PAGE_SIZE }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
    enabled: open,
  })

  const revisions = useMemo(
    () => revisionsQ.data?.pages.flatMap((page) => page.items) ?? [],
    [revisionsQ.data],
  )

  // The backend pages the ledger OLDEST-first (ascending keyset — there is no
  // reverse fetch), so page 1 alone would make "latest vs previous" silently
  // compare the two OLDEST revisions once an entity outgrows one page. Drain
  // the remaining pages automatically while the sheet is open: history is
  // complete and the derived default below is the true latest pair.
  useEffect(() => {
    if (
      open &&
      revisionsQ.hasNextPage &&
      !revisionsQ.isFetchingNextPage &&
      !revisionsQ.isError
    ) {
      void revisionsQ.fetchNextPage()
    }
  }, [open, revisionsQ])

  // Until the operator makes an explicit selection, compare the latest
  // revision with its immediate predecessor (exact once the drain above
  // completes) without overwriting a deliberate comparison.
  const effectiveSelectedIds = useMemo(
    () => selectedIds ?? revisions.slice(-2).map((revision) => revision.id),
    [revisions, selectedIds],
  )

  const selected = useMemo(() => {
    const byId = new Map(revisions.map((revision) => [revision.id, revision]))
    return effectiveSelectedIds
      .map((id) => byId.get(id))
      .filter((revision): revision is EntityRevision<TSnapshot> => !!revision)
  }, [effectiveSelectedIds, revisions])

  function toggleRevision(id: string) {
    setSelectedIds((stored) => {
      const current =
        stored ?? revisions.slice(-2).map((revision) => revision.id)
      if (current.includes(id)) return current.filter((item) => item !== id)

      // Replacing a full pair: pair the clicked revision with its immediate
      // predecessor. The OLDEST revision has none — fall back to a single
      // selection (the pane then asks for a second pick) instead of the old
      // clamp, which produced the degenerate {id, id} pair.
      const idx = revisions.findIndex((r) => r.id === id)
      const predecessor = idx > 0 ? revisions[idx - 1].id : undefined
      const selectedSet = new Set(
        current.length < 2
          ? [...current, id]
          : predecessor
            ? [predecessor, id]
            : [id],
      )
      return revisions
        .filter((revision) => selectedSet.has(revision.id))
        .map((revision) => revision.id)
        .slice(-2)
    })
  }

  const restore = usePrivilegedMutation<string, TEntity>({
    mutationFn: (revisionId) => restoreRevision(revisionId),
    invalidateKeys: [...invalidateKeys, queryKey],
    successMessage: labels.restoreSuccess,
    onDone: () => setRestoreTarget(null),
  })

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-5xl">
        <SheetHeader>
          <SheetTitle>{labels.title}</SheetTitle>
          <SheetDescription>{labels.description}</SheetDescription>
        </SheetHeader>

        {labels.caption ? (
          <p className="rounded-md border border-info-line bg-info-soft px-3 py-2 text-xs text-info">
            {labels.caption}
          </p>
        ) : null}

        <ScrollArea className="-mr-4 flex-1 pr-4">
          {revisionsQ.isLoading ? (
            <div role="status" className="flex justify-center py-12">
              <span className="sr-only">{labels.loading}</span>
              <Spinner />
            </div>
          ) : revisionsQ.isError ? (
            <ErrorState retry={() => void revisionsQ.refetch()} />
          ) : revisions.length === 0 ? (
            <EmptyState icon={<History />} title={labels.empty} />
          ) : (
            <div className="flex flex-col gap-5">
              <ol className="flex flex-col gap-2">
                {revisions.map((revision) => {
                  const checked = effectiveSelectedIds.includes(revision.id)
                  const canRestoreRevision =
                    canWrite && !(entityDeleted && revision.op === 'delete')
                  const operation = labels.operations[revision.op]
                  const selectionLabel = labels.selectRevision(
                    operation,
                    revision.actor,
                  )
                  return (
                    <li
                      key={revision.id}
                      className={cn(
                        'flex flex-wrap items-center gap-3 rounded-lg border bg-surface p-3',
                        checked ? 'border-accent-line' : 'border-border',
                      )}
                    >
                      <Checkbox
                        checked={checked}
                        onCheckedChange={() => toggleRevision(revision.id)}
                        aria-label={selectionLabel}
                      />
                      <Badge variant={OP_VARIANT[revision.op]}>
                        {operation}
                      </Badge>
                      <span className="min-w-0 flex-1 truncate font-mono text-xs text-foreground">
                        {revision.actor}
                        {revision.actor_kind ? (
                          <span className="ml-1 text-muted-foreground">
                            ({revision.actor_kind})
                          </span>
                        ) : null}
                      </span>
                      <RelTime
                        ts={revision.at}
                        className="text-xs text-muted-foreground"
                      />
                      {canRestoreRevision ? (
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setRestoreTarget(revision)}
                        >
                          {labels.restore}
                        </Button>
                      ) : null}
                    </li>
                  )
                })}
              </ol>

              {revisionsQ.hasNextPage ? (
                <Button
                  variant="ghost"
                  size="sm"
                  className="self-center"
                  onClick={() => void revisionsQ.fetchNextPage()}
                  disabled={revisionsQ.isFetchingNextPage}
                >
                  {labels.loadMore}
                </Button>
              ) : null}

              <section className="flex flex-col gap-2">
                <h3 className="text-sm font-medium text-foreground">
                  {labels.compareTitle}
                </h3>
                {selected.length === 2 ? (
                  <CodeDiff
                    original={JSON.stringify(selected[0].snapshot, null, 2)}
                    modified={JSON.stringify(selected[1].snapshot, null, 2)}
                    language="json"
                    originalLabel={labels.originalLabel}
                    modifiedLabel={labels.modifiedLabel}
                    height="28rem"
                  />
                ) : (
                  <p className="text-sm text-muted-foreground">
                    {labels.selectTwo}
                  </p>
                )}
              </section>
            </div>
          )}
        </ScrollArea>
      </SheetContent>

      <ConfirmDialog
        open={restoreTarget !== null}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) setRestoreTarget(null)
        }}
        title={labels.restoreTitle}
        description={labels.restoreDescription}
        confirmLabel={labels.restoreConfirm}
        pending={restore.isPending}
        onConfirm={() => {
          if (restoreTarget) restore.mutate(restoreTarget.id)
        }}
      />
    </Sheet>
  )
}
