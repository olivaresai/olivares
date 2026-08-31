// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// Regulatory operations in the console — E3 (DORA register + incidents, OSCAL
// profiles) and E4 (compliance-depth packs: US state law, sector overlays, CCM)
// with the TWELVE WRITES wired.
//
// WHAT CHANGED, AND WHY IT WAS A FALSE CLAIM RATHER THAN A GAP. This tab's
// own banner promised, in seven languages, that "reading, exporting and deleting
// these artefacts works in this build" — and the twelve writes of api.ts had NOT
// ONE CALLER anywhere in web/src (measured on the base branch: `grep -c` over the
// twelve names = 0, the only two hits being assertions in nis2.test.tsx). They are
// SEVEN non-delete writes and FIVE deletes, not six and six: the CCM plane
// contributes two writes (snapshot, drift) and no delete. The engine has served
// every one of these routes since
// compliance.go:480-534. The copy was telling the truth about the ENGINE and the
// console was the part that did not exist, so the fix is the surface, never the
// sentence: deleting a function is not how a promise gets kept.
//
// THE OPEN-CORE SPLIT, MEASURED. Every GENERATOR answers 501 unless the
// enterprise add-on is linked — regpackage.go:258 (DORA register),
// doraincident.go:94 (incident classification), oscalprofile.go:246 (OSCAL
// ingestion), depthhandlers.go:242/521/796/976 (depth + CCM). Every READ, EXPORT
// and DELETE is open-core and works against whatever the add-on persisted. So a
// 501 is drawn as the boundary it is — detected by STATUS, never by matching
// prose — because a console that shouts "request failed" at a deliberate
// boundary teaches operators to distrust true errors. And a 403 here is TWO
// pieces of news: the role case (already hidden by the `can()` gates) and a
// COMMERCIAL refusal carrying the engine's own words (helpers.go:64-76), which
// Measured on this very module. Replacing that with "not authorized" sends
// an operator to ask for a permission nobody can grant.
//
// THE ONE RULE THAT SHAPES EVERY DIALOG BELOW: five of these writes take the
// operator's document as RAW BYTES and their side inputs in the QUERY STRING,
// because the engine hashes those exact bytes into `doc_sha256` before the add-on
// parses them. The client carries that in one place (`postDocument`, api.ts); the
// dialogs carry the operator-facing half — the document goes verbatim, and it is
// the document that gets anchored.
import { useState } from 'react'
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
  type QueryKey,
} from '@tanstack/react-query'
import {
  AlertTriangle,
  FileJson,
  Landmark,
  Layers,
  RefreshCw,
  ShieldCheck,
  Siren,
  Trash2,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
  confirmedRemoval,
  isOpenCoreSeam,
} from './api'
import {
  COMPLIANCE_MAX_DOCUMENT_BYTES,
  documentTooLarge,
  refTooLong,
  type CcmSnapshot,
  type AimsPack,
  type DepthPack,
  type DepthPackBase,
  type FedrampKsiPack,
  type DoraIncident,
  type DoraRegister,
  type OscalProfile,
} from './types'

/** Deleting any artefact on this tab removes governed evidence an auditor may
 *  already have been shown, so every destructive verb gets the typed-phrase
 *  guard rather than a lone button. */
const DELETE_PHRASE = 'DELETE'

/** Download the server's exact export bytes (never recomputed client-side). */
function downloadRaw(res: {
  filename: string
  content_type: string
  text: string
}) {
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
}

function useExport() {
  const [busy, setBusy] = useState<string | null>(null)
  const run = (
    id: string,
    fn: () => Promise<{ filename: string; content_type: string; text: string }>,
    doneMsg: string,
  ) => {
    setBusy(id)
    fn()
      .then((res) => {
        downloadRaw(res)
        toast.success(doneMsg)
      })
      .catch((e: unknown) => toast.error(String((e as Error).message ?? e)))
      .finally(() => setBusy(null))
  }
  return { busy, run }
}

export function RegOpsTab({
  canDoraRead,
  canDoraAdmin,
  canOscalRead,
  canOscalAdmin,
  canDepthRead,
  canDepthAdmin,
  canAimsRead,
  canAimsAdmin,
  canCcmRead,
  canCcmAdmin,
}: {
  canDoraRead: boolean
  canDoraAdmin: boolean
  canOscalRead: boolean
  canOscalAdmin: boolean
  canDepthRead: boolean
  canDepthAdmin: boolean
  canAimsRead: boolean
  canAimsAdmin: boolean
  canCcmRead: boolean
  canCcmAdmin: boolean
}) {
  const { t } = useTranslation('compliance')
  return (
    <>
      {/* The generators are an add-on capability. Said once, up front, so each
          panel below does not have to repeat it. */}
      <CaveatNotice tone="info">{t('regops.seamHint')}</CaveatNotice>
      {canDoraRead ? <DoraRegisterPanel canAdmin={canDoraAdmin} /> : null}
      {canDoraRead ? <DoraIncidentPanel canAdmin={canDoraAdmin} /> : null}
      {canOscalRead ? <OscalProfilePanel canAdmin={canOscalAdmin} /> : null}
      {/* El panel se monta si el usuario puede leer AL MENOS UNA de sus familias, y
          dentro se filtra por familia: `aims` no se sirve bajo `depth`. */}
      {canDepthRead || canAimsRead ? (
        <DepthPackPanel
          permisos={{
            depth: { read: canDepthRead, admin: canDepthAdmin },
            aims: { read: canAimsRead, admin: canAimsAdmin },
          }}
        />
      ) : null}
      {canCcmRead ? <CcmPanel canAdmin={canCcmAdmin} /> : null}
      {!canDoraRead &&
      !canOscalRead &&
      !canDepthRead &&
      !canAimsRead &&
      !canCcmRead ? (
        <SectionCard title={t('regops.title')}>
          <EmptyState icon={<Landmark />} title={t('regops.noAccess')} />
        </SectionCard>
      ) : null}
    </>
  )
}

// --- the shared write surface -------------------------------------------------

/** The dialog every document-generating write on this tab uses.
 *
 *  It exists because five writes share one contract and one hazard: the body is
 *  the OPERATOR'S DOCUMENT, verbatim, and the engine hashes exactly those bytes
 *  into `doc_sha256` before anything parses them. Five hand-rolled dialogs would
 *  be five chances to re-introduce the double-encoding defect this campaign has
 *  already paid for twice, and five places where the 501 boundary could turn back
 *  into a red "request failed".
 *
 *  The caller owns its own side inputs (rendered through `children`, sent as
 *  QUERY params by its `mutationFn`); this component owns the document, the
 *  engine's size rule, the seam and the two failure tones.
 *
 *  NOT usePrivilegedMutation, and the reason is the same one the NIS 2 sibling
 *  states: that hook treats every non-403 rejection as a failure and raises the
 *  generic red error toast. A 501 here is not a failure, so routing it through
 *  the shared hook would print "Something went wrong" beside the calm
 *  explanation. The 403 branch is kept by hand so nothing is lost by not using
 *  it. */
