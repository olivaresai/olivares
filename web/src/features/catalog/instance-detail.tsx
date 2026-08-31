// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { CheckCircle2, Play, XCircle } from 'lucide-react'
import { type ReactNode, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Field } from '@/components/ui/field'
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
import { Textarea } from '@/components/ui/textarea'
import { StatusBadge } from '@/components/data/badges'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { catalogApi, catalogKeys } from './api'
import './i18n'
import type { InstanceDecision, InstanceDTO } from './types'

function Section({
  title,
  children,
}: {
  title: ReactNode
  children: ReactNode
}) {
  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-sm font-medium text-foreground">{title}</h3>
      {children}
    </section>
  )
}

export interface InstanceDetailSheetProps {
  instanceId: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function InstanceDetailSheet({
  instanceId,
  open,
  onOpenChange,
}: InstanceDetailSheetProps) {
  const { t } = useTranslation(['catalog', 'common'])
  const { activeTenant, can } = useAuth()
  const canAdmin = can('catalog:instance:admin')

  // The pending governance decision (drives the single ConfirmDialog).
  const [decision, setDecision] = useState<InstanceDecision | null>(null)
  const [note, setNote] = useState('')

  const query = useQuery({
    queryKey: catalogKeys.instance(activeTenant, instanceId ?? ''),
    queryFn: () => catalogApi.getInstance(instanceId!),
    enabled: open && !!instanceId,
  })
  const instance = query.data

  const transition = usePrivilegedMutation<InstanceDecision, InstanceDTO>({
    mutationFn: (status) =>
      catalogApi.transitionInstance(instanceId!, {
        status,
        ...(note.trim() ? { note: note.trim() } : {}),
      }),
    invalidateKeys: () => [
      catalogKeys.instance(activeTenant, instanceId ?? ''),
      catalogKeys.instances(activeTenant),
    ],
    successMessage: t('transition.done'),
    onDone: () => {
      setDecision(null)
      setNote('')
    },
  })

  function openDecision(next: InstanceDecision) {
    setNote('')
    setDecision(next)
  }

  const status = instance?.status
  const canApproveReject = status === 'requested'
  const canActivateReject = status === 'approved'

  const decisionTitle =
    decision === 'approved'
      ? t('transition.approveTitle')
      : decision === 'rejected'
        ? t('transition.rejectTitle')
        : decision === 'active'
          ? t('transition.activateTitle')
          : ''
  const decisionConfirm =
    decision === 'approved'
      ? t('transition.approveConfirm')
      : decision === 'rejected'
        ? t('transition.rejectConfirm')
        : decision === 'active'
          ? t('transition.activateConfirm')
          : ''

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{instance?.name ?? t('instanceDetail.title')}</SheetTitle>
          {instance && (
            <SheetDescription className="flex flex-wrap items-center gap-1.5">
              <Badge variant="neutral">
                {t(`kind.${instance.entry_kind}`, {
                  defaultValue: instance.entry_kind ?? '—',
                })}
              </Badge>
              {instance.entry_version && (
                <Badge variant="outline" className="font-mono">
                  {instance.entry_version}
                </Badge>
              )}
              {instance.status && <StatusBadge status={instance.status} />}
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
            // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
            // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que leerlo
            // primero sustituía la pantalla por «no tienes autorización» —falso, y sin salida—.
            //
            // DEFENSA EN PROFUNDIDAD, y lo digo porque en esta campaña ya presenté dos veces como
            // «camino vivo» algo que no lo era: HOY esta ruta no emite el código. Los emisores medidos
            // son las dos escrituras de `modules/governance` y las 21 llamadas a `requireAAL3` de
            // `core/api`, todas cubiertas ya. Esto se arregla porque el defecto es de FORMA y sobrevive
            // al día en que el gate llegue aquí, no porque alguien lo esté sufriendo ahora.
            <StepUpRequiredState
              action="generic"
              onElevated={() => void query.refetch()}
            />
          ) : query.error instanceof ApiError && query.error.isForbidden ? (
            <ForbiddenState />
          ) : query.error || !instance ? (
            <ErrorState retry={() => query.refetch()} />
          ) : (
            <div className="flex flex-col gap-5">
              {/* Governance action bar (admin only, gated by current state). */}
              {canAdmin && (canApproveReject || canActivateReject) && (
                <div className="flex flex-wrap gap-2">
                  {canApproveReject && (
                    <Button
                      variant="primary"
                      size="sm"
                      onClick={() => openDecision('approved')}
                    >
                      <CheckCircle2 />
                      {t('instanceDetail.actions.approve')}
                    </Button>
                  )}
                  {canActivateReject && (
                    <Button
                      variant="primary"
                      size="sm"
                      onClick={() => openDecision('active')}
                    >
                      <Play />
                      {t('instanceDetail.actions.activate')}
                    </Button>
                  )}
                  {(canApproveReject || canActivateReject) && (
                    <Button
                      variant="destructive"
                      size="sm"
                      onClick={() => openDecision('rejected')}
                    >
                      <XCircle />
                      {t('instanceDetail.actions.reject')}
                    </Button>
                  )}
                </div>
              )}

              <Section title={t('instanceDetail.provenance')}>
                <KvList>
                  <KvRow label={t('instanceDetail.entryId')} mono>
                    {instance.entry_id}
                  </KvRow>
                  {instance.entry_kind && (
                    <KvRow label={t('instanceDetail.entryKind')}>
                      {t(`kind.${instance.entry_kind}`, {
                        defaultValue: instance.entry_kind,
                      })}
                    </KvRow>
                  )}
                  {instance.entry_slug && (
                    <KvRow label={t('instanceDetail.entrySlug')} mono>
                      {instance.entry_slug}
                    </KvRow>
                  )}
                  {instance.entry_version && (
                    <KvRow label={t('instanceDetail.entryVersion')} mono>
                      {instance.entry_version}
                    </KvRow>
                  )}
                  {instance.target_ref && (
                    <KvRow label={t('instances.target')} mono>
                      {instance.target_ref}
                    </KvRow>
                  )}
                </KvList>
              </Section>

              <Separator />

              <Section title={t('instanceDetail.governance')}>
                <p className="mb-1 text-xs text-muted-foreground">
                  {t('instanceDetail.governanceCaption')}
                </p>
                <KvList>
                  {instance.status && (
                    <KvRow label={t('instances.status')}>
                      <StatusBadge status={instance.status} />
                    </KvRow>
                  )}
                  {instance.requested_by && (
                    <KvRow label={t('instances.requestedBy')} mono>
                      {instance.requested_by}
                    </KvRow>
                  )}
                  {instance.decided_by && (
                    <KvRow label={t('instances.decidedBy')} mono>
                      {instance.decided_by}
                    </KvRow>
                  )}
                  {instance.note && (
                    <KvRow label={t('instanceDetail.note')} align="start">
                      {instance.note}
                    </KvRow>
                  )}
                </KvList>
              </Section>
            </div>
          )}
        </ScrollArea>
      </SheetContent>

      {/* Governance decision (HIGH risk — danger; collects an optional note). */}
      <ConfirmDialog
        open={decision !== null}
        onOpenChange={(o) => {
          if (!o) setDecision(null)
        }}
        title={decisionTitle}
        description={t('transition.body')}
        tone="danger"
        confirmLabel={decisionConfirm}
        pending={transition.isPending}
        onConfirm={() => decision && transition.mutate(decision)}
      >
        <Field label={t('transition.noteLabel')} htmlFor="transition-note">
          <Textarea
            id="transition-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder={t('transition.notePlaceholder')}
            rows={2}
          />
        </Field>
      </ConfirmDialog>
    </Sheet>
  )
}
