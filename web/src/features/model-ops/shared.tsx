// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Shared model-operations building blocks: the derived-admission-mode helper, the
// honest verdict posture, the signer-root fingerprint chips, the per-version admission
// evidence panel, and the reusable admit dialog. Kept here so the Owned-models drawer
// (canonical per-version Admit) and the Admission tab (verdict history + re-admit)
// render admission the SAME way and never diverge.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { type ReactNode, useState } from 'react'
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
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { KvList, KvRow } from '@/components/ui/kv'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { HashChip } from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { modelsApi, modelsKeys } from '@/features/models/api'
import type {
  AdmissionMode,
  AdmitInput,
  ModelAdmission,
} from '@/features/models/types'

/** Re-exported from `@/lib/admission/policy`, where it is shared with the catalog
 *  admission surface: the engine enforces ONE anchor contract, so one implementation
 *  derives it. Kept exported here so existing call sites are untouched. */
export { deriveAdmissionModes } from '@/lib/admission/policy'

/** The recorded verdict's honest posture. NOT "currently deployable" — that is unknown
 *  until the engine re-checks the anchor at deploy time. */
export type VerdictPosture = 'verified' | 'signed-unbound' | 'denied'
export function verdictPosture(a: ModelAdmission): VerdictPosture {
  if (!a.signature_verified) return 'denied'
  return a.artifact_verified ? 'verified' : 'signed-unbound'
}

export function PostureBadge({ posture }: { posture: VerdictPosture }) {
  const { t } = useTranslation('model-ops')
  if (posture === 'verified')
    return <Badge variant="success">{t('admission.posture.verified')}</Badge>
  if (posture === 'signed-unbound')
    return (
      <Badge variant="warning">{t('admission.posture.signedUnbound')}</Badge>
    )
  return <Badge variant="danger">{t('admission.posture.denied')}</Badge>
}

/** Renders every active signing mode (see deriveAdmissionModes) — one badge per method,
 *  so a policy configuring both certificate roots and bare keys shows both. */
export function ModeBadges({ modes }: { modes: AdmissionMode[] }) {
  const { t } = useTranslation('model-ops')
  return (
    <div className="flex flex-wrap gap-1">
      {modes.map((mode) => (
        <Badge
          key={mode}
          variant={mode === 'empty' ? 'danger' : 'outline'}
          className="font-mono text-[11px]"
        >
          {t(`admission.mode.${mode}`)}
        </Badge>
      ))}
    </div>
  )
}

/** signer_roots rendered as full, copyable root:<sha256> chips. Visual truncation only
 *  (HashChip copies the whole value); never normalized, never labelled "trusted". */
export function SignerRoots({ roots }: { roots?: string[] }) {
  if (!roots || roots.length === 0) return null
  return (
    <div className="flex flex-wrap gap-1.5">
      {roots.map((r) => (
        <HashChip key={r} hash={r} head={12} tail={8} />
      ))}
    </div>
  )
}

function ReasonBox({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <div className="rounded-md border border-border bg-muted/50 px-3 py-2">
      <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </p>
      {/* Engine text — rendered verbatim, never reworded or treated as a key. */}
      <p className="mt-0.5 whitespace-pre-wrap text-xs text-foreground">
        {children}
      </p>
    </div>
  )
}

/** The evidence a single verdict shows. Exposes each truth independently — signature,
 *  artifact, transparency-log present vs verified — and never a "trusted"/"deployable"
 *  summary. */
