// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link2, Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { EXPORT_FORMATS, type ExportFormat } from '@/features/audit/types'
import { securityApi, securityKeys } from './api'
import { CASE_LINK_KINDS, type CaseLinkKind } from './types'
import type {
  CaseStatus,
  FindingSeverity,
  ForensicCase,
  UpdateCaseInput,
} from './types'

const STATUSES: CaseStatus[] = ['open', 'investigating', 'contained', 'closed']
const SEVERITIES: FindingSeverity[] = ['low', 'medium', 'high', 'critical']

/** Abre un caso. El motor exige `title` y una severidad del enum, y fija `status:"open"`
 *  él mismo — por eso este formulario NO ofrece estado: ofrecerlo sugeriría que se puede
 *  abrir un caso ya cerrado, que el motor no permite. */
export function NewCaseButton({ canWrite }: { canWrite: boolean }) {
  const { t } = useTranslation('security')
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const report = useFailedActionReporter()
  const [open, setOpen] = useState(false)
  const [title, setTitle] = useState('')
  const [severity, setSeverity] = useState<FindingSeverity>('medium')

  const mutation = useMutation({
    mutationFn: () => securityApi.createCase({ title: title.trim(), severity }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: securityKeys.cases(activeTenant),
      })
      toast.success(t('cases.created'))
      setOpen(false)
      setTitle('')
      setSeverity('medium')
    },
    onError: (e) => report(e),
  })

  if (!canWrite) return null

  return (
    <>
      <Button variant="primary" size="sm" onClick={() => setOpen(true)}>
        <Plus />
        {t('cases.new')}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('cases.new')}</DialogTitle>
            <DialogDescription>{t('cases.newHint')}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-4">
            <Field label={t('cases.title')} htmlFor="case-title">
              <Input
                id="case-title"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
            </Field>
            <Field label={t('cases.severity')} htmlFor="case-severity">
              <Select
                value={severity}
                onValueChange={(v) => setSeverity(v as FindingSeverity)}
              >
                <SelectTrigger id="case-severity">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {SEVERITIES.map((s) => (
                    <SelectItem key={s} value={s}>
                      {t(`severity.${s}`, { defaultValue: s })}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setOpen(false)}>
              {t('cases.cancel')}
            </Button>
            <Button
              variant="primary"
              disabled={!title.trim() || mutation.isPending}
              onClick={() => mutation.mutate()}
            >
              {t('cases.create')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

/** Edición del caso + exportación.
 *
 *  ⛔ El PATCH manda SÓLO lo que cambia. El motor lee punteros (`req.Status != nil`), así que
 *  un campo ausente se conserva; mandar el caso entero convertiría «cambiar el estado» en
 *  reescribir el título y el resumen con lo que hubiera en pantalla. */
export function CaseActions({
  forensicCase,
  canWrite,
}: {
  forensicCase: ForensicCase
  canWrite: boolean
}) {
  const { t } = useTranslation('security')
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const report = useFailedActionReporter()
  const [status, setStatus] = useState<CaseStatus>(forensicCase.status)
  const [severity, setSeverity] = useState<FindingSeverity>(
    forensicCase.severity,
  )
  const [format, setFormat] = useState<ExportFormat>(EXPORT_FORMATS[0])

  const changed: UpdateCaseInput = {
    ...(status !== forensicCase.status ? { status } : {}),
    ...(severity !== forensicCase.severity ? { severity } : {}),
  }
  const dirty = Object.keys(changed).length > 0

  const save = useMutation({
    mutationFn: () => securityApi.updateCase(forensicCase.id, changed),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: securityKeys.caseTimeline(activeTenant, forensicCase.id),
      })
      await queryClient.invalidateQueries({
        queryKey: securityKeys.cases(activeTenant),
      })
      toast.success(t('cases.updated'))
    },
    onError: (e) => report(e),
  })

  const exportIt = useMutation({
    mutationFn: () => securityApi.exportCase(forensicCase.id, format),
    onSuccess: () => toast.success(t('cases.exported')),
    onError: (e) => report(e),
  })

  return (
    <div className="flex flex-wrap items-end gap-3">
      <Field label={t('cases.status')} htmlFor="case-status">
        <Select
          value={status}
          onValueChange={(v) => setStatus(v as CaseStatus)}
          disabled={!canWrite}
        >
          <SelectTrigger id="case-status" className="w-44">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {STATUSES.map((s) => (
              <SelectItem key={s} value={s}>
                {t(`cases.statusName.${s}`, { defaultValue: s })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>

      <Field label={t('cases.severity')} htmlFor="case-sev">
        <Select
          value={severity}
          onValueChange={(v) => setSeverity(v as FindingSeverity)}
          disabled={!canWrite}
        >
          <SelectTrigger id="case-sev" className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {SEVERITIES.map((s) => (
              <SelectItem key={s} value={s}>
                {t(`severity.${s}`, { defaultValue: s })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>

      {canWrite && (
        <Button
          variant="primary"
          size="sm"
          disabled={!dirty || save.isPending}
          onClick={() => save.mutate()}
        >
          {t('cases.save')}
        </Button>
      )}

      {/* La lista de formatos NO se escribe aquí: sale de `@/features/audit/types`, que es la
          copia declarada contra el catálogo del motor y está fijada contra la instantánea
          OpenAPI. Repetirla sería una tercera copia en la consola. */}
      <Field label={t('cases.exportFormat')} htmlFor="case-format">
        <Select
          value={format}
          onValueChange={(v) => setFormat(v as ExportFormat)}
        >
          <SelectTrigger id="case-format" className="w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {EXPORT_FORMATS.map((f) => (
              <SelectItem key={f} value={f}>
                {f}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Button
        variant="ghost"
        size="sm"
        disabled={exportIt.isPending}
        onClick={() => exportIt.mutate()}
      >
        {t('cases.export')}
      </Button>
    </div>
  )
}

/** La cadena de custodia: los enlaces del caso a hallazgos, secuencias del ledger, anomalías
 *  o notas. Las cuatro clases salen del enum del motor (`forensic.go:34`); cualquier otra es
 *  un 400. */
export function CaseLinksPanel({
  caseId,
  canWrite,
}: {
  caseId: string
  canWrite: boolean
}) {
  const { t } = useTranslation('security')
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const report = useFailedActionReporter()
  const [kind, setKind] = useState<CaseLinkKind>('finding')
  const [ref, setRef] = useState('')

  const q = useQuery({
    queryKey: securityKeys.caseLinks(activeTenant, caseId),
    queryFn: () => securityApi.caseLinks(caseId),
  })

  const add = useMutation({
    mutationFn: () =>
      securityApi.linkCase(caseId, { link_kind: kind, link_ref: ref.trim() }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: securityKeys.caseLinks(activeTenant, caseId),
      })
      toast.success(t('cases.linked'))
      setRef('')
    },
    onError: (e) => report(e),
  })

  return (
    <div className="flex flex-col gap-3">
      {q.isLoading && <Skeleton className="h-20 w-full" />}
      {q.data &&
        (q.data.items.length === 0 ? (
          <EmptyState icon={<Link2 />} title={t('cases.noLinks')} />
        ) : (
          <ul className="flex flex-col gap-1">
            {q.data.items.map((l, i) => (
              <li
                key={l.id ?? `${l.link_kind}:${l.link_ref}:${i}`}
                className="flex items-center gap-2 text-xs"
              >
                <Badge variant="neutral">
                  {t(`cases.linkKind.${l.link_kind}`, {
                    defaultValue: l.link_kind,
                  })}
                </Badge>
                <span className="font-mono break-all">{l.link_ref}</span>
                {l.linked_by && (
                  <span className="text-muted-foreground">{l.linked_by}</span>
                )}
              </li>
            ))}
          </ul>
        ))}

      {canWrite && (
        <div className="flex flex-wrap items-end gap-2">
          <Field label={t('cases.linkKindLabel')} htmlFor="link-kind">
            <Select
              value={kind}
              onValueChange={(v) => setKind(v as CaseLinkKind)}
            >
              <SelectTrigger id="link-kind" className="w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CASE_LINK_KINDS.map((k) => (
                  <SelectItem key={k} value={k}>
                    {t(`cases.linkKind.${k}`, { defaultValue: k })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('cases.linkRef')} htmlFor="link-ref">
            <Input
              id="link-ref"
              value={ref}
              onChange={(e) => setRef(e.target.value)}
            />
          </Field>
          <Button
            variant="ghost"
            size="sm"
            disabled={!ref.trim() || add.isPending}
            onClick={() => add.mutate()}
          >
            <Link2 />
            {t('cases.link')}
          </Button>
        </div>
      )}
    </div>
  )
}
