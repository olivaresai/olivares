// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The attestation-admission panel for an mcp/connector catalog entry — the DURABLE
// answer to "why was this entry admitted or refused into the served catalog?". It
// renders the recorded provenance/SBOM verdict (verified/denied posture + the
// verifier's verbatim reason + the artifact it covered) that gates approval, and
// offers the admin-only "admit an attestation" action. It is the read/write surface
// over the durable verdict the engine already persists (GET/POST …-admissions); the
// verdict outlives any toast, closing the "refusal only lived in a transient toast"
// gap. The model gate is intentionally NOT surfaced here — it lives in the models
// module (no catalog route), so admissionKind() returns only mcp/connector.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ShieldCheck } from 'lucide-react'
import { type ReactNode, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
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
import { ForbiddenState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { KvList, KvRow } from '@/components/ui/kv'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { RelTimeLabel } from '@/features/shared'
import { catalogApi, catalogKeys } from './api'
import { admissionKind } from './types'
import type { AdmissionDTO, AdmissionKind, AdmitInput, EntryDTO } from './types'

/** verdict posture — honest tri-state, never "verified" when it is not. */
type Posture = 'verified' | 'unbound' | 'denied'
function posture(a: AdmissionDTO): Posture {
  if (!a.signature_verified) return 'denied'
  return a.artifact_verified ? 'verified' : 'unbound'
}

export function AdmissionPanel({ entry }: { entry: EntryDTO }) {
  const { t } = useTranslation('catalog')
  const { activeTenant, can } = useAuth()
  const kind = admissionKind(entry.kind)
  const canAdmin = can('catalog:entry:admin')
  const [admitOpen, setAdmitOpen] = useState(false)

  const query = useQuery({
    queryKey: catalogKeys.admissions(
      activeTenant,
      kind ?? 'mcp',
      entry.id ?? '',
    ),
    queryFn: () => catalogApi.listAdmissions(kind!, entry.id!),
    enabled: !!kind && !!entry.id,
  })

  if (!kind) return null // not an admission-gated kind — nothing to render.

  const verdict = query.data?.items?.[0]

  return (
    <section className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <h3 className="flex items-center gap-1.5 text-sm font-medium text-foreground">
          <ShieldCheck className="size-4 text-muted-foreground" aria-hidden />
          {t('admission.title')}
        </h3>
        {canAdmin && (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setAdmitOpen(true)}
          >
            {t('admission.admit')}
          </Button>
        )}
      </div>
      <p className="text-xs text-muted-foreground">{t('admission.caption')}</p>

      {query.isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : query.error instanceof ApiError && query.error.isStepUpRequired ? (
        /* ⛔ ASEGURAMIENTO ANTES QUE ROL (_intel/async.tsx:56-63). `isForbidden` es sólo el
           status (lib/api/errors.ts:59), así que un `step_up_required` lo satisfacía y este
           panel escondía el VEREDICTO DE ADMISIÓN —el motivo de abrirlo— detrás de un «no
           autorizado» falso. El ForbiddenState de rol se queda detrás, intacto. */
        <StepUpRequiredState
          action="generic"
          onElevated={() => void query.refetch()}
        />
      ) : query.error instanceof ApiError && query.error.isForbidden ? (
        <ForbiddenState />
      ) : verdict ? (
        <VerdictBody verdict={verdict} />
      ) : (
        <EmptyState
          title={t('admission.none')}
          description={t('admission.noneHint')}
        />
      )}

      {/* ⛔ AQUI HABIA UN AVISO DE RECORTE Y SE RETIRA, no se le anade un testigo: NO PUEDE
          APARECER. Y la garantia esta en DOS sitios distintos, que conviene no confundir porque
          uno de ellos podria cambiar sin que nadie lo note:

          1) EL CLIENTE, que es quien de verdad lo impide hoy. `listAdmissions` exige `entryRef`
             en su firma (`api.ts:115-118`) y este panel solo consulta con una entrada delante
             (`admission-panel.tsx:67`, `enabled: !!kind && !!entry.id`). NO es el motor: el
             handler trata `entry_ref` como OPCIONAL y filtra solo `if v != ""`
             (`mcpadmission.go:537`, `connectoradmission.go:618`), asi que una lista SIN filtrar
             es representable en la API — simplemente no la pide nadie. Lo verifico P sobre este
             mismo commit buscando la via por la que el aviso SI podria aparecer.

          2) EL INDICE UNICO, que acota el resultado aunque lo de arriba cambiara: un veredicto
             por entrada y tenant (`mcpadmission.go:154-155`, `connectoradmission.go:171-172`,
             «One verdict per catalog entry (per tenant); re-admit upserts»). Con a lo sumo UNA
             fila y un techo de mil, `has_more` no puede ser cierto.

          ⇒ si algun dia un llamante pide la lista sin `entry_ref`, sigue sin poder recortarse por
          (2) — pero la premisa que sostiene este comentario habra cambiado, y entonces se re-mide
          en vez de confiar en lo que dice aqui.

          Un aviso que no puede aparecer no protege: solo lo afirma. Y ademas cuesta —
          `check-list-truncation-witness.sh` lo dice con esas palabras—, porque cualquiera que
          audite la pantalla lo cuenta como cobertura que no existe. Lo destapo el contraste
 (A-09) al senalar que su mutante sobrevivia: la respuesta correcta no era darle
          testigo, era quitarlo. Si algun dia el llamante deja de filtrar por `entry_ref`, el
          aviso vuelve CON su par de testigos, como el de instancias. */}

      {canAdmin && (
        <AdmitDialog
          entry={entry}
          kind={kind}
          open={admitOpen}
          onOpenChange={setAdmitOpen}
        />
      )}
    </section>
  )
}

function PostureBadge({ p }: { p: Posture }) {
  const { t } = useTranslation('catalog')
  if (p === 'verified')
    return <Badge variant="success">{t('admission.verified')}</Badge>
  if (p === 'unbound')
    return <Badge variant="warning">{t('admission.signedUnbound')}</Badge>
  return <Badge variant="danger">{t('admission.notVerified')}</Badge>
}

function VerdictBody({ verdict }: { verdict: AdmissionDTO }) {
  const { t } = useTranslation('catalog')
  const p = posture(verdict)
  const signer = [verdict.signer_identity, verdict.signer_issuer]
    .filter(Boolean)
    .join(' · ')

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-1.5">
        <PostureBadge p={p} />
        {verdict.predicate_type && (
          <Badge variant="outline" className="font-mono text-[11px]">
            {verdict.predicate_type}
          </Badge>
        )}
      </div>

      {/* The verifier's verbatim explanation — rendered as-is, never reworded. */}
      {verdict.reason && (
        <Reason label={t('admission.reason')}>{verdict.reason}</Reason>
      )}
      {!verdict.reason && p !== 'verified' && (
        <Reason label={t('admission.reason')}>{t('admission.noReason')}</Reason>
      )}

      <KvList>
        {verdict.subject_digest && (
          <KvRow label={t('admission.subjectDigest')} align="start" mono>
            <span className="block break-all">{verdict.subject_digest}</span>
          </KvRow>
        )}
        {verdict.subject_name && (
          <KvRow label={t('admission.subjectName')} mono>
            {verdict.subject_name}
          </KvRow>
        )}
        {verdict.method && (
          <KvRow label={t('admission.method')} mono>
            {verdict.method}
          </KvRow>
        )}
        {signer && (
          <KvRow label={t('admission.signer')} align="start" mono>
            <span className="block break-all">{signer}</span>
          </KvRow>
        )}
        <KvRow label={t('admission.transparencyLog')}>
          {verdict.tlog_verified
            ? t('admission.tlogVerified')
            : verdict.tlog_present
              ? t('admission.tlogPresent')
              : t('admission.tlogAbsent')}
        </KvRow>
        {verdict.coverage_note && (
          <KvRow label={t('admission.coverage')} align="start">
            {verdict.coverage_note}
          </KvRow>
        )}
        {verdict.attested_by && (
          <KvRow label={t('admission.attestedBy')} mono>
            {verdict.attested_by}
          </KvRow>
        )}
        {verdict.attested_at && (
          <KvRow label={t('admission.attestedAt')}>
            <RelTimeLabel ts={verdict.attested_at} />
          </KvRow>
        )}
      </KvList>
    </div>
  )
}

