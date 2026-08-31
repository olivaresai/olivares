// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Download, Pencil, RefreshCw, Search, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { KvList, KvRow } from '@/components/ui/kv'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusBadge } from '@/components/data/badges'
import { SourceModeBadge } from '@/features/shared'
import { ListTruncationBadge } from '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  AclRefs,
  ClassificationBadge,
  EmbedModelBadge,
  EmbedPolicyBadge,
  HashChip,
  ResidencyBadge,
} from './chips'
import { knowledgeApi, knowledgeKeys } from './api'
import { IngestDialog } from './ingest-dialog'
import { KbEditorDialog } from './kb-editor'
import { QueryDialog } from './query-dialog'
import './i18n'
import type { DocumentDTO, KbDTO } from './types'

export interface KbDetailSheetProps {
  kbId: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function KbDetailSheet({
  kbId,
  open,
  onOpenChange,
}: KbDetailSheetProps) {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant, can } = useAuth()
  const canWrite = can('knowledge:kb:write')
  const canAdmin = can('knowledge:kb:admin')
  const canRetrieve = can('knowledge:retrieval:read')

  const [editorOpen, setEditorOpen] = useState(false)
  const [ingestOpen, setIngestOpen] = useState(false)
  const [queryOpen, setQueryOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [confirmReindex, setConfirmReindex] = useState(false)

  const kbQuery = useQuery({
    queryKey: knowledgeKeys.kb(activeTenant, kbId ?? ''),
    queryFn: () => knowledgeApi.getKb(kbId!),
    enabled: open && !!kbId,
  })
  const kb = kbQuery.data

  // Light poll while any document is pending (ingest/reindex progresses status).
  const docsQuery = useQuery({
    queryKey: knowledgeKeys.documents(activeTenant, kbId ?? ''),
    queryFn: () => knowledgeApi.listDocuments(kbId!),
    enabled: open && !!kbId,
    refetchInterval: (q) =>
      open && (q.state.data?.items ?? []).some((d) => d.status === 'pending')
        ? 12_000
        : false,
  })
  const docs = docsQuery.data?.items ?? []
  const hasPending = docs.some((d) => d.status === 'pending')

  const remove = usePrivilegedMutation<void, { deleted: boolean }>({
    mutationFn: () => knowledgeApi.deleteKb(kbId!),
    invalidateKeys: () => [knowledgeKeys.kbs(activeTenant)],
    successMessage: t('kbRemove.done'),
    onDone: () => {
      setConfirmDelete(false)
      onOpenChange(false)
    },
  })

  const reindex = usePrivilegedMutation<void, { reindexed: number }>({
    mutationFn: () => knowledgeApi.reindex(kbId!),
    invalidateKeys: () => [
      knowledgeKeys.kb(activeTenant, kbId ?? ''),
      knowledgeKeys.documents(activeTenant, kbId ?? ''),
      knowledgeKeys.kbs(activeTenant),
    ],
    successMessage: t('reindex.done'),
    onDone: () => setConfirmReindex(false),
  })

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{kb?.name ?? t('kbDetail.title')}</SheetTitle>
          {kb && (
            <SheetDescription className="flex flex-wrap items-center gap-1.5">
              <ClassificationBadge value={kb.classification} />
              <ResidencyBadge value={kb.residency_region} />
              <EmbedPolicyBadge value={kb.embed_policy} />
              <StatusBadge status={kb.status} />
            </SheetDescription>
          )}
        </SheetHeader>

        <ScrollArea className="-mr-4 flex-1 pr-4">
          {kbQuery.isLoading ? (
            <div className="flex flex-col gap-3">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-16 w-full" />
              ))}
            </div>
          ) : kbQuery.error instanceof ApiError &&
            kbQuery.error.isStepUpRequired ? (
            // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
            // (lib/api/errors.ts:59) y un `step_up_required` lo satisface también. Leerlo
            // primero sustituía la base de conocimiento entera por «no tienes autorización».
            <StepUpRequiredState
              action="generic"
              onElevated={() => void kbQuery.refetch()}
            />
          ) : kbQuery.error instanceof ApiError && kbQuery.error.isForbidden ? (
            <ForbiddenState />
          ) : kbQuery.error || !kb ? (
            <ErrorState retry={() => kbQuery.refetch()} />
          ) : (
            <DetailBody
              kb={kb}
              docs={docs}
              docsQuery={docsQuery}
              docsLoading={docsQuery.isLoading}
              docsError={docsQuery.error}
              onRetryDocs={() => docsQuery.refetch()}
              canWrite={canWrite}
              canAdmin={canAdmin}
              canRetrieve={canRetrieve}
              hasPending={hasPending}
              reindexPending={reindex.isPending}
              onEdit={() => setEditorOpen(true)}
              onDelete={() => setConfirmDelete(true)}
              onIngest={() => setIngestOpen(true)}
              onReindex={() => setConfirmReindex(true)}
              onQuery={() => setQueryOpen(true)}
            />
          )}
        </ScrollArea>
      </SheetContent>

      {kb && (
        <KbEditorDialog
          open={editorOpen}
          onOpenChange={setEditorOpen}
          kb={kb}
        />
      )}
      {kb && (
        <IngestDialog open={ingestOpen} onOpenChange={setIngestOpen} kb={kb} />
      )}
      {kb && canRetrieve && (
        <QueryDialog open={queryOpen} onOpenChange={setQueryOpen} kb={kb} />
      )}

      {kb && (
        <ConfirmDialog
          open={confirmReindex}
          onOpenChange={setConfirmReindex}
          title={t('reindex.title')}
          description={t('reindex.body', { name: kb.name })}
          confirmLabel={t('reindex.confirm')}
          pending={reindex.isPending}
          onConfirm={() => reindex.mutate()}
        />
      )}

      {kb && (
        <ConfirmDialog
          open={confirmDelete}
          onOpenChange={setConfirmDelete}
          title={t('kbRemove.title')}
          description={t('kbRemove.body', { name: kb.name })}
          tone="danger"
          confirmPhrase={t('kbRemove.phrase')}
          confirmLabel={t('kbRemove.confirm')}
          pending={remove.isPending}
          onConfirm={() => remove.mutate()}
        />
      )}
    </Sheet>
  )
}

