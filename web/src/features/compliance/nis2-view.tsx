// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// NIS 2 Directive significant-incident classification in the console —.
//
// The engine has served these six routes since compliance.go:547-552. Until this
// file existed the console did not contain the string "nis2" ANYWHERE (measured:
// `grep -ric nis2 web/src` = 0, against `dora` = 13 files), so the only ways to
// classify an incident under Art 23(3) were curl and the API playground — which
// does reach them (`filterEndpointsForAdmin` hides only `system:*`,
// openapi-parser.ts:193-203), and a raw REST client is not an operating surface.
//
// Three engine rules shape this view, and each is rendered rather than discovered:
//
//   THE VERDICT IS NEVER LEGAL. The engine hardcodes `provisional: true`
//   (nis2incident.go:78) and ships a disclaimer saying the classification is
//   DECISION SUPPORT and the duty to notify rests with the entity (`:33-40`). So
//   "significant" is shown as a reporting TRIGGER to attest, never as a finding,
//   and the disclaimer rides every row.
//
//   PHASES ONLY GO FORWARD. early_warning → notification → intermediate → final,
//   with a 409 on any backward or same-phase move (`:295`). The advance dialog
//   offers only the phases ahead, so the rule is visible before the request.
//
//   CLASSIFY IS AN ADD-ON. Without the enterprise packager every classify is 501
//   (`:121-126`) while every READ keeps working. A 501 is drawn as the open-core
//   boundary it is — detected by status, not by matching prose — because a console
//   that shouts "request failed" at a deliberate boundary teaches operators to
//   distrust true errors.
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  CalendarClock,
  FileJson,
  Globe2,
  ShieldAlert,
  Siren,
  Trash2,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
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
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import {
  useFailedActionReporter,
  usePrivilegedMutation,
} from '@/lib/hooks/use-privileged-mutation'
import {
  AsyncSection,
  CaveatNotice,
  DisclaimerNote,
  HashChip,
  SeamBadge,
  SectionCard,
  SelfAuditNotice,
} from '@/features/_intel'
import {
  complianceApi,
  complianceKeys,
  confirmedDeleted,
  isOpenCoreSeam,
} from './api'
import {
  COMPLIANCE_MAX_DOCUMENT_BYTES,
  isKnownNis2Phase,
  NIS2_MAX_REFERENCE_RUNES,
  nis2PhasesAfter,
  nis2ReferenceTooLong,
  utf8ByteLength as utf8Bytes,
  type Nis2Incident,
  type Nis2Phase,
} from './types'

/** The engine's raw-body cap, and it REJECTS over it rather than truncating
 *  (oscalprofile.go readBoundedBody, 413; `maxReqBytes = 1 << 20` in
 *  helpers.go:33). Measured in BYTES, because that is what the engine counts.
 *
 *  It moved to types.ts in when the regulatory-operations tab next door
 *  grew five more writes governed by the SAME cap: one engine rule with two
 *  console constants is the drift this module has already paid for elsewhere.
 *  The local name stays so this view reads as it did. */
const MAX_IMPACT_BYTES = COMPLIANCE_MAX_DOCUMENT_BYTES

/** Deleting a classification removes governed evidence, so the destructive verb
 *  gets the typed-phrase guard rather than a lone button. */
const DELETE_PHRASE = 'DELETE'