export function VerdictBody({ verdict }: { verdict: ModelAdmission }) {
  const { t } = useTranslation('model-ops')
  const posture = verdictPosture(verdict)
  const signer = [verdict.signer_identity, verdict.signer_issuer]
    .filter(Boolean)
    .join(' · ')

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-1.5">
        <PostureBadge posture={posture} />
        {verdict.method && (
          <Badge variant="outline" className="font-mono text-[11px]">
            {verdict.method}
          </Badge>
        )}
        {verdict.predicate_type && (
          <Badge variant="neutral" className="font-mono text-[11px]">
            {verdict.predicate_type}
          </Badge>
        )}
      </div>

      {verdict.reason && (
        <ReasonBox label={t('evidence.reason')}>{verdict.reason}</ReasonBox>
      )}
      {!verdict.reason && posture !== 'verified' && (
        <ReasonBox label={t('evidence.reason')}>
          {t('evidence.noReason')}
        </ReasonBox>
      )}

      <KvList>
        {verdict.subject_name && (
          <KvRow label={t('evidence.subjectName')} mono>
            {verdict.subject_name}
          </KvRow>
        )}
        {verdict.subject_digest && (
          <KvRow label={t('evidence.subjectDigest')} align="start">
            <HashChip hash={verdict.subject_digest} head={12} tail={10} />
          </KvRow>
        )}
        {signer && (
          <KvRow label={t('evidence.signer')} align="start" mono>
            <span className="block break-all">{signer}</span>
          </KvRow>
        )}
        {verdict.signer_roots && verdict.signer_roots.length > 0 && (
          <KvRow label={t('evidence.signerRoots')} align="start">
            <SignerRoots roots={verdict.signer_roots} />
          </KvRow>
        )}
        <KvRow label={t('evidence.artifact')}>
          {verdict.artifact_verified
            ? t('evidence.artifactVerified')
            : t('evidence.artifactUnverified')}
        </KvRow>
        {/* present and verified are DISTINCT truths — present is never a verification. */}
        <KvRow label={t('evidence.tlog')}>
          {verdict.tlog_verified
            ? t('evidence.tlogVerified')
            : verdict.tlog_present
              ? t('evidence.tlogPresent')
              : t('evidence.tlogAbsent')}
        </KvRow>
        {verdict.resource_count > 0 && (
          <KvRow label={t('evidence.resourceCount')}>
            {verdict.resource_count}
          </KvRow>
        )}
        {verdict.coverage_note && (
          <KvRow label={t('evidence.coverage')} align="start">
            {verdict.coverage_note}
          </KvRow>
        )}
        {verdict.attested_by && (
          <KvRow label={t('evidence.attestedBy')} mono>
            {verdict.attested_by}
          </KvRow>
        )}
        {verdict.attested_at && (
          <KvRow label={t('evidence.attestedAt')}>
            <RelTimeLabel ts={verdict.attested_at} />
          </KvRow>
        )}
      </KvList>
    </div>
  )
}

/** The evidence panel for ONE version — fetches its single verdict (never a panel per
 *  row). Renders loading / forbidden / the verdict / an honest "no attempt recorded". */
export function VersionEvidence({ versionRef }: { versionRef: string }) {
  const { t } = useTranslation('model-ops')
  const { activeTenant } = useAuth()
  const query = useQuery({
    queryKey: modelsKeys.modelAdmissions(activeTenant, {
      version_ref: versionRef,
    }),
    queryFn: () => modelsApi.modelAdmissions({ version_ref: versionRef }),
    enabled: !!versionRef,
  })

  if (query.isLoading) return <Skeleton className="h-24 w-full" />
  // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status (lib/api/errors.ts:59)
  // y un `step_up_required` lo satisface también, así que leerlo primero tapaba el veredicto
  // de admisión con un «no autorizado» falso. Es el único sitio de LECTURA de este grupo.
  if (query.error instanceof ApiError && query.error.isStepUpRequired)
    return (
      <StepUpRequiredState
        action="generic"
        onElevated={() => void query.refetch()}
      />
    )
  if (query.error instanceof ApiError && query.error.isForbidden)
    return <ForbiddenState />
  // ⛔ Y UNA LECTURA FALLIDA NO ES «NO HAY EVIDENCIA». Sin esta rama, un 500 o una caída de
  // red caían al estado vacío de abajo y el panel afirmaba «no admission attempt recorded»
  // —una afirmación sobre el MUNDO— cuando lo único cierto es que no se pudo mirar. En un
  // panel de evidencia de admisión esa confusión es de las caras: dice que no hubo intento
  // donde igual lo hubo y fue denegado. Es preexistente, y lo cazó el contraste `sol max`.
  if (query.error) return <ErrorState retry={() => void query.refetch()} />
  const verdict = query.data?.items?.[0]
  if (!verdict)
    return (
      <EmptyState
        title={t('evidence.none')}
        description={t('evidence.noneHint')}
      />
    )
  return <VerdictBody verdict={verdict} />
}

/** The reusable admit action. `bundle` is a raw Sigstore/OMS JSON object; `200` with
 *  admitted:false is a recorded deny (warning, not success); malformed → 400 (input
 *  error). `reAdmit` only changes the copy (re-admitting an already-recorded version). */