function DocumentWriteDialog({
  open,
  onOpenChange,
  title,
  description,
  documentLabel,
  documentHint,
  confirmLabel,
  successMessage,
  successHint,
  invalidateKeys,
  mutationFn,
  onReset,
  children,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  title: string
  description: string
  documentLabel: string
  documentHint: string
  confirmLabel: string
  successMessage: string
  successHint?: string
  invalidateKeys: QueryKey[]
  mutationFn: (document: string) => Promise<{ id?: string }>
  /** Clear the caller's own side inputs when the dialog closes or succeeds. */
  onReset?: () => void
  /** The caller's side inputs — rendered above the document. */
  children?: React.ReactNode
}) {
  // El reporte vive en un solo sitio (use-privileged-mutation.ts:25-32).
  const report = useFailedActionReporter()
  const { t } = useTranslation(['compliance', 'common'])
  const qc = useQueryClient()
  const [document, setDocument] = useState('')
  /** Set when the engine answered 501: the generator lives in the enterprise
   *  add-on and is not linked. Kept in state so the dialog SAYS that where the
   *  operator is standing, instead of a red toast that reads like a fault — and
   *  so the document they just pasted is not thrown away by a build that cannot
   *  process it. */
  const [seam, setSeam] = useState(false)

  const reset = () => {
    setDocument('')
    setSeam(false)
    onReset?.()
  }

  const write = useMutation({
    mutationFn: () => mutationFn(document),
    onSuccess: (created) => {
      // ALLOWLIST, not "the request did not throw". These routes answer 201 with
      // the persisted artefact (regpackage.go:348, doraincident.go:179,
      // oscalprofile.go:334, depthhandlers.go:354/631). An answer carrying no id
      // is not an artefact, and reporting one anyway would tell an operator that
      // a regulatory document exists when nothing was persisted.
      if (!created?.id) {
        toast.warning(t('compliance:regops.dialog.unconfirmed'))
        return
      }
      toast.success(successMessage, {
        description: successHint,
      })
      invalidateKeys.forEach((queryKey) => {
        void qc.invalidateQueries({ queryKey })
      })
      onOpenChange(false)
      reset()
    },
    onError: (e: unknown) => {
      // Boundary, by STATUS: explained in place, dialog stays open, no toast.
      if (isOpenCoreSeam(e)) {
        setSeam(true)
        return
      }
      // A 403 can be a COMMERCIAL refusal carrying the engine's own stable
      // message (helpers.go:64-76) — measured on this module by — not a
      // permission the operator could be granted. Calm tone either way; the
      // engine's words when it has them.
      // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
      // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que esta rama
      // acusaba al operador de un permiso que SÍ tiene y le escondía la salida. Defensa en
      // profundidad: esta ruta no está en ninguna de las cuatro familias de emisores medidas.
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

  const tooBig = documentTooLarge(document)
  const valid = document.trim() !== '' && !tooBig

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
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            setSeam(false)
            write.mutate()
          }}
        >
          {/* The platform does not author these documents; the operator supplies
              the facts and the add-on structures them. Said before the form,
              because it is what the form is asking for. */}
          <CaveatNotice tone="info">
            {t('compliance:regops.dialog.operatorSuppliesHint')}
          </CaveatNotice>

          {seam ? (
            <CaveatNotice tone="warning" className="flex items-start gap-2">
              <SeamBadge label={t('compliance:regops.dialog.seamBadge')} />
              <span>{t('compliance:regops.dialog.seamHint')}</span>
            </CaveatNotice>
          ) : null}

          {children}

          <Field label={documentLabel} description={documentHint}>
            {({ id }) => (
              <Textarea
                id={id}
                value={document}
                onChange={(e) => setDocument(e.target.value)}
                rows={10}
                className="font-mono text-xs"
                required
              />
            )}
          </Field>
          {tooBig ? (
            <CaveatNotice tone="warning">
              {t('compliance:regops.dialog.documentTooLarge', {
                max: COMPLIANCE_MAX_DOCUMENT_BYTES,
              })}
            </CaveatNotice>
          ) : null}
          {/* The bytes typed here are the bytes hashed into doc_sha256. Said so
              the operator knows the document is anchored, not re-serialized. */}
          <CaveatNotice tone="neutral">
            {t('compliance:regops.dialog.verbatimHint')}
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
              disabled={!valid || write.isPending}
            >
              {confirmLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** The delete every artefact on this tab uses.
 *
 *  ⚠ IT DOES NOT USE `confirmedDeleted`, AND THAT IS THE MEASUREMENT, NOT AN
 *  OVERSIGHT. The NIS 2 sibling's route STATES its outcome in a body
 *  (`{"deleted":true}` with 200, nis2incident.go:403 — the line the sibling's own
 *  comments still cite as :383, which is now its transaction setup) and its helper
 *  demands exactly that. THESE five routes answer a bodyless **204**
 *  (regpackage.go:476, doraincident.go:321, oscalprofile.go:415,
 *  depthhandlers.go:503, :779), so `confirmedDeleted` would report every
 *  successful deletion as unconfirmed. Copying the sibling's helper because the
 *  action has the same name is precisely the class of error this campaign keeps
 *  paying for — so the allowlist here is on the status the engine actually
 *  sends, via `confirmedRemoval`. */
function DeleteArtefactDialog({
  open,
  onOpenChange,
  title,
  description,
  scopeWarning,
  confirmLabel,
  deletedMessage,
  unconfirmedMessage,
  invalidateKeys,
  mutationFn,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  title: string
  description: string
  scopeWarning: string
  confirmLabel: string
  deletedMessage: string
  unconfirmedMessage: string
  invalidateKeys: QueryKey[]
  mutationFn: () => Promise<{ status: number; data: unknown }>
}) {
  const { t } = useTranslation(['compliance', 'common'])

  const remove = usePrivilegedMutation({
    mutationFn,
    invalidateKeys,
    successMessage: (res) =>
      confirmedRemoval(res) ? deletedMessage : unconfirmedMessage,
    onDone: () => onOpenChange(false),
  })

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      tone="danger"
      confirmPhrase={DELETE_PHRASE}
      pending={remove.isPending}
      title={title}
      description={description}
      confirmLabel={confirmLabel}
      onConfirm={() => remove.mutate()}
    >
      <CaveatNotice tone="warning" className="flex items-start gap-2">
        <AlertTriangle className="mt-0.5 size-4 shrink-0" />
        <span>{scopeWarning}</span>
      </CaveatNotice>
      <p className="text-xs text-muted-foreground">
        {t('compliance:regops.dialog.deleteAuditNote')}
      </p>
    </ConfirmDialog>
  )
}

/** The framework catalog as a CHECKLIST, never a text box.
 *
 *  The engine SILENTLY SKIPS a framework id it does not know
 *  (`gatherMultiAssessments`, depthhandlers.go:216-220: `if !ok { continue }`).
 *  A typed id is therefore worse than a rejection — the snapshot is taken over
 *  fewer frameworks than the operator asked for, succeeds with 201, and nothing
 *  anywhere says so. Offering the catalog is the only shape that cannot produce
 *  that outcome. (The same reasoning already governs the data-class registry on
 *  the holds dialog, api.ts (9).) */
function FrameworkChecklist({
  selected,
  onToggle,
}: {
  selected: string[]
  onToggle: (id: string) => void
}) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const q = useQuery({
    queryKey: complianceKeys.frameworks(activeTenant),
    queryFn: () => complianceApi.frameworks(),
  })

  return (
    <Field
      label={t('regops.ccm.frameworks')}
      description={t('regops.ccm.frameworksHint')}
    >
      <AsyncSection query={q} skeletonHeight={120}>
        {(list) => (
          <div className="grid max-h-48 gap-2 overflow-y-auto sm:grid-cols-2">
            {list.items.map((fw) => (
              <label key={fw.id} className="flex items-center gap-2 text-xs">
                <Checkbox
                  checked={selected.includes(fw.id)}
                  onCheckedChange={() => onToggle(fw.id)}
                />
                <span>{fw.name}</span>
              </label>
            ))}
          </div>
        )}
      </AsyncSection>
    </Field>
  )
}

// --- DORA Register of Information --------------------------------------------

function DoraRegisterPanel({ canAdmin }: { canAdmin: boolean }) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const { busy, run } = useExport()
  const [generating, setGenerating] = useState(false)
  const [deleting, setDeleting] = useState<DoraRegister | null>(null)
  const q = useQuery({
    queryKey: complianceKeys.doraRegisters(activeTenant),
    queryFn: () => complianceApi.doraRegisters(),
  })

  return (
    <SectionCard
      title={t('regops.dora.title')}
      description={t('regops.dora.description')}
      actions={
        canAdmin ? (
          <div className="flex items-center gap-2">
            <SeamBadge label={t('regops.addonRequired')} />
            <Button
              variant="primary"
              size="sm"
              onClick={() => setGenerating(true)}
            >
              <Landmark />
              {t('regops.dora.generate')}
            </Button>
          </div>
        ) : null
      }
    >
      <SelfAuditNotice className="mb-3" />
      <AsyncSection query={q} skeletonHeight={180}>
        {(list) =>
          (list.items ?? []).length === 0 ? (
            <EmptyState
              icon={<Landmark />}
              title={t('regops.dora.empty')}
              description={t('regops.dora.emptyHint')}
            />
          ) : (
            <div className="flex flex-col gap-2">
              {list.items.map((reg) => (
                <RegisterRow
                  key={reg.id}
                  reg={reg}
                  canAdmin={canAdmin}
                  busy={busy === reg.id}
                  onExport={() =>
                    run(
                      reg.id,
                      () => complianceApi.exportDoraRegister(reg.id),
                      t('regops.exported'),
                    )
                  }
                  onDelete={() => setDeleting(reg)}
                />
              ))}
            </div>
          )
        }
      </AsyncSection>

      {canAdmin ? (
        <GenerateRegisterDialog
          open={generating}
          onOpenChange={setGenerating}
        />
      ) : null}

      {canAdmin && deleting ? (
        <DeleteArtefactDialog
          open={deleting !== null}
          onOpenChange={(v) => {
            if (!v) setDeleting(null)
          }}
          title={t('regops.dora.deleteTitle', {
            entity: deleting.entity_name ?? deleting.entity_lei,
          })}
          description={t('regops.dora.deleteDescription')}
          scopeWarning={t('regops.dora.deleteScope')}
          confirmLabel={t('regops.dora.confirmDelete')}
          deletedMessage={t('regops.dora.deleted')}
          unconfirmedMessage={t('regops.deleteUnconfirmed')}
          invalidateKeys={[complianceKeys.doraRegisters(activeTenant)]}
          mutationFn={() => complianceApi.deleteDoraRegister(deleting.id)}
        />
      ) : null}
    </SectionCard>
  )
}

function GenerateRegisterDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [referenceDate, setReferenceDate] = useState('')

  return (
    <DocumentWriteDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('regops.dora.generateTitle')}
      description={t('regops.dora.generateDescription')}
      documentLabel={t('regops.dora.document')}
      documentHint={t('regops.dora.documentHint')}
      confirmLabel={t('regops.dora.confirmGenerate')}
      successMessage={t('regops.dora.generated')}
      successHint={t('regops.dora.generatedHint')}
      invalidateKeys={[complianceKeys.doraRegisters(activeTenant)]}
      onReset={() => setReferenceDate('')}
      mutationFn={(document) =>
        complianceApi.generateDoraRegister(document, referenceDate)
      }
    >
      {/* REPLACE-ON-REGENERATE: the register's identity is the maintaining
          entity's LEI, which the ENGINE reads out of the document
          (regpackage.go:288) — the console does not ask for it and must not
          invent it. Regenerating for the same LEI overwrites the existing
          register (`:323-328`), which is why the operator is told here. */}
      <CaveatNotice tone="warning">{t('regops.dora.replaceHint')}</CaveatNotice>
      <Field
        label={t('regops.dora.referenceDate')}
        description={t('regops.dora.referenceDateHint')}
      >
        {({ id }) => (
          <Input
            id={id}
            value={referenceDate}
            onChange={(e) => setReferenceDate(e.target.value)}
            placeholder="2026-12-31"
          />
        )}
      </Field>
    </DocumentWriteDialog>
  )
}

function RegisterRow({
  reg,
  canAdmin,
  busy,
  onExport,
  onDelete,
}: {
  reg: DoraRegister
  canAdmin: boolean
  busy: boolean
  onExport: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation('compliance')
  const errors = reg.error_count
  return (
    <div className="flex flex-wrap items-start justify-between gap-3 rounded-md border border-border p-3">
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={errors > 0 ? 'danger' : 'success'}>
            {errors > 0
              ? t('regops.dora.errors', { count: errors })
              : t('regops.dora.clean')}
          </Badge>
          <span className="font-medium">
            {reg.entity_name ?? reg.entity_lei}
          </span>
          <Badge variant="outline">{reg.regulation}</Badge>
        </div>
        <p className="text-xs text-muted-foreground">
          {t('regops.dora.lei', { lei: reg.entity_lei })}
          {reg.reference_date ? ` · ${reg.reference_date}` : ''}
        </p>
        <p className="text-xs text-muted-foreground">
          {t('regops.generatedBy', {
            actor: reg.generated_by,
            at: reg.generated_at,
          })}
        </p>
        <HashChip hash={reg.doc_sha256} label={t('regops.docHash')} />
        {/* The register is explicitly a DRAFT a competent person must review. */}
        <DisclaimerNote text={reg.disclaimer} />
      </div>
      <div className="flex shrink-0 flex-wrap gap-2">
        <Button variant="ghost" size="sm" onClick={onExport} disabled={busy}>
          <FileJson />
          {t('regops.export')}
        </Button>
        {canAdmin ? (
          <Button variant="ghost" size="sm" onClick={onDelete}>
            <Trash2 />
            {t('regops.delete')}
          </Button>
        ) : null}
      </div>
    </div>
  )
}

// --- DORA major-incident classification --------------------------------------

function DoraIncidentPanel({ canAdmin }: { canAdmin: boolean }) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const { busy, run } = useExport()
  const [classifying, setClassifying] = useState(false)
  const [deleting, setDeleting] = useState<DoraIncident | null>(null)
  const q = useQuery({
    queryKey: complianceKeys.doraIncidents(activeTenant),
    queryFn: () => complianceApi.doraIncidents(),
  })

  return (
    <SectionCard
      title={t('regops.incidents.title')}
      description={t('regops.incidents.description')}
      actions={
        canAdmin ? (
          <Button
            variant="primary"
            size="sm"
            onClick={() => setClassifying(true)}
          >
            <Siren />
            {t('regops.incidents.classify')}
          </Button>
        ) : null
      }
    >
      <AsyncSection query={q} skeletonHeight={160}>
        {(list) =>
          (list.items ?? []).length === 0 ? (
            <EmptyState
              icon={<AlertTriangle />}
              title={t('regops.incidents.empty')}
              description={t('regops.incidents.emptyHint')}
            />
          ) : (
            <div className="flex flex-col gap-2">
              {list.items.map((inc) => (
                <IncidentRow
                  key={inc.id}
                  inc={inc}
                  canAdmin={canAdmin}
                  busy={busy === inc.id}
                  onExport={() =>
                    run(
                      inc.id,
                      () => complianceApi.exportIncidentReport(inc.id),
                      t('regops.exported'),
                    )
                  }
                  onDelete={() => setDeleting(inc)}
                />
              ))}
            </div>
          )
        }
      </AsyncSection>

      {canAdmin ? (
        <ClassifyIncidentDialog
          open={classifying}
          onOpenChange={setClassifying}
        />
      ) : null}

      {canAdmin && deleting ? (
        <DeleteArtefactDialog
          open={deleting !== null}
          onOpenChange={(v) => {
            if (!v) setDeleting(null)
          }}
          title={t('regops.incidents.deleteTitle', {
            reference: deleting.reference,
          })}
          description={t('regops.incidents.deleteDescription')}
          scopeWarning={t('regops.incidents.deleteScope')}
          confirmLabel={t('regops.incidents.confirmDelete')}
          deletedMessage={t('regops.incidents.deleted')}
          unconfirmedMessage={t('regops.deleteUnconfirmed')}
          invalidateKeys={[complianceKeys.doraIncidents(activeTenant)]}
          mutationFn={() => complianceApi.deleteIncident(deleting.id)}
        />
      ) : null}
    </SectionCard>
  )
}

function ClassifyIncidentDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [reference, setReference] = useState('')
  const [findingId, setFindingId] = useState('')

  // The engine REJECTS an over-length reference rather than truncating it
  // (doraincident.go:104-107): a clamped reference would persist as a DIFFERENT
  // incident. Shown while typing, not after the impact document is lost to a 400.
  const referenceTooLong = refTooLong(reference.trim())

  return (
    <DocumentWriteDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('regops.incidents.classifyTitle')}
      description={t('regops.incidents.classifyDescription')}
      documentLabel={t('regops.incidents.impact')}
      documentHint={t('regops.incidents.impactHint')}
      confirmLabel={t('regops.incidents.confirmClassify')}
      successMessage={t('regops.incidents.classified')}
      successHint={t('regops.incidents.classifiedHint')}
      invalidateKeys={[complianceKeys.doraIncidents(activeTenant)]}
      onReset={() => {
        setReference('')
        setFindingId('')
      }}
      mutationFn={(document) =>
        complianceApi.classifyIncident(reference.trim(), document, findingId)
      }
    >
      {/* Re-classifying the same reference UPDATES the row rather than adding one
          (doraincident.go:154-162) — the engine's own answer to a repeat, and the
          operator should know before choosing a reference. */}
      <CaveatNotice tone="warning">
        {t('regops.incidents.replaceHint')}
      </CaveatNotice>
      <Field
        label={t('regops.incidents.reference')}
        description={t('regops.incidents.referenceHint')}
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
      {referenceTooLong ? (
        <CaveatNotice tone="warning">
          {t('regops.incidents.referenceTooLong')}
        </CaveatNotice>
      ) : null}
      <Field
        label={t('regops.incidents.findingId')}
        description={t('regops.incidents.findingIdHint')}
      >
        {({ id }) => (
          <Input
            id={id}
            value={findingId}
            onChange={(e) => setFindingId(e.target.value)}
          />
        )}
      </Field>
    </DocumentWriteDialog>
  )
}