export function Nis2Tab({
  canAdmin,
  canRead,
}: {
  canAdmin: boolean
  canRead: boolean
}) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [classifying, setClassifying] = useState(false)
  const [advancing, setAdvancing] = useState<Nis2Incident | null>(null)
  const [deleting, setDeleting] = useState<Nis2Incident | null>(null)
  const [inspecting, setInspecting] = useState<Nis2Incident | null>(null)
  const [exporting, setExporting] = useState<string | null>(null)

  const incidentsQ = useQuery({
    queryKey: complianceKeys.nis2Incidents(activeTenant),
    queryFn: () => complianceApi.nis2Incidents(),
    enabled: canRead,
  })

  if (!canRead) {
    return (
      <SectionCard title={t('nis2.title')}>
        <EmptyState icon={<Siren />} title={t('nis2.noAccess')} />
      </SectionCard>
    )
  }

  const runExport = (inc: Nis2Incident) => {
    setExporting(inc.id)
    complianceApi
      .exportNis2Incident(inc.id)
      .then((res) => {
        // The server's exact bytes, never recomputed here: this file is what an
        // auditor is handed, and it carries the LIVE ledger anchor the engine
        // computed at export time (nis2incident.go:341-345).
        const blob = new Blob([res.text], {
          type: res.content_type || 'application/octet-stream',
        })
        const url = URL.createObjectURL(blob)
        const a = document.createElement('a')
        a.href = url
        a.download = res.filename
        document.body.appendChild(a)
        a.click()
        a.remove()
        URL.revokeObjectURL(url)
        toast.success(t('nis2.exported'))
      })
      .catch((e: unknown) => toast.error(String((e as Error).message ?? e)))
      .finally(() => setExporting(null))
  }

  return (
    <>
      <SectionCard
        title={t('nis2.title')}
        description={t('nis2.description')}
        actions={
          canAdmin ? (
            <Button
              variant="primary"
              size="sm"
              onClick={() => setClassifying(true)}
            >
              <Siren />
              {t('nis2.classify')}
            </Button>
          ) : null
        }
      >
        {/* Reading and exporting a classification are privileged, self-audited
            reads (nis2incident.go:346). */}
        <SelfAuditNotice className="mb-3" />
        {/* The honesty rule of this plane, stated where the operator acts. */}
        <CaveatNotice tone="warning" className="mb-3">
          {t('nis2.provisionalHint')}
        </CaveatNotice>
        <AsyncSection query={incidentsQ} skeletonHeight={220}>
          {(list) =>
            list.items.length === 0 ? (
              <EmptyState
                icon={<Siren />}
                title={t('nis2.empty')}
                description={t('nis2.emptyHint')}
              />
            ) : (
              <div className="flex flex-col gap-2">
                {list.items.map((inc) => (
                  <Nis2Row
                    key={inc.id}
                    inc={inc}
                    canAdmin={canAdmin}
                    exporting={exporting === inc.id}
                    onAdvance={() => setAdvancing(inc)}
                    onDelete={() => setDeleting(inc)}
                    onInspect={() => setInspecting(inc)}
                    onExport={() => runExport(inc)}
                  />
                ))}
              </div>
            )
          }
        </AsyncSection>
      </SectionCard>

      {canAdmin ? (
        <ClassifyDialog open={classifying} onOpenChange={setClassifying} />
      ) : null}

      {inspecting ? (
        <Nis2DetailDialog
          inc={inspecting}
          onClose={() => setInspecting(null)}
        />
      ) : null}

      {canAdmin && advancing ? (
        <AdvancePhaseDialog
          inc={advancing}
          open={advancing !== null}
          onOpenChange={(v) => {
            if (!v) setAdvancing(null)
          }}
        />
      ) : null}

      {canAdmin && deleting ? (
        <DeleteIncidentDialog
          inc={deleting}
          open={deleting !== null}
          onOpenChange={(v) => {
            if (!v) setDeleting(null)
          }}
        />
      ) : null}
    </>
  )
}

/** One classification. `significant` is the fact an operator scans for — it is
 *  what starts the Art 23(4) clock — so it leads the row; and it is rendered as a
 *  reporting TRIGGER TO ATTEST, never as a legal finding, because the engine's own
 *  verdict is provisional by construction (nis2incident.go:78). */