function DetailBody({
  kb,
  docs,
  docsQuery,
  docsLoading,
  docsError,
  onRetryDocs,
  canWrite,
  canAdmin,
  canRetrieve,
  hasPending,
  reindexPending,
  onEdit,
  onDelete,
  onIngest,
  onReindex,
  onQuery,
}: {
  kb: KbDTO
  docs: DocumentDTO[]
  docsQuery: { data?: unknown; error?: unknown }
  docsLoading: boolean
  docsError: unknown
  onRetryDocs: () => void
  canWrite: boolean
  canAdmin: boolean
  canRetrieve: boolean
  hasPending: boolean
  reindexPending: boolean
  onEdit: () => void
  onDelete: () => void
  onIngest: () => void
  onReindex: () => void
  onQuery: () => void
}) {
  const { t } = useTranslation('knowledge')

  return (
    <div className="flex flex-col gap-5">
      {/* Action bar — every control gated by its permission. */}
      <div className="flex flex-wrap gap-2">
        {canRetrieve && (
          <Button variant="secondary" size="sm" onClick={onQuery}>
            <Search />
            {t('kbDetail.query')}
          </Button>
        )}
        {canWrite && (
          <Button variant="secondary" size="sm" onClick={onIngest}>
            <Download />
            {t('kbDetail.ingest')}
          </Button>
        )}
        {canWrite && (
          <Button variant="ghost" size="sm" onClick={onEdit}>
            <Pencil />
            {t('kbDetail.editKb')}
          </Button>
        )}
        {canWrite && hasPending && (
          <Button
            variant="ghost"
            size="sm"
            onClick={onReindex}
            disabled={reindexPending}
          >
            <RefreshCw />
            {t('kbDetail.reindex')}
          </Button>
        )}
        {canAdmin && (
          <Button variant="destructive" size="sm" onClick={onDelete}>
            <Trash2 />
            {t('kbDetail.deleteKb')}
          </Button>
        )}
      </div>

      {/* Governance metadata. */}
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium text-foreground">
          {t('kbDetail.governance')}
        </h3>
        <KvList>
          <KvRow label={t('common.classification')}>
            <ClassificationBadge value={kb.classification} />
          </KvRow>
          <KvRow label={t('common.residency')}>
            <ResidencyBadge value={kb.residency_region} />
          </KvRow>
          <KvRow label={t('common.embedPolicy')}>
            <EmbedPolicyBadge value={kb.embed_policy} />
          </KvRow>
          <KvRow label={t('common.embedModel')} align="start">
            <EmbedModelBadge value={kb.embed_model} />
          </KvRow>
          <KvRow label={t('kbDetail.dim')} mono>
            {kb.dim}
          </KvRow>
          <KvRow label={t('common.acl')} align="start">
            <div className="flex flex-wrap justify-end gap-1.5">
              <AclRefs acl={kb.default_acl} />
            </div>
          </KvRow>
          <KvRow label={t('kbs.docs')} mono>
            {kb.doc_count}
          </KvRow>
          <KvRow label={t('kbs.chunks')} mono>
            {kb.chunk_count}
          </KvRow>
        </KvList>
      </section>

      <Separator />

      {/* Documents — metadata + provenance only, never the body. */}
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium text-foreground">
          {t('kbDetail.documents')}
        </h3>
        <p className="text-xs text-muted-foreground">
          {t('kbDetail.documentsCaption')}
        </p>
        <ListTruncationBadge
          query={docsQuery}
          label={t('kbDetail.documentsTruncated', { n: docs.length })}
          hint={t('kbDetail.documentsTruncatedHint')}
          className="px-0 pt-0 pb-0"
        />
        {docsLoading ? (
          <div className="flex flex-col gap-2">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </div>
        ) : docsError instanceof ApiError && docsError.isStepUpRequired ? (
          // Segundo sitio del mismo fichero, y por eso la guarda de clase mira POSICIÓN y no
          // sólo presencia: con la ceremonia arriba, un `isForbidden` suelto aquí abajo pasaría
          // desapercibido. La lista de documentos se gatea aparte de la base que la contiene.
          <StepUpRequiredState action="generic" onElevated={onRetryDocs} />
        ) : docsError instanceof ApiError && docsError.isForbidden ? (
          <ForbiddenState />
        ) : docsError ? (
          <ErrorState retry={onRetryDocs} />
        ) : docs.length === 0 ? (
          <EmptyState
            title={t('kbDetail.docsEmpty')}
            description={t('kbDetail.docsEmptyHint')}
          />
        ) : (
          <ul className="flex flex-col gap-2">
            {docs.map((d) => (
              <li
                key={d.id}
                className="rounded-md border border-border bg-surface p-3"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="truncate text-sm font-medium text-foreground">
                    {d.title || d.source_doc_id}
                  </span>
                  <Badge variant={d.status === 'pending' ? 'info' : 'success'}>
                    {d.status === 'pending'
                      ? t('kbDetail.docPending')
                      : t('kbDetail.docIndexed')}
                  </Badge>
                </div>
                <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                  <Badge variant="neutral">{d.source_kind}</Badge>
                  <SourceModeBadge value={d.source_mode} />
                  <ClassificationBadge value={d.classification} />
                  <span title={t('kbDetail.redactionsHint')}>
                    {t('kbDetail.redactions')}:{' '}
                    <span className="font-mono tabular-nums">
                      {d.redaction_count}
                    </span>
                  </span>
                  <span>
                    {t('kbs.chunks')}:{' '}
                    <span className="font-mono tabular-nums">
                      {d.chunk_count}
                    </span>
                  </span>
                  <HashChip value={d.content_hash} />
                </div>
                {d.acl.length > 0 && (
                  <div className="mt-1.5">
                    <AclRefs acl={d.acl} />
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
