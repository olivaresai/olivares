// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { SecretRef } from '@/components/data/secret-ref'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { RelTimeLabel } from '@/features/shared'
import { capabilitiesApi, capabilitiesKeys } from './api'
import './i18n'
import type { RevisionDTO } from './types'
import { ListTruncationBadge } from '@/features/_intel'

const ACTION_VARIANT: Record<string, BadgeVariant> = {
  create: 'success',
  update: 'info',
  delete: 'danger',
}

export interface RevisionsSheetProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  configId: string | null
  serverRef: string
}

export function RevisionsSheet({
  open,
  onOpenChange,
  configId,
  serverRef,
}: RevisionsSheetProps) {
  const { t } = useTranslation('capabilities')
  const { activeTenant } = useAuth()
  const query = useQuery({
    queryKey: capabilitiesKeys.revisions(activeTenant, configId ?? ''),
    queryFn: () => capabilitiesApi.listRevisions(configId!),
    enabled: open && !!configId,
  })

  const revisions = query.data?.items ?? []

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{t('revisions.title')}</SheetTitle>
          <SheetDescription>
            {t('revisions.subtitle', { server: serverRef })}
          </SheetDescription>
        </SheetHeader>

        {/* El aviso vive FUERA del ScrollArea a proposito: dentro, el usuario tendria que
            desplazarse hasta el final para enterarse de que la lista esta incompleta, que es
            justo cuando ya ha sacado su conclusion. */}
        <ListTruncationBadge
          query={query}
          label={t('revisions.truncated', { n: revisions.length })}
          hint={t('revisions.truncatedHint')}
        />

        <ScrollArea className="-mr-4 flex-1 pr-4">
          {query.isLoading ? (
            <div className="flex flex-col gap-3">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-20 w-full" />
              ))}
            </div>
          ) : query.error instanceof ApiError &&
            query.error.isStepUpRequired ? (
            /* El más profundo de la lista —hoja lateral tras pulsar una config— y el más
               obstinado: cerrar y reabrir vuelve a disparar el mismo 403 indefinidamente,
               sin ninguna salida ofrecida. Ceremonia delante; el rol se queda detrás. */
            <StepUpRequiredState
              action="generic"
              onElevated={() => void query.refetch()}
            />
          ) : query.error instanceof ApiError && query.error.isForbidden ? (
            <ForbiddenState />
          ) : query.error ? (
            <ErrorState retry={() => query.refetch()} />
          ) : revisions.length === 0 ? (
            <EmptyState title={t('revisions.empty')} />
          ) : (
            <ol className="flex flex-col gap-3">
              {revisions.map((r: RevisionDTO) => (
                <li
                  key={`${r.server_ref}:${r.revision}`}
                  className="rounded-lg border border-border bg-surface p-3"
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-mono text-sm font-medium tabular-nums text-foreground">
                      {t('revisions.revision')} {r.revision}
                    </span>
                    <Badge
                      variant={ACTION_VARIANT[r.change_action] ?? 'neutral'}
                    >
                      {t(`revisions.actions.${r.change_action}`, {
                        defaultValue: r.change_action,
                      })}
                    </Badge>
                  </div>
                  <div className="mt-1 flex items-center gap-2 text-xs text-muted-foreground">
                    <span className="font-mono">{r.change_actor}</span>
                    <span>·</span>
                    <RelTimeLabel ts={r.changed_at} />
                  </div>
                  <div className="mt-2 flex flex-wrap items-center gap-1.5 text-xs">
                    <Badge variant="neutral">{r.transport}</Badge>
                    {r.scope && <Badge variant="outline">{r.scope}</Badge>}
                    {r.secret_refs.map((s, i) => (
                      <SecretRef key={i} name={s.name} />
                    ))}
                  </div>
                </li>
              ))}
            </ol>
          )}
        </ScrollArea>

        <p className="text-xs text-muted-foreground">
          {t('revisions.caption')}
        </p>
      </SheetContent>
    </Sheet>
  )
}