function Nis2Row({
  inc,
  canAdmin,
  exporting,
  onAdvance,
  onDelete,
  onInspect,
  onExport,
}: {
  inc: Nis2Incident
  canAdmin: boolean
  exporting: boolean
  onAdvance: () => void
  onDelete: () => void
  onInspect: () => void
  onExport: () => void
}) {
  const { t } = useTranslation('compliance')
  // A phase this build cannot ORDER gets no advance action AND a stated reason.
  // Hiding the button alone would look identical to "already at the last phase",
  // which is the opposite news (nis2PhasesAfter, types.ts).
  const phaseKnown = isKnownNis2Phase(inc.phase)
  const canAdvance = nis2PhasesAfter(inc.phase).length > 0
  return (
    <div className="flex flex-wrap items-start justify-between gap-3 rounded-md border border-border p-3">
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={inc.significant ? 'danger' : 'neutral'}>
            {inc.significant ? t('nis2.significant') : t('nis2.notSignificant')}
          </Badge>
          {/* Always true on the wire; rendered anyway, because a verdict shown
              without it reads as a legal classification. */}
          {inc.provisional ? (
            <Badge variant="warning">{t('nis2.provisional')}</Badge>
          ) : null}
          <Badge variant="neutral">
            {t(`nis2.phase.${inc.phase}`, { defaultValue: inc.phase })}
          </Badge>
          {inc.cross_border ? (
            <Badge variant="warning" className="gap-1">
              <Globe2 className="size-3" />
              {t('nis2.crossBorder')}
            </Badge>
          ) : null}
          {inc.suspected_crime ? (
            <Badge variant="warning" className="gap-1">
              <AlertTriangle className="size-3" />
              {t('nis2.suspectedCrime')}
            </Badge>
          ) : null}
          <span className="font-medium">{inc.reference}</span>
        </div>
        {inc.rationale ? <p className="text-xs">{inc.rationale}</p> : null}
        {inc.criteria_met && inc.criteria_met.length > 0 ? (
          <p className="text-xs text-muted-foreground">
            {t('nis2.criteria', { list: inc.criteria_met.join(', ') })}
          </p>
        ) : null}
        {inc.note ? (
          <p className="text-xs text-muted-foreground">{inc.note}</p>
        ) : null}
        {!phaseKnown ? (
          <CaveatNotice tone="warning">
            {t('nis2.unknownPhase', { phase: inc.phase })}
          </CaveatNotice>
        ) : null}
        <p className="text-xs text-muted-foreground">
          {t('nis2.classifiedBy', {
            actor: inc.classified_by,
            at: inc.classified_at,
          })}
        </p>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs text-muted-foreground">
            {t('nis2.impactHash')}
          </span>
          <HashChip hash={inc.doc_sha256} />
        </div>
        <DisclaimerNote text={inc.disclaimer} />
      </div>
      <div className="flex shrink-0 flex-wrap gap-2">
        <Button variant="ghost" size="sm" onClick={onInspect}>
          <CalendarClock />
          {t('nis2.deadlines')}
        </Button>
        <Button
          variant="ghost"
          size="sm"
          onClick={onExport}
          disabled={exporting}
        >
          <FileJson />
          {t('nis2.export')}
        </Button>
        {canAdmin && canAdvance ? (
          <Button variant="secondary" size="sm" onClick={onAdvance}>
            {t('nis2.advance')}
          </Button>
        ) : null}
        {canAdmin ? (
          <Button variant="ghost" size="sm" onClick={onDelete}>
            <Trash2 />
            {t('nis2.delete')}
          </Button>
        ) : null}
      </div>
    </div>
  )
}

// --- classify: the write -----------------------------------------------------

function ClassifyDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [reference, setReference] = useState('')
  const [findingId, setFindingId] = useState('')
  const [impact, setImpact] = useState('')
  /** Set when the engine answered 501: the classifier lives in the enterprise
   *  add-on and is not linked. Kept in state so the dialog can SAY that where the
   *  operator is standing, instead of a red toast that reads like a fault. */
  const [seam, setSeam] = useState(false)

  // El reporte vive en un solo sitio (use-privileged-mutation.ts:25-32).
  const report = useFailedActionReporter()
  const reset = () => {
    setReference('')
    setFindingId('')
    setImpact('')
    setSeam(false)
  }

  // NOT usePrivilegedMutation, and the reason is the whole point of this dialog.
  // That hook treats every non-403 rejection as a failure and raises the generic
  // red error toast (use-privileged-mutation.ts:62-77). A 501 here is not a
  // failure — it is the open-core boundary — so routing it through the shared hook
  // would put "Something went wrong" on screen next to the calm explanation, which
  // is exactly the mixed message this view exists to avoid. The 403 branch is kept
  // by hand so nothing is lost by not using the hook.
  const classify = useMutation({
    mutationFn: () =>
      complianceApi.classifyNis2Incident(
        reference.trim(),
        impact,
        findingId.trim() || undefined,
      ),
    onSuccess: () => {
      toast.success(t('compliance:nis2.dialog.classified'), {
        description: t('compliance:nis2.dialog.classifiedHint'),
      })
      void qc.invalidateQueries({
        queryKey: complianceKeys.nis2All(activeTenant),
      })
      onOpenChange(false)
      reset()
    },
    onError: (e: unknown) => {
      // Boundary, by STATUS: explained in place, dialog stays open, no toast — the
      // operator's impact document is not thrown away by a build that cannot
      // classify it.
      if (isOpenCoreSeam(e)) {
        setSeam(true)
        return
      }
      // A 403 on this route is TWO different pieces of news, and the console must
      // not flatten them into one. The role case ("you lack compliance:nis2:admin")
      // is already prevented by the canAdmin gate above; the other is a COMMERCIAL
      // refusal — writeStoreError maps license.ErrAddonRequiresLicense to 403 with
      // its own stable message (helpers.go:64-76): *the "X" add-on is required for
      // Y; reading, verifying and exporting your data are unaffected*. Replacing
      // that with a generic "not authorized" tells an operator to go ask for a
      // permission nobody can grant them. Calm tone either way — a boundary is not
      // a fault — but the engine's words when it has them.
      // ⛔ ASEGURAMIENTO ANTES QUE ROL. `isForbidden` es SÓLO el status 403
      // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que esta rama
      // acusaba al operador de un permiso que SÍ tiene y le escondía la salida.
      //
      // DEFENSA EN PROFUNDIDAD: los emisores medidos son cuatro familias —21 `requireAAL3` en
      // `core/api`, dos escrituras en `modules/governance`, el `requireStepUp` propio de
      // `modules/deploy` y los retornos de `core/auth/webauthn.go`— y esta ruta no está en ninguna
      // hoy. Se arregla porque el defecto es de FORMA y sobrevive al día en que el gate llegue.
      if (e instanceof ApiError && e.isStepUpRequired) {
        report(e)
        return
      }
      if (e instanceof ApiError && e.isForbidden) {
        toast.warning(e.message || t('common:privileged.notAuthorizedToast'))
        return
      }
      toast.error(String((e as Error).message ?? e))
    },
  })

  const submit = () => {
    setSeam(false)
    classify.mutate()
  }

  const refTooLong = nis2ReferenceTooLong(reference.trim())
  const impactTooBig = utf8Bytes(impact) > MAX_IMPACT_BYTES
  const valid =
    reference.trim() !== '' &&
    !refTooLong &&
    impact.trim() !== '' &&
    !impactTooBig

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        onOpenChange(v)
        if (!v) reset()
      }}
    >
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('compliance:nis2.dialog.classifyTitle')}</DialogTitle>
          <DialogDescription>
            {t('compliance:nis2.dialog.classifyDescription')}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            submit()
          }}
        >
          {/* The platform cannot MEASURE the Art 23(3) criteria; the operator
              supplies the facts and the add-on applies the test. Said before the
              form, because it is what the form is asking for. */}
          <CaveatNotice tone="info">
            {t('compliance:nis2.dialog.operatorSuppliesHint')}
          </CaveatNotice>

          {seam ? (
            <CaveatNotice tone="warning" className="flex items-start gap-2">
              <SeamBadge label={t('compliance:nis2.dialog.seamBadge')} />
              <span>{t('compliance:nis2.dialog.seamHint')}</span>
            </CaveatNotice>
          ) : null}

          <Field
            label={t('compliance:nis2.dialog.reference')}
            description={t('compliance:nis2.dialog.referenceHint')}
          >
            {({ id }) => (
              <Input
                id={id}
                value={reference}
                onChange={(e) => setReference(e.target.value)}
                required
              />
            )}
          </Field>
          {refTooLong ? (
            <CaveatNotice tone="warning">
              {t('compliance:nis2.dialog.referenceTooLong', {
                max: NIS2_MAX_REFERENCE_RUNES,
              })}
            </CaveatNotice>
          ) : null}

          <Field
            label={t('compliance:nis2.dialog.findingId')}
            description={t('compliance:nis2.dialog.findingIdHint')}
          >
            {({ id }) => (
              <Input
                id={id}
                value={findingId}
                onChange={(e) => setFindingId(e.target.value)}
              />
            )}
          </Field>

          <Field
            label={t('compliance:nis2.dialog.impact')}
            description={t('compliance:nis2.dialog.impactHint')}
          >
            {({ id }) => (
              <Textarea
                id={id}
                value={impact}
                onChange={(e) => setImpact(e.target.value)}
                rows={10}
                className="font-mono text-xs"
                required
              />
            )}
          </Field>
          {impactTooBig ? (
            <CaveatNotice tone="warning">
              {t('compliance:nis2.dialog.impactTooLarge', {
                max: MAX_IMPACT_BYTES,
              })}
            </CaveatNotice>
          ) : null}
          {/* The bytes typed here are the bytes hashed into doc_sha256. Said so
              the operator knows the document is anchored, not re-serialized. */}
          <CaveatNotice tone="neutral">
            {t('compliance:nis2.dialog.verbatimHint')}
          </CaveatNotice>

          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!valid || classify.isPending}
            >
              {t('compliance:nis2.dialog.confirmClassify')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- advance the reporting phase ---------------------------------------------

function AdvancePhaseDialog({
  inc,
  open,
  onOpenChange,
}: {
  inc: Nis2Incident
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  // Only the phases AHEAD of the current one. The engine answers 409 to anything
  // else (nis2incident.go:295); offering the full vocabulary would turn its rule
  // into an error the operator discovers after committing.
  const options = nis2PhasesAfter(inc.phase)
  const [phase, setPhase] = useState<Nis2Phase | ''>(options[0] ?? '')
  const [note, setNote] = useState('')

  const advance = usePrivilegedMutation({
    mutationFn: () =>
      // EXACTLY the two keys the engine's decoder accepts: it runs
      // DisallowUnknownFields (helpers.go:97-116), so sending the row back would
      // be a 400, not a partial update.
      complianceApi.updateNis2Incident(inc.id, {
        phase: phase === '' ? undefined : phase,
        note: note.trim() || undefined,
      }),
    invalidateKeys: [complianceKeys.nis2All(activeTenant)],
    successMessage: t('compliance:nis2.dialog.advanced'),
    onDone: () => onOpenChange(false),
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t('compliance:nis2.dialog.advanceTitle', {
              reference: inc.reference,
            })}
          </DialogTitle>
          <DialogDescription>
            {t('compliance:nis2.dialog.advanceDescription')}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            advance.mutate()
          }}
        >
          <CaveatNotice tone="info">
            {t('compliance:nis2.dialog.forwardOnlyHint', {
              current: t(`compliance:nis2.phase.${inc.phase}`, {
                defaultValue: inc.phase,
              }),
            })}
          </CaveatNotice>
          <Field label={t('compliance:nis2.dialog.phase')}>
            <Select
              value={phase}
              onValueChange={(v) => setPhase(v as Nis2Phase)}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {options.map((p) => (
                  <SelectItem key={p} value={p}>
                    {t(`compliance:nis2.phase.${p}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('compliance:nis2.dialog.note')}
            description={t('compliance:nis2.dialog.noteHint')}
          >
            {({ id }) => (
              <Textarea
                id={id}
                value={note}
                onChange={(e) => setNote(e.target.value)}
                rows={2}
              />
            )}
          </Field>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={phase === '' || advance.isPending}
            >
              {t('compliance:nis2.dialog.confirmAdvance')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- delete: removes governed evidence ---------------------------------------

function DeleteIncidentDialog({
  inc,
  open,
  onOpenChange,
}: {
  inc: Nis2Incident
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()

  const remove = usePrivilegedMutation({
    mutationFn: () => complianceApi.deleteNis2Incident(inc.id),
    invalidateKeys: [complianceKeys.nis2All(activeTenant)],
    // ALLOWLIST: the engine STATES the outcome (`{"deleted":true}`,
    // nis2incident.go:403). A 2xx alone is not evidence a classification is gone,
    // and "gone" is the one thing this dialog must never claim wrongly.
    successMessage: (res) =>
      confirmedDeleted(res)
        ? t('compliance:nis2.dialog.deleted')
        : t('compliance:nis2.dialog.deleteUnconfirmed'),
    onDone: () => onOpenChange(false),
  })

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      tone="danger"
      confirmPhrase={DELETE_PHRASE}
      pending={remove.isPending}
      title={t('compliance:nis2.dialog.deleteTitle', {
        reference: inc.reference,
      })}
      description={t('compliance:nis2.dialog.deleteDescription')}
      confirmLabel={t('compliance:nis2.dialog.confirmDelete')}
      onConfirm={() => remove.mutate()}
    >
      <CaveatNotice tone="warning" className="flex items-start gap-2">
        <ShieldAlert className="mt-0.5 size-4 shrink-0" />
        <span>{t('compliance:nis2.dialog.deleteScope')}</span>
      </CaveatNotice>
    </ConfirmDialog>
  )
}

// --- deadlines + report drafts ------------------------------------------------

/** The Art 23(4) clock and the drafted reports. They are NOT on the list rows:
 *  the list builds its DTOs with includeBody=false (nis2incident.go:224), so this
 *  panel refetches the single classification to get `deadlines`, `report_drafts`
 *  and `basis`. Rendered as the engine's own values — never recomputed, never
 *  reformatted into a date the operator might read as authoritative. */
function Nis2DetailDialog({
  inc,
  onClose,
}: {
  inc: Nis2Incident
  onClose: () => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const detailQ = useQuery({
    queryKey: complianceKeys.nis2Incident(activeTenant, inc.id),
    queryFn: () => complianceApi.nis2Incident(inc.id),
  })

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {t('compliance:nis2.detailTitle', { reference: inc.reference })}
          </DialogTitle>
          <DialogDescription>
            {t('compliance:nis2.detailDescription')}
          </DialogDescription>
        </DialogHeader>
        <AsyncSection query={detailQ} skeletonHeight={180}>
          {(full) => (
            <div className="flex max-h-[60vh] flex-col gap-3 overflow-y-auto">
              <CaveatNotice tone="warning">
                {t('compliance:nis2.deadlinesProvisional')}
              </CaveatNotice>
              <KeyValueBlock
                title={t('compliance:nis2.deadlinesTitle')}
                empty={t('compliance:nis2.deadlinesEmpty')}
                value={full.deadlines}
              />
              <KeyValueBlock
                title={t('compliance:nis2.draftsTitle')}
                empty={t('compliance:nis2.draftsEmpty')}
                value={full.report_drafts}
              />
              {full.basis && full.basis.length > 0 ? (
                <div className="flex flex-col gap-1">
                  <p className="text-xs font-medium">
                    {t('compliance:nis2.basisTitle')}
                  </p>
                  <ul className="flex flex-col gap-1">
                    {full.basis.map((b, i) => (
                      <li
                        key={`${b.provision ?? ''}-${i}`}
                        className="text-xs text-muted-foreground"
                      >
                        {b.provision ?? ''}
                        {b.source_url ? ` · ${b.source_url}` : ''}
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}
              <DisclaimerNote text={full.disclaimer} />
            </div>
          )}
        </AsyncSection>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            {t('common:actions.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** A free-shape map the ADD-ON produced. The console does not know its keys and
 *  does not invent a schema for them: it prints what came, verbatim, so a field
 *  the packager adds tomorrow appears instead of being silently dropped. */
function KeyValueBlock({
  title,
  empty,
  value,
}: {
  title: string
  empty: string
  value?: Record<string, unknown>
}) {
  const entries = Object.entries(value ?? {})
  return (
    <div className="flex flex-col gap-1">
      <p className="text-xs font-medium">{title}</p>
      {entries.length === 0 ? (
        <p className="text-xs text-muted-foreground">{empty}</p>
      ) : (
        <dl className="flex flex-col gap-1">
          {entries.map(([k, v]) => (
            <div key={k} className="flex flex-wrap gap-2 text-xs">
              <dt className="text-muted-foreground">{k}</dt>
              <dd className="font-mono break-all">
                {typeof v === 'string' ? v : JSON.stringify(v)}
              </dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  )
}