function Reason({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="rounded-md border border-border bg-muted/50 px-3 py-2">
      <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </p>
      <p className="mt-0.5 text-xs text-foreground">{children}</p>
    </div>
  )
}

function AdmitDialog({
  entry,
  kind,
  open,
  onOpenChange,
}: {
  entry: EntryDTO
  kind: AdmissionKind
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {open && (
          <AdmitForm
            entry={entry}
            kind={kind}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function AdmitForm({
  entry,
  kind,
  onClose,
}: {
  entry: EntryDTO
  kind: AdmissionKind
  onClose: () => void
}) {
  const report = useFailedActionReporter()
  const { t } = useTranslation(['catalog', 'common', 'errors'])
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()

  const [bundleText, setBundleText] = useState('')
  const [expectedDigest, setExpectedDigest] = useState('')
  const [note, setNote] = useState('')

  let bundle: unknown
  if (bundleText.trim()) {
    try {
      bundle = JSON.parse(bundleText)
    } catch {
      bundle = undefined
    }
  }
  // A Sigstore bundle is a JSON OBJECT — a bare scalar/array parses but is not a bundle,
  // so treat it as invalid here (inline field error) rather than enabling submit and
  // letting the backend 400 into a generic toast.
  const bundleIsObject =
    typeof bundle === 'object' && bundle !== null && !Array.isArray(bundle)
  const bundleInvalid = bundleText.trim() !== '' && !bundleIsObject
  const valid = bundleIsObject

  const mutation = useMutation({
    mutationFn: (input: AdmitInput) => catalogApi.admitEntry(entry.id!, input),
    onSuccess: async (data) => {
      await queryClient.invalidateQueries({
        queryKey: catalogKeys.admissions(activeTenant, kind, entry.id ?? ''),
      })
      // Honest branching: a 200 with admitted:false means "recorded, but the
      // attestation did NOT satisfy the policy" — a warning, never a green success.
      if (data.admitted) {
        toast.success(t('admission.admitted'))
      } else {
        toast.warning(t('admission.recordedNotAdmitted'), {
          description: data.admission.reason || undefined,
        })
      }
      onClose()
    },
    onError: (err, input) => {
      // El reporte se delega en la política que distingue los tres casos
      // (lib/hooks/use-privileged-mutation.ts:33-59). Y aquí SÍ se pasa reanudación: esta
      // es la única escritura de la lista que destruye trabajo TECLEADO —el bundle de
      // admisión—, así que tras la ceremonia se reintenta con las MISMAS variables en vez
      // de devolver al operador a pegar el JSON otra vez.
      report(err, () => mutation.mutate(input))
    },
  })

  function submit() {
    if (!valid) return
    const input: AdmitInput = {
      bundle,
      ...(expectedDigest.trim()
        ? { expected_digest: expectedDigest.trim() }
        : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
    }
    mutation.mutate(input)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('admission.admitTitle')}</DialogTitle>
        <DialogDescription>
          {kind === 'connector'
            ? t('admission.admitBodyConnector')
            : t('admission.admitBodyMcp')}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('admission.bundle')}
          htmlFor="admit-bundle"
          description={t('admission.bundleHint')}
          error={bundleInvalid ? t('admission.bundleInvalid') : undefined}
          required
        >
          <Textarea
            id="admit-bundle"
            value={bundleText}
            onChange={(e) => setBundleText(e.target.value)}
            placeholder={t('admission.bundlePlaceholder')}
            aria-invalid={bundleInvalid || undefined}
            rows={8}
            className="font-mono text-xs"
          />
        </Field>
        <Field
          label={t('admission.expectedDigest')}
          htmlFor="admit-digest"
          description={t('admission.expectedDigestHint')}
        >
          <Input
            id="admit-digest"
            value={expectedDigest}
            onChange={(e) => setExpectedDigest(e.target.value)}
            placeholder="sha256:…"
            mono
          />
        </Field>
        <Field label={t('admission.note')} htmlFor="admit-note">
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
          {t('admission.admitSubmit')}
        </Button>
      </DialogFooter>
    </>
  )
}
