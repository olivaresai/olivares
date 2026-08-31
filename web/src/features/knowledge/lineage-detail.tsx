// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
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
import { SourceModeBadge } from '@/features/shared'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { RelTimeLabel } from '@/features/shared'
import { EgressBadge, HashChip } from './chips'
import { knowledgeApi, knowledgeKeys } from './api'
import './i18n'

export interface LineageDetailSheetProps {
  lineageId: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * LineageDetailSheet opens one origin->answer record. The read is SELF-AUDITED
 * (the backend writes a knowledge.lineage.get audit event), surfaced by the banner
 * on the lineage tab. Evidence is references + hashes only — egress_provider and the
 * content/query hashes are rendered as opaque hash chips, never expanded.
 */
export function LineageDetailSheet({
  lineageId,
  open,
  onOpenChange,
}: LineageDetailSheetProps) {
  const { t } = useTranslation('knowledge')
  const { activeTenant } = useAuth()

  const query = useQuery({
    queryKey: knowledgeKeys.lineageOne(activeTenant, lineageId ?? ''),
    queryFn: () => knowledgeApi.getLineage(lineageId!),
    enabled: open && !!lineageId,
  })
  const rec = query.data

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{t('lineage.detailTitle')}</SheetTitle>
          {rec && (
            <SheetDescription className="flex flex-wrap items-center gap-1.5">
              <Badge variant={rec.decision === 'denied' ? 'danger' : 'success'}>
                {rec.decision === 'denied'
                  ? t('lineage.denied')
                  : t('lineage.allowed')}
              </Badge>
              <EgressBadge value={rec.egress} />
            </SheetDescription>
          )}
        </SheetHeader>

        <ScrollArea className="-mr-4 flex-1 pr-4">
          {query.isLoading ? (
            <div className="flex flex-col gap-3">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-16 w-full" />
              ))}
            </div>
          ) : query.error instanceof ApiError &&
            query.error.isStepUpRequired ? (
            /* Aseguramiento antes que rol: `isForbidden` es sólo el status
               (lib/api/errors.ts:59) y el 403 de ceremonia lo satisface también. */
            <StepUpRequiredState
              action="generic"
              onElevated={() => void query.refetch()}
            />
          ) : query.error instanceof ApiError && query.error.isForbidden ? (
            <ForbiddenState />
          ) : query.error || !rec ? (
            <ErrorState retry={() => query.refetch()} />
          ) : (
            <div className="flex flex-col gap-5">
              <KvList>
                <KvRow label={t('lineage.kbRef')} mono>
                  {rec.kb_ref}
                </KvRow>
                <KvRow label={t('lineage.agentRef')} mono>
                  {rec.agent_ref}
                </KvRow>
                {rec.session_ref && (
                  <KvRow label={t('lineage.session')} mono>
                    {rec.session_ref}
                  </KvRow>
                )}
                <KvRow label={t('common.residency')}>
                  {rec.residency_region}
                </KvRow>
                <KvRow label={t('lineage.queryHash')} align="start">
                  <HashChip value={rec.query_hash} />
                </KvRow>
                <KvRow label={t('lineage.egress')}>
                  <EgressBadge value={rec.egress} />
                </KvRow>
                {rec.egress && rec.egress_provider && (
                  <KvRow label={t('common.egressProvider')} align="start">
                    <HashChip
                      value={rec.egress_provider}
                      title={t('common.egressProviderHint')}
                    />
                  </KvRow>
                )}
                <KvRow label={t('lineage.results')} mono>
                  {rec.result_count}
                </KvRow>
                <KvRow label={t('lineage.occurredAt')}>
                  <RelTimeLabel ts={rec.occurred_at} />
                </KvRow>
              </KvList>

              {rec.decision === 'denied' && rec.reason && (
                <div
                  role="alert"
                  className="flex flex-col gap-1 rounded-md border border-warning-line bg-warning-soft px-3 py-2"
                >
                  <span className="text-sm font-medium text-warning">
                    {t('lineage.reason')}
                  </span>
                  <span className="text-xs text-muted-foreground">
                    {rec.reason}
                  </span>
                </div>
              )}

              {rec.source_refs.length > 0 && (
                <>
                  <Separator />
                  <section className="flex flex-col gap-2">
                    <h3 className="text-sm font-medium text-foreground">
                      {t('lineage.sourceRefs')}
                    </h3>
                    <div className="flex flex-wrap gap-1.5">
                      {rec.source_refs.map((s) => (
                        <Badge key={s} variant="outline" className="font-mono">
                          {s}
                        </Badge>
                      ))}
                    </div>
                  </section>
                </>
              )}

              {rec.chunk_refs.length > 0 && (
                <>
                  <Separator />
                  <section className="flex flex-col gap-2">
                    <h3 className="text-sm font-medium text-foreground">
                      {t('lineage.chunkRefs')}
                    </h3>
                    <p className="text-xs text-muted-foreground">
                      {t('lineage.chunkRefsCaption')}
                    </p>
                    <ul className="flex flex-col gap-2">
                      {rec.chunk_refs.map((c) => (
                        <li
                          key={c.chunk_id}
                          className="rounded-md border border-border bg-surface p-2.5 text-xs"
                        >
                          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                            <span className="text-muted-foreground">
                              {t('lineage.chunkId')}:{' '}
                              <span className="font-mono text-foreground">
                                {c.chunk_id}
                              </span>
                            </span>
                            <span className="text-muted-foreground">
                              {t('lineage.docRef')}:{' '}
                              <span className="font-mono text-foreground">
                                {c.doc_ref}
                              </span>
                            </span>
                            <Badge variant="neutral">{c.source_kind}</Badge>
                            <SourceModeBadge value={c.source_mode} />
                            <HashChip value={c.content_hash} />
                          </div>
                        </li>
                      ))}
                    </ul>
                  </section>
                </>
              )}
            </div>
          )}
        </ScrollArea>
      </SheetContent>
    </Sheet>
  )
}