function IncidentRow({
  inc,
  canAdmin,
  busy,
  onExport,
  onDelete,
}: {
  inc: DoraIncident
  canAdmin: boolean
  busy: boolean
  onExport: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation('compliance')
  return (
    <div className="flex flex-wrap items-start justify-between gap-3 rounded-md border border-border p-3">
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          {/* "major" is the reporting trigger under DORA Art. 19 — the single
              fact an operator is looking for on this row. */}
          <Badge variant={inc.major ? 'danger' : 'neutral'}>
            {inc.major
              ? t('regops.incidents.major')
              : t('regops.incidents.notMajor')}
          </Badge>
          {inc.provisional ? (
            <Badge variant="warning">{t('regops.incidents.provisional')}</Badge>
          ) : null}
          {inc.critical_services ? (
            <Badge variant="warning">
              {t('regops.incidents.criticalServices')}
            </Badge>
          ) : null}
          <span className="font-medium">{inc.reference}</span>
        </div>
        {inc.rationale ? <p className="text-xs">{inc.rationale}</p> : null}
        {inc.criteria_met && inc.criteria_met.length > 0 ? (
          <p className="text-xs text-muted-foreground">
            {t('regops.incidents.criteria', {
              list: inc.criteria_met.join(', '),
            })}
          </p>
        ) : null}
        <p className="text-xs text-muted-foreground">
          {t('regops.classifiedBy', {
            actor: inc.classified_by,
            at: inc.classified_at,
          })}
        </p>
        <HashChip hash={inc.doc_sha256} label={t('regops.docHash')} />
        <DisclaimerNote text={inc.disclaimer} />
      </div>
      <div className="flex shrink-0 flex-wrap gap-2">
        <Button variant="ghost" size="sm" onClick={onExport} disabled={busy}>
          <FileJson />
          {t('regops.incidents.report')}
        </Button>
        {canAdmin ? (
          <Button variant="ghost" size="sm" onClick={onDelete}>
            <Trash2 />
            {t('regops.delete')}
          </Button>
        ) : null}
      </div>
    </div>
  )
}

// --- OSCAL profiles ----------------------------------------------------------

function OscalProfilePanel({ canAdmin }: { canAdmin: boolean }) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [registering, setRegistering] = useState(false)
  const [deleting, setDeleting] = useState<OscalProfile | null>(null)
  const q = useQuery({
    queryKey: complianceKeys.oscalProfiles(activeTenant),
    queryFn: () => complianceApi.oscalProfiles(),
  })

  return (
    <SectionCard
      title={t('regops.oscal.title')}
      description={t('regops.oscal.description')}
      actions={
        canAdmin ? (
          <Button
            variant="primary"
            size="sm"
            onClick={() => setRegistering(true)}
          >
            <ShieldCheck />
            {t('regops.oscal.register')}
          </Button>
        ) : null
      }
    >
      <AsyncSection query={q} skeletonHeight={160}>
        {(list) =>
          (list.items ?? []).length === 0 ? (
            <EmptyState
              icon={<ShieldCheck />}
              title={t('regops.oscal.empty')}
              description={t('regops.oscal.emptyHint')}
            />
          ) : (
            <div className="flex flex-col gap-2">
              {list.items.map((p) => (
                <ProfileRow
                  key={p.id}
                  profile={p}
                  canAdmin={canAdmin}
                  onDelete={() => setDeleting(p)}
                />
              ))}
            </div>
          )
        }
      </AsyncSection>

      {canAdmin ? (
        <RegisterProfileDialog
          open={registering}
          onOpenChange={setRegistering}
        />
      ) : null}

      {canAdmin && deleting ? (
        <DeleteArtefactDialog
          open={deleting !== null}
          onOpenChange={(v) => {
            if (!v) setDeleting(null)
          }}
          title={t('regops.oscal.deleteTitle', {
            title: deleting.title ?? deleting.framework,
          })}
          description={t('regops.oscal.deleteDescription')}
          scopeWarning={t('regops.oscal.deleteScope')}
          confirmLabel={t('regops.oscal.confirmDelete')}
          deletedMessage={t('regops.oscal.deleted')}
          unconfirmedMessage={t('regops.deleteUnconfirmed')}
          invalidateKeys={[complianceKeys.oscalProfiles(activeTenant)]}
          mutationFn={() => complianceApi.deleteOscalProfile(deleting.id)}
        />
      ) : null}
    </SectionCard>
  )
}

function RegisterProfileDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [framework, setFramework] = useState('')
  const [scopeNote, setScopeNote] = useState('')
  const frameworksQ = useQuery({
    queryKey: complianceKeys.frameworks(activeTenant),
    queryFn: () => complianceApi.frameworks(),
  })

  return (
    <DocumentWriteDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('regops.oscal.registerTitle')}
      description={t('regops.oscal.registerDescription')}
      documentLabel={t('regops.oscal.document')}
      documentHint={t('regops.oscal.documentHint')}
      confirmLabel={t('regops.oscal.confirmRegister')}
      successMessage={t('regops.oscal.registered')}
      successHint={t('regops.oscal.registeredHint')}
      invalidateKeys={[complianceKeys.oscalProfiles(activeTenant)]}
      onReset={() => {
        setFramework('')
        setScopeNote('')
      }}
      mutationFn={(document) =>
        complianceApi.registerOscalProfile(document, { framework, scopeNote })
      }
    >
      {/* The hint only HELPS the resolver; the framework the document actually
          selects is the engine's answer (oscalprofile.go:256), and a document that
          resolves to no known framework is refused outright (`:270-274`) rather
          than filed under the hint. Said so a wrong hint is not read as a
          misfiling risk. */}
      <CaveatNotice tone="info">{t('regops.oscal.hintOnlyHint')}</CaveatNotice>
      <Field
        label={t('regops.oscal.framework')}
        description={t('regops.oscal.frameworkHint')}
      >
        {/* The render form, not a bare child: `aria-labelledby` is what names a
            BUTTON-based control. `<label htmlFor>` does not — the AccName
            algorithm ignores the for-association for a combobox trigger
            (field.tsx:21-25), so the select would reach a screen reader unnamed. */}
        {({ 'aria-labelledby': labelledBy }) => (
          <Select
            value={framework}
            onValueChange={(v) => setFramework(v === '__none__' ? '' : v)}
          >
            <SelectTrigger aria-labelledby={labelledBy}>
              <SelectValue placeholder={t('regops.oscal.frameworkAuto')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__none__">
                {t('regops.oscal.frameworkAuto')}
              </SelectItem>
              {(frameworksQ.data?.items ?? []).map((fw) => (
                <SelectItem key={fw.id} value={fw.id}>
                  {fw.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </Field>
      <Field
        label={t('regops.scopeNote')}
        description={t('regops.scopeNoteHint')}
      >
        {({ id }) => (
          <Input
            id={id}
            value={scopeNote}
            onChange={(e) => setScopeNote(e.target.value)}
          />
        )}
      </Field>
    </DocumentWriteDialog>
  )
}

function ProfileRow({
  profile,
  canAdmin,
  onDelete,
}: {
  profile: OscalProfile
  canAdmin: boolean
  onDelete: () => void
}) {
  const { t } = useTranslation('compliance')
  const dropped = profile.dropped_control_ids?.length ?? 0
  return (
    <div className="flex flex-wrap items-start justify-between gap-3 rounded-md border border-border p-3">
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant="outline">{profile.doc_kind}</Badge>
          <span className="font-medium">
            {profile.title ?? profile.framework}
          </span>
          <Badge variant="neutral">
            {t('regops.oscal.selected', { count: profile.selected_count })}
          </Badge>
          {/* Controls the ingestion DROPPED are the honest half: a profile that
              silently lost controls would overstate coverage. */}
          {dropped > 0 ? (
            <Badge variant="warning">
              {t('regops.oscal.dropped', { count: dropped })}
            </Badge>
          ) : null}
        </div>
        {profile.oscal_version ? (
          <p className="text-xs text-muted-foreground">
            OSCAL {profile.oscal_version}
          </p>
        ) : null}
        <p className="text-xs text-muted-foreground">
          {t('regops.registeredBy', {
            actor: profile.registered_by,
            at: profile.registered_at,
          })}
        </p>
        <HashChip hash={profile.doc_sha256} label={t('regops.docHash')} />
        <DisclaimerNote text={profile.disclaimer} />
      </div>
      {canAdmin ? (
        <Button variant="ghost" size="sm" onClick={onDelete}>
          <Trash2 />
          {t('regops.delete')}
        </Button>
      ) : null}
    </div>
  )
}

// --- compliance-depth packs (E4) ---------------------------------------------
/** DEPTH — familias de profundidad regulatoria, dirigidas por datos.
 *
 * ⛔ ANTES ERAN DOS BLOQUES ESCRITOS A MANO, y su simetría se comprobó línea a línea antes de
 *    fundirlos: normalizando el nombre de familia, los dos `SectionCard` son el MISMO bloque. La
 *    duplicación no era una decisión, era el resultado de añadir la segunda copiando la primera —
 *    y con ella, añadir una tercera costaba otro bloque de cincuenta líneas en un fichero de 1893.
 *
 * ⚠ Y ESTA TABLA NO ERA «LA FORMA DE TODA FAMILIA DE PROFUNDIDAD»: se midió el 2026-08-18 que
 *    `depth/fedramp` traía OTRO payload —`impact_level` (`depthhandlers.go:1176`), `fedRAMPKSIDTO`
 *    y no `DepthPack` (`depthseam.go:316`), `system_name` obligatorio— y que `/aims/pack` ni
 *    siquiera cuelga de `/depth`. Esta nota decía entonces «FedRAMP no entra por aquí».
 *
 * ⇒ **RESUELTO el mismo día, y la salida no fue una tercera sección**: lo que se generalizó no es
 *    el TIPO —cada familia conserva el suyo, con sus campos reales— sino lo que el panel necesita
 *    de ella. La tabla lleva ahora también `titulo`, `etiqueta`, `borrar` y `generar`, así que una
 *    familia con otro DTO y otra ruta entra sin tocar el panel. Lo que NO se hizo es forzar los
 *    cuatro DTOs en `DepthPack`: eso compila y pinta un hueco donde va el nombre.
 */
/**
 * Las CUATRO familias de profundidad que el motor sirve, cada una con todo lo que la distingue.
 *
 * ⛔ POR QUÉ LA TABLA LLEVA TAMBIÉN `titulo`, `etiqueta`, `borrar` y `generar`, y no sólo la
 * consulta. Con dos familias, el panel resolvía cada diferencia con un ternario `kind === 'us'`
 * — en la clave a invalidar, en la mutación de borrado, en la de generación y en el título del
 * diálogo. Un ternario es legible con dos ramas y **deja de serlo con cuatro**: se convierte en
 * una cadena anidada donde añadir la quinta familia obliga a tocar cinco sitios y olvidarse de
 * uno no rompe la compilación, sólo borra el pack equivocado.
 *
 * ⛔ Y LA RUTA NO SE COMPONE DESDE EL `kind`. Sería lo natural —`/depth/${kind}`— y AIMS lo
 * refutaría solo: su ruta es `/aims/pack`, fuera de `/depth` (compliance.go). Por eso cada
 * familia trae su llamada entera.
 *
 * ⚠ `titulo`/`etiqueta` existen porque los cuatro DTOs NO comparten campos de presentación: las
 * dos primeras traen `regulation`/`pack_type`, FedRAMP `system_name`/`impact_level` y AIMS
 * `organisation_name`/`standard`. Leer `pack_type` de un pack de FedRAMP compila y pinta un
 * hueco.
 */
/** Los niveles de impacto de FedRAMP 20x. Conjunto CERRADO a propósito: el motor recorta a
 *  `maxRefLen` y acepta cualquier cadena (depthhandlers.go:1176), así que un campo de texto libre
 *  dejaría autorizar un pack como «IL-2 », «il2» o un typo — y el nivel decide qué controles
 *  entran. Lo que el motor no valida, lo acota la consola. */
const NIVELES_IMPACTO = ['IL2', 'IL4', 'IL5', 'IL6'] as const

const FAMILIAS_DEPTH = [
  {
    kind: 'us' as const,
    permiso: 'depth' as const,
    i18n: 'usLaw',
    query: () => complianceApi.usLawPacks(),
    keys: complianceKeys.usLawPacks,
    exportar: (id: string) => complianceApi.exportUsLawPack(id),
    detalle: (id: string) => complianceApi.usLawPack(id),
    secciones: ['sections'] as const,
    borrar: (id: string) => complianceApi.deleteUsLawPack(id),
    generar: (doc: string, nota: string) =>
      complianceApi.generateUsLawPack(doc, nota),
    titulo: (p: DepthPackBase) =>
      (p as DepthPack).regulation ?? (p as DepthPack).pack_type,
    etiqueta: (p: DepthPackBase) => (p as DepthPack).pack_type,
    pideImpacto: false,
  },
  {
    kind: 'sector' as const,
    permiso: 'depth' as const,
    i18n: 'sector',
    query: () => complianceApi.sectorPacks(),
    keys: complianceKeys.sectorPacks,
    exportar: (id: string) => complianceApi.exportSectorPack(id),
    detalle: (id: string) => complianceApi.sectorPack(id),
    secciones: ['sections'] as const,
    borrar: (id: string) => complianceApi.deleteSectorPack(id),
    generar: (doc: string, nota: string) =>
      complianceApi.generateSectorPack(doc, nota),
    titulo: (p: DepthPackBase) =>
      (p as DepthPack).regulation ?? (p as DepthPack).pack_type,
    etiqueta: (p: DepthPackBase) => (p as DepthPack).pack_type,
    pideImpacto: false,
  },
  {
    kind: 'fedramp' as const,
    permiso: 'depth' as const,
    i18n: 'fedramp',
    query: () => complianceApi.fedrampPacks(),
    keys: complianceKeys.fedrampPacks,
    exportar: (id: string) => complianceApi.exportFedrampPack(id),
    detalle: (id: string) => complianceApi.fedrampPack(id),
    // ⛔ LAS SECCIONES SE DECLARAN POR FAMILIA porque los cuatro DTOs no comparten ninguna. Un
    //    detalle que iterase `Object.keys` pintaría también `doc_sha256` o `generated_by`, que ya
    //    salen en la fila, y perdería el orden con el que un auditor los lee.
    secciones: ['ksis', 'authorization_package'] as const,
    borrar: (id: string) => complianceApi.deleteFedrampPack(id),
    generar: (doc: string, nota: string, impacto: string) =>
      complianceApi.generateFedrampPack(doc, impacto, nota),
    titulo: (p: DepthPackBase) => (p as FedrampKsiPack).system_name,
    etiqueta: (p: DepthPackBase) => (p as FedrampKsiPack).impact_level,
    pideImpacto: true,
  },
  {
    kind: 'aims' as const,
    // ⛔ ESTA FAMILIA NO SE SIRVE BAJO `depth`, y ése era el defecto. Sus cinco rutas están
    //    FUERA de `/depth` —`/aims/pack*`, `modules/compliance/compliance.go:509-513`— y
    //    exigen `compliance:aims:read|admin`. Un comentario de más arriba en este mismo
    //    fichero ya lo decía («su ruta es `/aims/pack`, fuera de `/depth`») y aun así el
    //    panel entero se gateaba con `depth`: la nota estaba y la consecuencia no se sacó.
    permiso: 'aims' as const,
    i18n: 'aims',
    query: () => complianceApi.aimsPacks(),
    keys: complianceKeys.aimsPacks,
    exportar: (id: string) => complianceApi.exportAimsPack(id),
    detalle: (id: string) => complianceApi.aimsPack(id),
    secciones: [
      'soa',
      'policy',
      'risk_register',
      'impact_assessments',
      'lifecycle_controls',
      'supplier_governance',
    ] as const,
    borrar: (id: string) => complianceApi.deleteAimsPack(id),
    generar: (doc: string, nota: string) =>
      complianceApi.generateAimsPack(doc, nota),
    titulo: (p: DepthPackBase) => (p as AimsPack).organisation_name,
    etiqueta: (p: DepthPackBase) => (p as AimsPack).standard,
    pideImpacto: false,
  },
]

type DepthKind = (typeof FAMILIAS_DEPTH)[number]['kind']
// DepthPermiso sale de la PROPIA tabla, no de una lista aparte: si alguien añade una familia con
// un permiso nuevo, el tipo lo recoge y el llamante deja de compilar hasta pasarlo. Una segunda
// lista escrita a mano es exactamente cómo `aims` acabó servido bajo `depth`.
type DepthPermiso = (typeof FAMILIAS_DEPTH)[number]['permiso']
type FamiliaDepth = (typeof FAMILIAS_DEPTH)[number]

/**
 * El detalle de un pack, abierto desde su fila.
 *
 * ⛔ POR QUÉ EXISTE, y no es una pantalla de adorno: los cuatro clientes de detalle
 * (`usLawPack`, `sectorPack`, `fedrampPack`, `aimsPack`) **no tenían un solo llamante**. Había
 * cuatro listados en pantalla cuyos elementos no se podían abrir — el cliente sabía pedir el pack
 * y ninguna vista lo pedía. `check-client-callers.mjs` lo señaló y tenía razón.
 *
 * ⚠ Lo que enseña es lo que la FILA no puede: la lista de incidencias de validación —la fila sólo
 * muestra su CUENTA, y un número no dice qué falta— y las secciones propias de cada familia, que
 * cada una declara porque los cuatro DTOs no comparten ninguna.
 */
function DepthPackDetail({
  fam,
  packId,
  titulo,
  open,
  onOpenChange,
}: {
  fam: FamiliaDepth
  packId: string
  titulo: string
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  // ⚠ El tipo se ANOTA a `DepthPackBase`, que es lo único que los cuatro DTOs comparten. Sin la
  //    anotación, la unión de los cuatro `detalle` deja `pack` en `unknown` y obliga a castear en
  //    cada lectura — y un cast por campo es donde se cuela leer uno que ese DTO no tiene. Lo
  //    específico de cada familia se lee por `fam.secciones`, con su clave declarada.
  const q = useQuery<DepthPackBase>({
    queryKey: [...fam.keys(activeTenant), 'detail', packId] as QueryKey,
    queryFn: () => fam.detalle(packId) as Promise<DepthPackBase>,
    enabled: open,
  })

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{titulo}</SheetTitle>
          <SheetDescription>{t(`regops.${fam.i18n}.title`)}</SheetDescription>
        </SheetHeader>
        <AsyncSection query={q} skeletonHeight={220}>
          {(pack) => (
            <div className="flex flex-col gap-4 p-4">
              <HashChip hash={pack.doc_sha256} label={t('regops.docHash')} />
              <p className="text-xs text-muted-foreground">
                {t('regops.generatedBy', {
                  actor: pack.generated_by,
                  at: pack.generated_at,
                })}
              </p>
              {/* ⛔ LAS INCIDENCIAS, ENTERAS. La fila dice «3 errores» y eso no permite arreglar
                  ninguno: un contador no distingue «falta un campo» de «la sección entera no
                  aplica». Aquí van con su severidad, su sección y su campo. */}
              <div>
                <h3 className="mb-1.5 text-sm font-medium text-foreground">
                  {t('regops.depth.validation')}
                </h3>
                {(pack.validation ?? []).length === 0 ? (
                  <p className="text-xs text-muted-foreground">
                    {t('regops.depth.validationClean')}
                  </p>
                ) : (
                  <ul className="flex flex-col gap-1.5">
                    {(pack.validation ?? []).map((v, i) => (
                      <li
                        key={`${v.section ?? ''}-${v.field ?? ''}-${i}`}
                        className="rounded-md border border-border p-2 text-xs"
                      >
                        <span className="font-medium text-foreground">
                          {v.severity ?? '—'}
                        </span>{' '}
                        <span className="text-muted-foreground">
                          {[v.section, v.field].filter(Boolean).join(' · ')}
                        </span>
                        <p className="text-foreground">{v.message}</p>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
              {/* Las secciones que ESTA familia declara. Una sección ausente se dice; no se pinta
                  un hueco que se lea como «vacía». */}
              {fam.secciones.map((clave) => {
                const valor = (pack as unknown as Record<string, unknown>)[
                  clave
                ]
                return (
                  <div key={clave}>
                    <h3 className="mb-1.5 text-sm font-medium text-foreground">
                      {clave}
                    </h3>
                    {valor == null ? (
                      <p className="text-xs text-muted-foreground">
                        {t('regops.depth.sectionAbsent')}
                      </p>
                    ) : (
                      <pre className="max-h-64 overflow-auto rounded-md border border-border bg-surface p-2 text-xs text-foreground">
                        {JSON.stringify(valor, null, 2)}
                      </pre>
                    )}
                  </div>
                )
              })}
              <DisclaimerNote text={pack.disclaimer} />
            </div>
          )}
        </AsyncSection>
      </SheetContent>
    </Sheet>
  )
}

// ⛔ EL PANEL SE FILTRA POR FAMILIA; NO SE MONTA O NO SE MONTA ENTERO. Recibía un solo
//    `canAdmin` y el llamante lo gateaba todo con `depth`, así que servía la familia AIMS —cuyas
//    cinco rutas exigen `compliance:aims:*` (`modules/compliance/compliance.go:509-513`)— a quien
//    no tenía ese permiso, y se la ocultaba a quien SÍ lo tenía.
//
//    El `useQueries` de abajo ya estaba escrito para esto: su comentario dice que un hook por
//    familia «viola las reglas de los hooks en cuanto la tabla se filtre alguna vez por permiso».
//    La pieza estaba preparada; sólo faltaba filtrar.
type PermisoFamilia = { read: boolean; admin: boolean }

function DepthPackPanel({
  permisos,
}: {
  permisos: Record<DepthPermiso, PermisoFamilia>
}) {
  // Sólo las familias que este usuario puede LEER. El llamante no monta el panel si no puede
  // leer ninguna, así que aquí la lista nunca queda vacía por permisos.
  const familias = FAMILIAS_DEPTH.filter((fam) => permisos[fam.permiso].read)
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const { busy, run } = useExport()
  const [generating, setGenerating] = useState<DepthKind | null>(null)
  const [abierto, setAbierto] = useState<{
    fam: FamiliaDepth
    id: string
    titulo: string
  } | null>(null)
  const [deleting, setDeleting] = useState<{
    pack: DepthPackBase
    fam: FamiliaDepth
  } | null>(null)
  // ⛔ `useQueries`, NO un `useQuery` por familia. Con cuatro, un hook por familia escrito a mano
  // vuelve a ser la lista que esta tabla vino a borrar; y meterlos en un `map` viola las reglas de
  // los hooks en cuanto la tabla se filtre alguna vez por permiso o por edición.
  const consultas = useQueries({
    queries: familias.map((fam) => ({
      queryKey: fam.keys(activeTenant),
      queryFn: () => fam.query(),
    })),
  })

  return (
    <>
      {familias.map((fam, i) => (
        <SectionCard
          key={fam.kind}
          title={t(`regops.${fam.i18n}.title`)}
          description={t(`regops.${fam.i18n}.description`)}
          actions={
            permisos[fam.permiso].admin ? (
              <Button
                variant="primary"
                size="sm"
                onClick={() => setGenerating(fam.kind)}
              >
                <Layers />
                {t(`regops.${fam.i18n}.generate`)}
              </Button>
            ) : null
          }
        >
          <AsyncSection query={consultas[i]} skeletonHeight={140}>
            {(list) =>
              (list.items ?? []).length === 0 ? (
                <EmptyState
                  icon={<Layers />}
                  title={t(`regops.${fam.i18n}.empty`)}
                  description={t(`regops.${fam.i18n}.emptyHint`)}
                />
              ) : (
                <div className="flex flex-col gap-2">
                  {list.items.map((p) => (
                    <DepthRow
                      key={p.id}
                      pack={p}
                      titulo={fam.titulo(p)}
                      etiqueta={fam.etiqueta(p)}
                      canAdmin={permisos[fam.permiso].admin}
                      busy={busy === p.id}
                      onOpen={() =>
                        setAbierto({ fam, id: p.id, titulo: fam.titulo(p) })
                      }
                      onExport={() =>
                        run(
                          p.id,
                          () => fam.exportar(p.id),
                          t('regops.exported'),
                        )
                      }
                      onDelete={() => setDeleting({ pack: p, fam })}
                    />
                  ))}
                </div>
              )
            }
          </AsyncSection>
        </SectionCard>
      ))}
      {abierto ? (
        <DepthPackDetail
          fam={abierto.fam}
          packId={abierto.id}
          titulo={abierto.titulo}
          open={abierto !== null}
          onOpenChange={(v) => {
            if (!v) setAbierto(null)
          }}
        />
      ) : null}
      {generating &&
      permisos[FAMILIAS_DEPTH.find((f) => f.kind === generating)!.permiso]
        .admin ? (
        <GenerateDepthPackDialog
          fam={FAMILIAS_DEPTH.find((f) => f.kind === generating)!}
          open={generating !== null}
          onOpenChange={(v) => {
            if (!v) setGenerating(null)
          }}
        />
      ) : null}

      {deleting && permisos[deleting.fam.permiso].admin ? (
        <DeleteArtefactDialog
          open={deleting !== null}
          onOpenChange={(v) => {
            if (!v) setDeleting(null)
          }}
          title={t('regops.depth.deleteTitle', {
            pack: deleting.fam.titulo(deleting.pack),
          })}
          description={t('regops.depth.deleteDescription')}
          scopeWarning={t('regops.depth.deleteScope')}
          confirmLabel={t('regops.depth.confirmDelete')}
          deletedMessage={t('regops.depth.deleted')}
          unconfirmedMessage={t('regops.deleteUnconfirmed')}
          invalidateKeys={[deleting.fam.keys(activeTenant)]}
          mutationFn={() => deleting.fam.borrar(deleting.pack.id)}
        />
      ) : null}
    </>
  )
}

/** Los CUATRO generadores de profundidad. Comparten el contrato de documento crudo y el
 *  `?scope_note` (depthhandlers.go:251/530/1171, aimspack.go:223) y la identidad de reemplazo al
 *  regenerar; se diferencian en la ruta, en el texto y —sólo FedRAMP— en un campo más.
 *
 *  ⚠ Y ESE CAMPO NO ES COSMÉTICO. `impact_level` es opcional de forma y NO de hecho: si llega
 *  vacío, el motor lo fija a `IL2` (depthhandlers.go:1233-1237) sin decírselo a nadie. Un
 *  diálogo que no lo pidiera dejaría al operador con un pack autorizado a un nivel que él no
 *  eligió y que la pantalla no muestra — por eso el campo es obligatorio aquí. */
function GenerateDepthPackDialog({
  fam,
  open,
  onOpenChange,
}: {
  fam: FamiliaDepth
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [scopeNote, setScopeNote] = useState('')
  const [impactLevel, setImpactLevel] = useState('IL2')
  const ns = fam.i18n

  return (
    <DocumentWriteDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t(`regops.${ns}.generateTitle`)}
      description={t(`regops.${ns}.generateDescription`)}
      documentLabel={t(`regops.${ns}.document`)}
      documentHint={t(`regops.${ns}.documentHint`)}
      confirmLabel={t(`regops.${ns}.confirmGenerate`)}
      successMessage={t(`regops.${ns}.generated`)}
      successHint={t('regops.depth.generatedHint')}
      invalidateKeys={[fam.keys(activeTenant)]}
      onReset={() => {
        setScopeNote('')
        setImpactLevel('IL2')
      }}
      mutationFn={(document) => fam.generar(document, scopeNote, impactLevel)}
    >
      {/* The pack is built against the LIVE assessment of its frameworks
          (depthhandlers.go:265/544), so two runs of the SAME document can differ.
          An operator who does not know that reads a regenerated pack as a
          document error. */}
      <CaveatNotice tone="info">{t('regops.depth.liveHint')}</CaveatNotice>
      {fam.pideImpacto ? (
        <Field
          label={t('regops.fedramp.impactLevel')}
          description={t('regops.fedramp.impactLevelHint')}
          required
        >
          <Select value={impactLevel} onValueChange={setImpactLevel}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {NIVELES_IMPACTO.map((n) => (
                <SelectItem key={n} value={n}>
                  {n}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      ) : null}
      <Field
        label={t('regops.scopeNote')}
        description={t('regops.scopeNoteHint')}
      >
        {({ id }) => (
          <Input
            id={id}
            value={scopeNote}
            onChange={(e) => setScopeNote(e.target.value)}
          />
        )}
      </Field>
    </DocumentWriteDialog>
  )
}

function DepthRow({
  pack,
  titulo,
  etiqueta,
  onOpen,
  canAdmin,
  busy,
  onExport,
  onDelete,
}: {
  pack: DepthPackBase
  /** Ya RESUELTO por la familia. La fila no sabe leer un nombre de un pack: cada DTO lo guarda en
   *  un campo distinto, y una fila que lo adivinara pintaría un hueco en dos de las cuatro. */
  titulo: string
  etiqueta: string
  /** Abre el detalle. Existe porque los cuatro clientes de detalle no tenían llamante: había
   *  listados cuyos elementos no se podían abrir. */
  onOpen: () => void
  canAdmin: boolean
  busy: boolean
  onExport: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation('compliance')
  return (
    <div className="flex flex-wrap items-start justify-between gap-3 rounded-md border border-border p-3">
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={pack.error_count > 0 ? 'danger' : 'success'}>
            {pack.error_count > 0
              ? t('regops.dora.errors', { count: pack.error_count })
              : t('regops.dora.clean')}
          </Badge>
          <span className="font-medium">{titulo}</span>
          <Badge variant="outline">{etiqueta}</Badge>
        </div>
        {pack.scope_note ? (
          <p className="text-xs text-muted-foreground">{pack.scope_note}</p>
        ) : null}
        <p className="text-xs text-muted-foreground">
          {t('regops.generatedBy', {
            actor: pack.generated_by,
            at: pack.generated_at,
          })}
        </p>
        <HashChip hash={pack.doc_sha256} label={t('regops.docHash')} />
        <DisclaimerNote text={pack.disclaimer} />
      </div>
      <div className="flex shrink-0 flex-wrap gap-2">
        {/* ⛔ Abrir va ANTES de exportar, y no es orden estético: exportar se lleva el pack a un
            fichero y abrir permite MIRARLO. Hasta hoy sólo existía lo primero — cuatro listados
            cuyos elementos no se podían abrir, con el cliente de detalle escrito y sin llamante. */}
        <Button variant="ghost" size="sm" onClick={onOpen}>
          <FileJson />
          {t('regops.depth.open')}
        </Button>
        <Button variant="ghost" size="sm" onClick={onExport} disabled={busy}>
          <FileJson />
          {t('regops.export')}
        </Button>
        {canAdmin ? (
          <Button variant="ghost" size="sm" onClick={onDelete}>
            <Trash2 />
            {t('regops.delete')}
          </Button>
        ) : null}
      </div>
    </div>
  )
}

// --- continuous control monitoring (CCM) -------------------------------------

function CcmPanel({ canAdmin }: { canAdmin: boolean }) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [snapshotting, setSnapshotting] = useState(false)
  const [detecting, setDetecting] = useState(false)
  const snapshotsQ = useQuery({
    queryKey: complianceKeys.ccmSnapshots(activeTenant),
    queryFn: () => complianceApi.ccmSnapshots(),
  })
  const driftQ = useQuery({
    queryKey: complianceKeys.ccmDrift(activeTenant),
    queryFn: () => complianceApi.ccmDrift(),
  })
  const snapshots = snapshotsQ.data?.items ?? []
  const canDetectDrift = snapshots.length >= 2

  return (
    <SectionCard
      title={t('regops.ccm.title')}
      description={t('regops.ccm.description')}
      actions={
        canAdmin ? (
          <div className="flex flex-wrap items-center gap-2">
            {/* Drift is a COMPARISON: with fewer than two snapshots the engine
                has nothing to compare and answers 422 (depthhandlers.go:1022).
                Disabled with the reason attached, never hidden — a missing button
                and an impossible one are different news, and hiding it would read
                as "this build cannot detect drift". */}
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setDetecting(true)}
              disabled={!canDetectDrift}
              title={
                canDetectDrift ? undefined : t('regops.ccm.needTwoSnapshots')
              }
            >
              <RefreshCw />
              {t('regops.ccm.detect')}
            </Button>
            <Button
              variant="primary"
              size="sm"
              onClick={() => setSnapshotting(true)}
            >
              <Layers />
              {t('regops.ccm.snapshot')}
            </Button>
          </div>
        ) : null
      }
    >
      <AsyncSection query={snapshotsQ} skeletonHeight={120}>
        {(list) =>
          (list.items ?? []).length === 0 ? (
            <EmptyState
              icon={<Layers />}
              title={t('regops.ccm.empty')}
              description={t('regops.ccm.emptyHint')}
            />
          ) : (
            <ul className="flex flex-col gap-2">
              {list.items.map((s) => (
                <li
                  key={s.id}
                  className="rounded-md border border-border p-2 text-xs"
                >
                  <span className="font-medium">{s.snapshot_at}</span>
                  {s.note ? (
                    <p className="text-muted-foreground">{s.note}</p>
                  ) : null}
                </li>
              ))}
            </ul>
          )
        }
      </AsyncSection>

      <div className="mt-4">
        <p className="mb-2 text-sm font-medium">{t('regops.ccm.driftTitle')}</p>
        <AsyncSection query={driftQ} skeletonHeight={100}>
          {(list) =>
            (list.items ?? []).length === 0 ? (
              <EmptyState
                icon={<ShieldCheck />}
                title={t('regops.ccm.noDrift')}
              />
            ) : (
              <ul className="flex flex-col gap-2">
                {list.items.map((d) => (
                  <li
                    key={d.id}
                    className="rounded-md border border-border p-2 text-xs"
                  >
                    {/* THE ENGINE'S OWN FIELD NAMES. Reading `from_status` /
                        `to_status` / `framework` / `note` drew `? → ?` over a
                        perfectly good answer — see CcmDriftFinding in types.ts.
                        `direction` is the engine's word for which way the control
                        moved and the console does not recompute it from the two
                        statuses: an improvement and a regression must not look
                        alike, and only the engine knows its own ordering. */}
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge
                        variant={
                          d.direction === 'improved' ? 'success' : 'warning'
                        }
                      >
                        {d.prev_status ?? '?'} → {d.curr_status ?? '?'}
                      </Badge>
                      {d.direction ? (
                        <Badge variant="outline">{d.direction}</Badge>
                      ) : null}
                      <span className="font-medium">
                        {d.framework_id ?? ''} {d.control_id ?? ''}
                      </span>
                    </div>
                    {d.title ? <p>{d.title}</p> : null}
                    {d.detail ? (
                      <p className="text-muted-foreground">{d.detail}</p>
                    ) : null}
                  </li>
                ))}
              </ul>
            )
          }
        </AsyncSection>
      </div>

      {canAdmin ? (
        <TakeSnapshotDialog
          open={snapshotting}
          onOpenChange={setSnapshotting}
        />
      ) : null}

      {canAdmin ? (
        <DetectDriftDialog
          open={detecting}
          onOpenChange={setDetecting}
          snapshots={snapshots}
        />
      ) : null}
    </SectionCard>
  )
}

/** The one write on this tab whose input really is a JSON body
 *  (depthhandlers.go:807-810, decoded with DisallowUnknownFields) — so it does
 *  NOT use DocumentWriteDialog, which exists for the raw-document contract. */
function TakeSnapshotDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  // El reporte vive en un solo sitio (use-privileged-mutation.ts:25-32).
  const report = useFailedActionReporter()
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const [frameworks, setFrameworks] = useState<string[]>([])
  const [scopeNote, setScopeNote] = useState('')
  const [seam, setSeam] = useState(false)
  const qc = useQueryClient()

  const reset = () => {
    setFrameworks([])
    setScopeNote('')
    setSeam(false)
  }

  const snapshot = useMutation({
    // An EMPTY selection means every catalog framework (depthhandlers.go:821-826),
    // which is what the dialog says it will do — the field is sent only when the
    // operator narrowed it, so "all" travels as absence rather than as `[]`.
    mutationFn: () =>
      complianceApi.triggerCcmSnapshot({
        frameworks: frameworks.length > 0 ? frameworks : undefined,
        scope_note: scopeNote.trim() || undefined,
      }),
    onSuccess: (created) => {
      if (!created?.id) {
        toast.warning(t('compliance:regops.dialog.unconfirmed'))
        return
      }
      toast.success(t('compliance:regops.ccm.snapshotTaken'))
      void qc.invalidateQueries({
        queryKey: complianceKeys.ccmSnapshots(activeTenant),
      })
      onOpenChange(false)
      reset()
    },
    onError: (e: unknown) => {
      if (isOpenCoreSeam(e)) {
        setSeam(true)
        return
      }
      // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
      // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que esta rama
      // acusaba al operador de un permiso que SÍ tiene y le escondía la salida. Defensa en
      // profundidad: esta ruta no está en ninguna de las cuatro familias de emisores medidas.
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
          <DialogTitle>{t('compliance:regops.ccm.snapshotTitle')}</DialogTitle>
          <DialogDescription>
            {t('compliance:regops.ccm.snapshotDescription')}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            setSeam(false)
            snapshot.mutate()
          }}
        >
          {/* WHAT THIS ACTION COVERS, before it is taken: nothing selected means
              the WHOLE catalog, which is the larger action, not the safer one. */}
          <CaveatNotice tone="info">
            {frameworks.length === 0
              ? t('compliance:regops.ccm.scopeAll')
              : t('compliance:regops.ccm.scopeSome', {
                  count: frameworks.length,
                })}
          </CaveatNotice>

          {seam ? (
            <CaveatNotice tone="warning" className="flex items-start gap-2">
              <SeamBadge label={t('compliance:regops.dialog.seamBadge')} />
              <span>{t('compliance:regops.dialog.seamHint')}</span>
            </CaveatNotice>
          ) : null}

          <FrameworkChecklist
            selected={frameworks}
            onToggle={(id) =>
              setFrameworks((prev) =>
                prev.includes(id)
                  ? prev.filter((x) => x !== id)
                  : [...prev, id],
              )
            }
          />

          <Field
            label={t('compliance:regops.scopeNote')}
            description={t('compliance:regops.scopeNoteHint')}
          >
            {({ id }) => (
              <Input
                id={id}
                value={scopeNote}
                onChange={(e) => setScopeNote(e.target.value)}
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
              disabled={snapshot.isPending}
            >
              {t('compliance:regops.ccm.confirmSnapshot')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Drift detection, and its filter is the defect this session came to fix.
 *
 *  The engine reads the snapshot to compare from `?snapshot_id`
 *  (depthhandlers.go:984-985) and NEVER looks at the request body. The client used
 *  to send the filter as a body, so the request succeeded with 201 and compared
 *  the engine's default pair instead — an answer to a question nobody asked, with
 *  nothing on any surface to say so.
 *
 *  The picker offers the snapshots this tenant has, so the id is one the engine
 *  can resolve; a stale or unknown id is a 404 (`:999`, `:1003`), not a silent
 *  widening.
 *
 *  ⚠ AND IT OMITS THE OLDEST ONE. Drift is a comparison against the PREDECESSOR
 *  (`findPredecessor`, depthhandlers.go:1525-1535, which returns nil for `i == 0`),
 *  so pinning the first snapshot is unsatisfiable by construction — the engine now
 *  answers 422 for it rather than 500, but the console should not offer a target it
 *  can prove is impossible. Raised by the the model contrast of (F3), which
 *  also caught why the suite never saw it: the fixture had ONE snapshot and the stub
 *  answered 201, accepting what production refuses. */
function DetectDriftDialog({
  open,
  onOpenChange,
  snapshots,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  snapshots: CcmSnapshot[]
}) {
  // El reporte vive en un solo sitio (use-privileged-mutation.ts:25-32).
  const report = useFailedActionReporter()
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const [snapshotId, setSnapshotId] = useState('')
  const [seam, setSeam] = useState(false)
  const qc = useQueryClient()
  // The engine lists oldest-first (`listAll`, and it reads `all[len-1]` as current
  // against `all[len-2]`), so index 0 is the one with no predecessor.
  const pinnable = snapshots.slice(1)

  const reset = () => {
    setSnapshotId('')
    setSeam(false)
  }

  const detect = useMutation({
    mutationFn: () => complianceApi.detectCcmDrift(snapshotId),
    onSuccess: (res) => {
      toast.success(
        t('compliance:regops.ccm.driftDetected', {
          count: res.items?.length ?? 0,
        }),
      )
      void qc.invalidateQueries({
        queryKey: complianceKeys.ccmDrift(activeTenant),
      })
      onOpenChange(false)
      reset()
    },
    onError: (e: unknown) => {
      if (isOpenCoreSeam(e)) {
        setSeam(true)
        return
      }
      // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
      // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que esta rama
      // acusaba al operador de un permiso que SÍ tiene y le escondía la salida. Defensa en
      // profundidad: esta ruta no está en ninguna de las cuatro familias de emisores medidas.
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

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        onOpenChange(v)
        if (!v) reset()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('compliance:regops.ccm.detectTitle')}</DialogTitle>
          <DialogDescription>
            {t('compliance:regops.ccm.detectDescription')}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            setSeam(false)
            detect.mutate()
          }}
        >
          <CaveatNotice tone="info">
            {snapshotId === ''
              ? t('compliance:regops.ccm.detectLatest')
              : t('compliance:regops.ccm.detectPinned')}
          </CaveatNotice>

          {seam ? (
            <CaveatNotice tone="warning" className="flex items-start gap-2">
              <SeamBadge label={t('compliance:regops.dialog.seamBadge')} />
              <span>{t('compliance:regops.dialog.seamHint')}</span>
            </CaveatNotice>
          ) : null}

          <Field
            label={t('compliance:regops.ccm.snapshotField')}
            description={t('compliance:regops.ccm.snapshotFieldHint')}
          >
            {({ 'aria-labelledby': labelledBy }) => (
              <Select
                value={snapshotId}
                onValueChange={(v) =>
                  setSnapshotId(v === '__latest__' ? '' : v)
                }
              >
                <SelectTrigger aria-labelledby={labelledBy}>
                  <SelectValue
                    placeholder={t('compliance:regops.ccm.detectLatestOption')}
                  />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__latest__">
                    {t('compliance:regops.ccm.detectLatestOption')}
                  </SelectItem>
                  {pinnable.map((s) => (
                    <SelectItem key={s.id} value={s.id}>
                      {s.snapshot_at}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
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
            <Button type="submit" variant="primary" disabled={detect.isPending}>
              {t('compliance:regops.ccm.confirmDetect')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