export function AdmitDialog({
  versionId,
  versionLabel,
  reAdmit,
  open,
  onOpenChange,
}: {
  versionId: string
  versionLabel: string
  reAdmit?: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {open && (
          <AdmitForm
            versionId={versionId}
            versionLabel={versionLabel}
            reAdmit={reAdmit}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function AdmitForm({
  versionId,
  versionLabel,
  reAdmit,
  onClose,
}: {
  versionId: string
  versionLabel: string
  reAdmit?: boolean
  onClose: () => void
}) {
  const { t } = useTranslation(['model-ops', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()

  const [bundleText, setBundleText] = useState('')
  const [digestsText, setDigestsText] = useState('')
  const [modelRef, setModelRef] = useState('')
  const [aibomRef, setAibomRef] = useState('')
  const [note, setNote] = useState('')

  // A Sigstore bundle is a JSON OBJECT. A scalar/array parses but is not a bundle, so
  // flag it inline rather than posting garbage into a generic 400.
  let bundle: unknown
  if (bundleText.trim()) {
    try {
      bundle = JSON.parse(bundleText)
    } catch {
      bundle = undefined
    }
  }
  const bundleIsObject =
    typeof bundle === 'object' && bundle !== null && !Array.isArray(bundle)
  const bundleInvalid = bundleText.trim() !== '' && !bundleIsObject

  // resolved_digests is an OPTIONAL map<file,hex>. Parse defensively; a non-object is a
  // field error, never silently dropped.
  let digests: Record<string, string> | undefined
  let digestsInvalid = false
  if (digestsText.trim()) {
    try {
      const parsed: unknown = JSON.parse(digestsText)
      if (
        typeof parsed === 'object' &&
        parsed !== null &&
        !Array.isArray(parsed) &&
        Object.values(parsed).every((v) => typeof v === 'string')
      ) {
        digests = parsed as Record<string, string>
      } else {
        digestsInvalid = true
      }
    } catch {
      digestsInvalid = true
    }
  }

  const valid = bundleIsObject && !digestsInvalid

  const mutation = useMutation({
    mutationFn: (input: AdmitInput) => modelsApi.admitVersion(versionId, input),
    onSuccess: async (data) => {
      // Both the version-scoped evidence and any admissions list must refresh — but the
      // seal ledgers must NOT (this writes a verdict, not a seal).
      await queryClient.invalidateQueries({
        queryKey: ['models', activeTenant, 'model-admissions'],
      })
      // Honest branching: a 200 admitted:false is recorded evidence that FAILED policy
      // — a warning with the verbatim reason, never a green success.
      if (data.admitted) {
        toast.success(t('admit.admitted'))
      } else {
        toast.warning(t('admit.recordedNotAdmitted'), {
          description: data.admission.reason || undefined,
        })
      }
      onClose()
    },
    onError: (err) => {
      // A malformed bundle is a 400 — no verdict was recorded (distinct from a deny).
      // Ese mensaje lo sigue enseñando `report`, que lo pasa como descripción del toast
      // genérico (use-privileged-mutation.ts:54-58): la política es la misma, el texto no
      // se pierde, y encima deja de acusar al operador cuando el 403 es de ceremonia.
      report(err)
    },
  })

  function submit() {
    if (!valid) return
    const input: AdmitInput = {
      bundle,
      ...(digests ? { resolved_digests: digests } : {}),
      ...(modelRef.trim() ? { model_ref: modelRef.trim() } : {}),
      ...(aibomRef.trim() ? { aibom_ref: aibomRef.trim() } : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
    }
    mutation.mutate(input)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {reAdmit ? t('admit.reAdmitTitle') : t('admit.title')}
        </DialogTitle>
        <DialogDescription>
          {t('admit.body', { version: versionLabel })}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('admit.bundle')}
          htmlFor="admit-bundle"
          description={t('admit.bundleHint')}
          error={bundleInvalid ? t('admit.bundleInvalid') : undefined}
          required
        >
          <Textarea
            id="admit-bundle"
            value={bundleText}
            onChange={(e) => setBundleText(e.target.value)}
            placeholder={t('admit.bundlePlaceholder')}
            aria-invalid={bundleInvalid || undefined}
            rows={8}
            className="font-mono text-xs"
          />
        </Field>
        <Field
          label={t('admit.digests')}
          htmlFor="admit-digests"
          description={t('admit.digestsHint')}
          error={digestsInvalid ? t('admit.digestsInvalid') : undefined}
        >
          <Textarea
            id="admit-digests"
            value={digestsText}
            onChange={(e) => setDigestsText(e.target.value)}
            placeholder={'{ "model.safetensors": "…" }'}
            aria-invalid={digestsInvalid || undefined}
            rows={3}
            className="font-mono text-xs"
          />
        </Field>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('admit.modelRef')} htmlFor="admit-model-ref">
            <Input
              id="admit-model-ref"
              value={modelRef}
              onChange={(e) => setModelRef(e.target.value)}
              mono
            />
          </Field>
          <Field label={t('admit.aibomRef')} htmlFor="admit-aibom-ref">
            <Input
              id="admit-aibom-ref"
              value={aibomRef}
              onChange={(e) => setAibomRef(e.target.value)}
              mono
            />
          </Field>
        </div>
        <Field label={t('admit.note')} htmlFor="admit-note">
          <Textarea
            id="admit-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={2}
          />
        </Field>
      </div>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={submit}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('admit.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}
