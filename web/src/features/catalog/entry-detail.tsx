// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import {
  CheckCircle2,
  Pencil,
  Rocket,
  Send,
  Trash2,
  XCircle,
} from 'lucide-react'
import { type ReactNode, useState } from 'react'
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
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { RelTimeLabel } from '@/features/shared'
import { AdmissionPanel } from './admission-panel'
import { catalogApi, catalogKeys } from './api'
import { EntryEditorDialog } from './entry-editor'
import { InstantiateDialog } from './instantiate-dialog'
import { SigningBadge } from './signing-badge'
import { EntryVerifyPanel } from './verify-panel'
import './i18n'
import { admissionKind } from './types'
import type { EntryDTO } from './types'

function Section({
  title,
  caption,
  action,
  children,
}: {
  title: ReactNode
  caption?: ReactNode
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium text-foreground">{title}</h3>
        {action}
      </div>
      {caption && <p className="text-xs text-muted-foreground">{caption}</p>}
      {children}
    </section>
  )
}

export interface EntryDetailSheetProps {
  entryId: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function EntryDetailSheet({
  entryId,
  open,
  onOpenChange,
}: EntryDetailSheetProps) {
  const { t } = useTranslation(['catalog', 'common'])
  const { activeTenant, can } = useAuth()
  const canWrite = can('catalog:entry:write')
  const canAdmin = can('catalog:entry:admin')
  const canInstantiate = can('catalog:instance:write')

  const [editorOpen, setEditorOpen] = useState(false)
  const [instantiateOpen, setInstantiateOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [confirmSubmit, setConfirmSubmit] = useState(false)
  const [confirmApprove, setConfirmApprove] = useState(false)
  const [confirmDeprecate, setConfirmDeprecate] = useState(false)

  const query = useQuery({
    queryKey: catalogKeys.entry(activeTenant, entryId ?? ''),
    queryFn: () => catalogApi.getEntry(entryId!),
    enabled: open && !!entryId,
  })
  const entry = query.data

  const invalidate = () => [
    catalogKeys.entry(activeTenant, entryId ?? ''),
    catalogKeys.entries(activeTenant),
    catalogKeys.verify(activeTenant, entryId ?? ''),
  ]

  const remove = usePrivilegedMutation({
    mutationFn: () => catalogApi.deleteEntry(entryId!),
    invalidateKeys: () => [catalogKeys.entries(activeTenant)],
    successMessage: t('confirm.deleted'),
    onDone: () => {
      setConfirmDelete(false)
      onOpenChange(false)
    },
  })
  const submit = usePrivilegedMutation({
    mutationFn: () => catalogApi.submitEntry(entryId!),
    invalidateKeys: invalidate,
    successMessage: t('confirm.submitted'),
    onDone: () => setConfirmSubmit(false),
  })
  const approve = usePrivilegedMutation({
    mutationFn: () => catalogApi.approveEntry(entryId!),
    invalidateKeys: invalidate,
    successMessage: t('confirm.approved'),
    onDone: () => setConfirmApprove(false),
  })
  const deprecate = usePrivilegedMutation({
    mutationFn: () => catalogApi.deprecateEntry(entryId!),
    invalidateKeys: invalidate,
    successMessage: t('confirm.deprecated'),
    onDone: () => setConfirmDeprecate(false),
  })

  const isDraft = entry?.status === 'draft'
  const isPending = entry?.status === 'pending'
  const isApproved = entry?.status === 'approved'

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{entry?.name ?? t('detail.title')}</SheetTitle>
          {entry && (
            <SheetDescription className="flex flex-wrap items-center gap-1.5">
              <Badge variant="neutral">
                {t(`kind.${entry.kind}`, { defaultValue: entry.kind })}
              </Badge>
              <Badge variant="outline" className="font-mono">
                {entry.slug}
              </Badge>
              <Badge variant="outline" className="font-mono">
                {entry.version}
              </Badge>
              {entry.status && <StatusBadge status={entry.status} />}
              <SigningBadge entry={entry} />
            </SheetDescription>
          )}
        </SheetHeader>

        <ScrollArea className="-mr-4 flex-1 pr-4">
          {query.isLoading ? (
            <div className="flex flex-col gap-3">
              {Array.from({ length: 5 }).map((_, i) => (
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
          ) : query.error || !entry ? (
            <ErrorState retry={() => query.refetch()} />
          ) : (
            <div className="flex flex-col gap-5">
              {/* Lifecycle action bar — each gated on status + permission. */}
              <div className="flex flex-wrap gap-2">
                {isDraft && canWrite && (
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => setEditorOpen(true)}
                  >
                    <Pencil />
                    {t('detail.actions.edit')}
                  </Button>
                )}
                {isDraft && canWrite && (
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => setConfirmSubmit(true)}
                  >
                    <Send />
                    {t('detail.actions.submit')}
                  </Button>
                )}
                {(isDraft || isPending) && canAdmin && (
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={() => setConfirmApprove(true)}
                  >
                    <CheckCircle2 />
                    {t('detail.actions.approve')}
                  </Button>
                )}
                {isApproved && canInstantiate && (
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => setInstantiateOpen(true)}
                  >
                    <Rocket />
                    {t('detail.actions.instantiate')}
                  </Button>
                )}
                {isApproved && canAdmin && (
                  <Button
                    variant="destructive"
                    size="sm"
                    onClick={() => setConfirmDeprecate(true)}
                  >
                    <XCircle />
                    {t('detail.actions.deprecate')}
                  </Button>
                )}
                {isDraft && canWrite && (
                  <Button
                    variant="destructive"
                    size="sm"
                    onClick={() => setConfirmDelete(true)}
                  >
                    <Trash2 />
                    {t('detail.actions.delete')}
                  </Button>
                )}
              </div>

              <DetailBody entry={entry} />
            </div>
          )}
        </ScrollArea>
      </SheetContent>

      {/* Edit draft (privileged form). */}
      {entry && (
        <EntryEditorDialog
          open={editorOpen}
          onOpenChange={setEditorOpen}
          entry={entry}
        />
      )}

      {/* Instantiate from an approved entry (launches a governance request). */}
      {entry && isApproved && (
        <InstantiateDialog
          open={instantiateOpen}
          onOpenChange={setInstantiateOpen}
          entry={entry}
        />
      )}

      {/* Delete draft (high risk — danger + typed phrase). */}
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={t('confirm.deleteTitle')}
        description={t('confirm.deleteBody')}
        tone="danger"
        confirmLabel={t('confirm.deleteConfirm')}
        confirmPhrase={t('confirm.deletePhrase')}
        pending={remove.isPending}
        onConfirm={() => remove.mutate(undefined)}
      />

      {/* Submit for review. */}
      <ConfirmDialog
        open={confirmSubmit}
        onOpenChange={setConfirmSubmit}
        title={t('confirm.submitTitle')}
        description={t('confirm.submitBody')}
        confirmLabel={t('confirm.submitConfirm')}
        pending={submit.isPending}
        onConfirm={() => submit.mutate(undefined)}
      />

      {/* Approve (HIGH risk — freezes/hashes/signs; danger + typed phrase). */}
      <ConfirmDialog
        open={confirmApprove}
        onOpenChange={setConfirmApprove}
        title={t('confirm.approveTitle')}
        description={t('confirm.approveBody')}
        tone="danger"
        confirmLabel={t('confirm.approveConfirm')}
        confirmPhrase={t('confirm.approvePhrase')}
        pending={approve.isPending}
        onConfirm={() => approve.mutate(undefined)}
      />

      {/* Deprecate (danger). */}
      <ConfirmDialog
        open={confirmDeprecate}
        onOpenChange={setConfirmDeprecate}
        title={t('confirm.deprecateTitle')}
        description={t('confirm.deprecateBody')}
        tone="danger"
        confirmLabel={t('confirm.deprecateConfirm')}
        pending={deprecate.isPending}
        onConfirm={() => deprecate.mutate(undefined)}
      />
    </Sheet>
  )
}

function DetailBody({ entry }: { entry: EntryDTO }) {
  const { t } = useTranslation('catalog')

  return (
    <>
      <Section title={t('detail.identity')}>
        <KvList>
          <KvRow label={t('detail.kind')}>
            {t(`kind.${entry.kind}`, { defaultValue: entry.kind })}
          </KvRow>
          <KvRow label={t('detail.slug')} mono>
            {entry.slug}
          </KvRow>
          <KvRow label={t('detail.version')} mono>
            {entry.version}
          </KvRow>
          {entry.status && (
            <KvRow label={t('detail.status')}>
              <StatusBadge status={entry.status} />
            </KvRow>
          )}
          {entry.owner_ref && (
            <KvRow label={t('detail.owner')} mono>
              {entry.owner_ref}
            </KvRow>
          )}
          {entry.summary && (
            <KvRow label={t('detail.summary')} align="start">
              {entry.summary}
            </KvRow>
          )}
        </KvList>
      </Section>

      <Separator />

      {/* Spec — read-only reference; secrets referenced by name/locator only. */}
      <Section title={t('detail.spec')} caption={t('detail.specCaption')}>
        {entry.spec && Object.keys(entry.spec).length > 0 ? (
          <pre className="max-h-72 overflow-auto rounded-md border border-border bg-muted px-3 py-2 font-mono text-xs text-foreground">
            {JSON.stringify(entry.spec, null, 2)}
          </pre>
        ) : (
          <EmptyState title={t('detail.specEmpty')} />
        )}
      </Section>

      <Separator />

      {/* Attestation admission (mcp/connector) — the durable "why admitted/refused". */}
      {admissionKind(entry.kind) && (
        <>
          <AdmissionPanel entry={entry} />
          <Separator />
        </>
      )}

      {/* Honest verification posture (verify + signing posture). */}
      <EntryVerifyPanel entry={entry} />

      <Separator />

      {/* Integrity & approval metadata. */}
      <Section title={t('detail.integrity')}>
        {entry.content_hash ? (
          <KvList>
            <KvRow label={t('detail.contentHash')} align="start" mono>
              <span className="block break-all">{entry.content_hash}</span>
            </KvRow>
            <KvRow label={t('detail.signed')}>
              <span aria-hidden="true">{entry.signed ? '✓' : '—'}</span>
              <span className="sr-only">
                {t(entry.signed ? 'common:states.yes' : 'common:states.no')}
              </span>
            </KvRow>
            {entry.sig_alg && (
              <KvRow label={t('detail.sigAlg')} mono>
                {entry.sig_alg}
              </KvRow>
            )}
            {entry.signed_by && (
              <KvRow label={t('detail.signedBy')} mono>
                {entry.signed_by}
              </KvRow>
            )}
            {entry.approved_by && (
              <KvRow label={t('detail.approvedBy')} mono>
                {entry.approved_by}
              </KvRow>
            )}
            {entry.approved_at && (
              <KvRow label={t('detail.approvedAt')}>
                <RelTimeLabel ts={entry.approved_at} />
              </KvRow>
            )}
          </KvList>
        ) : (
          <p className="text-sm text-muted-foreground">
            {t('detail.notApprovedYet')}
          </p>
        )}
      </Section>
    </>
  )
}
