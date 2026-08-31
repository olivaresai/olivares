// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Evidence & documents for one owned model (rendered in the Owned-models drawer): the
// AIBOM and the model card. The GENERATE/PREVIEW/EXPORT paths are LIVE, read-only and
// NOT sealed — the copy says so; only "Seal AIBOM" (POST, no body) writes durable,
// ledger-anchored tamper-evidence, which then appears in the Model evidence ledger. The
// AIBOM's canonical sealed form is ALWAYS CycloneDX; SPDX is a read-only alternate
// serialization that can never be sealed. Documents are opaque to the browser — fetched
// and saved verbatim, never rebuilt here.
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Download, FileText, ScrollText, Stamp } from 'lucide-react'
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { ListTruncationBadge, HashChip } from '@/features/_intel'
import { KvList, KvRow } from '@/components/ui/kv'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { RelTimeLabel } from '@/features/shared'
import { EVIDENCE_PAGE, modelsApi, modelsKeys } from '@/features/models/api'
import type { AibomSealReceipt, ModelCardDoc } from '@/features/models/types'
import { SealDetail } from './evidence'
import { downloadBlob, downloadJson, fetchModelCardMarkdown } from './export'

/** A filesystem-safe slug for export filenames. */
function slug(name: string): string {
  return (
    name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '') || 'model'
  )
}

export function ModelDocuments({
  ownedRef,
  ownedName,
  canWrite,
}: {
  ownedRef: string
  ownedName: string
  canWrite: boolean
}) {
  const { t } = useTranslation(['model-ops', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()

  const [aibomPreview, setAibomPreview] = useState(false)
  const [cardPreview, setCardPreview] = useState(false)
  const [confirmSeal, setConfirmSeal] = useState(false)
  const [receipt, setReceipt] = useState<AibomSealReceipt | null>(null)
  const [busy, setBusy] = useState<string | null>(null)

  // This model's seal history (durable commitments). Distinct query key from the live
  // generate — a generated document is never a sealed one.
  // El mismo techo y el mismo aviso que el ledger entre modelos: precintar CREA fila, así que
  // el historial de un modelo también crece sin tope y sin `limit` salían los cien primeros.
  const historyParams = { owned_ref: ownedRef, limit: EVIDENCE_PAGE }
  const historyQ = useQuery({
    queryKey: modelsKeys.aibomSeals(activeTenant, ownedRef, historyParams),
    queryFn: () => modelsApi.aibomSeals(historyParams),
  })

  /** Run an async export with a per-action busy flag and an honest error toast. */
  async function runExport(key: string, fn: () => Promise<void>) {
    setBusy(key)
    try {
      await fn()
    } catch (err) {
      report(err)
    } finally {
      setBusy(null)
    }
  }

  const sealMutation = useMutation({
    mutationFn: () => modelsApi.sealAibom(ownedRef),
    onSuccess: async (r) => {
      // A seal is a NEW durable ledger row — refresh both this model's history and the
      // cross-model ledger (the `aiboms` prefix covers both keys). The response carries
      // the ONLY copy of the sealed document (the ledger stores just the hash), so open a
      // receipt so the operator can save it now.
      // ⛔ LOS DOS PREFIJOS CONCRETOS, no el de la familia entera. `aibomsAll` alcanzaría
      //    también los historiales de TODOS los demás modelos, que no han cambiado: no
      //    corrompe nada, pero pide de nuevo lo que nadie tocó. Lo señaló el contraste.
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: modelsKeys.aibomSeals(activeTenant),
        }),
        queryClient.invalidateQueries({
          queryKey: modelsKeys.aibomSeals(activeTenant, ownedRef),
        }),
      ])
      setReceipt(r)
      toast.success(t('documents.sealRecorded'))
    },
    onError: (err) => {
      report(err)
    },
  })

  const seals = historyQ.data?.items ?? []

  return (
    <section className="flex flex-col gap-4">
      {/* Block 1 — LIVE generated documents (never sealed, never audited). */}
      <div className="flex flex-col gap-3">
        <div>
          <h3 className="flex items-center gap-2 text-sm font-medium text-foreground">
            {t('documents.liveTitle')}
            <Badge variant="outline" className="text-[11px]">
              {t('documents.live')}
            </Badge>
          </h3>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t('documents.liveSubtitle')}
          </p>
        </div>

        <DocRow
          icon={<FileText className="size-4 text-muted-foreground" />}
          label={t('documents.aibom.title')}
          onPreview={() => setAibomPreview(true)}
          exporting={busy === 'aibom-cdx' || busy === 'aibom-spdx'}
          disabled={busy != null}
          items={[
            {
              label: t('documents.aibom.cyclonedx'),
              onSelect: () =>
                void runExport('aibom-cdx', async () => {
                  const doc = await modelsApi.generateAibom(ownedRef)
                  downloadJson(doc, `aibom-${slug(ownedName)}.cyclonedx.json`)
                }),
            },
            {
              label: t('documents.aibom.spdx'),
              onSelect: () =>
                void runExport('aibom-spdx', async () => {
                  const doc = await modelsApi.generateAibom(ownedRef, {
                    format: 'spdx',
                  })
                  downloadJson(doc, `aibom-${slug(ownedName)}.spdx.json`)
                }),
            },
          ]}
        />

        <DocRow
          icon={<ScrollText className="size-4 text-muted-foreground" />}
          label={t('documents.card.title')}
          onPreview={() => setCardPreview(true)}
          exporting={busy === 'card-md' || busy === 'card-json'}
          disabled={busy != null}
          items={[
            {
              label: t('documents.card.markdown'),
              onSelect: () =>
                void runExport('card-md', async () => {
                  const blob = await fetchModelCardMarkdown(ownedRef)
                  downloadBlob(blob, `model-card-${slug(ownedName)}.md`)
                }),
            },
            {
              label: t('documents.card.json'),
              onSelect: () =>
                void runExport('card-json', async () => {
                  const doc = await modelsApi.modelCard(ownedRef)
                  downloadJson(doc, `model-card-${slug(ownedName)}.json`)
                }),
            },
          ]}
        />
      </div>

      {/* Block 2 — SEALED durable commitments (distinct from the live docs above). */}
      <div className="flex flex-col gap-3">
        <div className="flex items-start justify-between gap-2">
          <div>
            <h3 className="flex items-center gap-2 text-sm font-medium text-foreground">
              <Stamp className="size-4 text-muted-foreground" />
              {t('documents.sealsTitle')}
            </h3>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {t('documents.aibom.sealHint')}
            </p>
          </div>
          {canWrite && (
            <Button
              variant="primary"
              size="sm"
              onClick={() => setConfirmSeal(true)}
              disabled={sealMutation.isPending}
            >
              {sealMutation.isPending ? (
                <Spinner size="sm" aria-hidden />
              ) : (
                <Stamp />
              )}
              {t('documents.aibom.seal')}
            </Button>
          )}
        </div>

        {/* Fuera del bloque de datos: un refetch fallido no debe dejar el aviso flotando. */}
        <ListTruncationBadge
          query={historyQ}
          label={t('documents.sealsTruncated', { n: seals.length })}
          hint={t('documents.sealsTruncatedHint')}
        />

        {historyQ.isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : historyQ.error ? (
          // ⛔ UNA LECTURA FALLIDA NO ES «NO HAY PRECINTOS». Sin esta rama, un 500 o una caída
          //    de red caían al estado vacío de abajo y el panel afirmaba «no sealed documents»
          //    —una afirmación sobre el MUNDO— cuando lo único cierto es que no se pudo mirar.
          //    En un panel de evidencia es de las confusiones caras: dice que no hay precinto
          //    donde igual lo hay. Y el caso peor era el otro: con dato viejo en caché la lista
          //    seguía pintándose, el aviso de recorte se ocultaba por el `!error` de arriba, y
          //    quedaba una lista VIEJA y RECORTADA sin marca ninguna. Lo devolvió el contraste
          //    externo; `VersionEvidence` ya tenía esta rama por la misma razón.
          <ErrorState
            title={t('documents.sealsLoadError')}
            retry={() => void historyQ.refetch()}
          />
        ) : seals.length === 0 ? (
          <EmptyState
            title={t('documents.noSeals')}
            description={t('documents.noSealsHint')}
          />
        ) : (
          <ul className="flex flex-col divide-y divide-border rounded-md border border-border">
            {seals.map((s) => (
              <li
                key={s.id}
                className="flex items-center justify-between gap-2 px-3 py-2"
              >
                <HashChip hash={s.content_hash} head={12} tail={10} />
                <span className="flex items-center gap-2 text-[11px] text-muted-foreground">
                  <span className="tabular-nums">
                    {s.component_count} · seq {s.ledger_seq}
                  </span>
                  {s.generated_at && <RelTimeLabel ts={s.generated_at} />}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>

      <ConfirmDialog
        open={confirmSeal}
        onOpenChange={setConfirmSeal}
        title={t('documents.sealConfirmTitle')}
        description={t('documents.sealConfirmBody')}
        confirmLabel={t('documents.aibom.seal')}
        pending={sealMutation.isPending}
        onConfirm={() => {
          setConfirmSeal(false)
          sealMutation.mutate()
        }}
      />

      <SealReceiptDialog
        receipt={receipt}
        ownedName={ownedName}
        onClose={() => setReceipt(null)}
      />

      <AibomPreviewDialog
        ownedRef={ownedRef}
        open={aibomPreview}
        onOpenChange={setAibomPreview}
      />
      <ModelCardPreviewDialog
        ownedRef={ownedRef}
        open={cardPreview}
        onOpenChange={setCardPreview}
      />
    </section>
  )
}

/** One live-document row: a label, a Preview action and an Export menu. */
function DocRow({
  icon,
  label,
  onPreview,
  exporting,
  disabled,
  items,
}: {
  icon: React.ReactNode
  label: string
  onPreview: () => void
  exporting: boolean
  disabled: boolean
  items: { label: string; onSelect: () => void }[]
}) {
  const { t } = useTranslation('model-ops')
  return (
    <div className="flex items-center justify-between gap-2 rounded-md border border-border px-3 py-2">
      <span className="flex items-center gap-2 text-sm">
        {icon}
        {label}
      </span>
      <div className="flex items-center gap-2">
        <Button variant="secondary" size="sm" onClick={onPreview}>
          {t('documents.preview')}
        </Button>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="sm" disabled={disabled}>
              {exporting ? <Spinner size="sm" aria-hidden /> : <Download />}
              {t('documents.export')}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {items.map((it) => (
              <DropdownMenuItem key={it.label} onSelect={it.onSelect}>
                {it.label}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  )
}

/** After a seal, the ledger keeps only the hash — the sealed CycloneDX document exists
 *  ONLY in this response. Show the receipt and offer to save it now. */
function SealReceiptDialog({
  receipt,
  ownedName,
  onClose,
}: {
  receipt: AibomSealReceipt | null
  ownedName: string
  onClose: () => void
}) {
  const { t } = useTranslation(['model-ops', 'common'])
  return (
    <Dialog open={!!receipt} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
        {receipt && (
          <>
            <DialogHeader>
              <DialogTitle>{t('documents.sealRecorded')}</DialogTitle>
              <DialogDescription>
                {t('documents.receiptBody')}
              </DialogDescription>
            </DialogHeader>
            <SealDetail seal={receipt.seal} />
            <DialogFooter>
              <Button variant="secondary" onClick={onClose}>
                {t('common:actions.close')}
              </Button>
              <Button
                variant="primary"
                onClick={() =>
                  downloadJson(
                    receipt.aibom,
                    `aibom-${slug(ownedName)}.sealed.cyclonedx.json`,
                  )
                }
              >
                <Download />
                {t('documents.downloadSealed')}
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** The live AIBOM preview — CycloneDX by default, with an SPDX toggle. The document is
 *  rendered as opaque, read-only JSON (never rebuilt in the browser). */
function AibomPreviewDialog({
  ownedRef,
  open,
  onOpenChange,
}: {
  ownedRef: string
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('model-ops')
  const { activeTenant } = useAuth()
  const [format, setFormat] = useState<'cyclonedx' | 'spdx'>('cyclonedx')

  const query = useQuery({
    queryKey: modelsKeys.aibomGenerate(activeTenant, ownedRef, format),
    queryFn: () =>
      modelsApi.generateAibom(
        ownedRef,
        format === 'spdx' ? { format: 'spdx' } : undefined,
      ),
    enabled: open,
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-3xl overflow-hidden">
        <DialogHeader>
          <DialogTitle>{t('documents.aibom.previewTitle')}</DialogTitle>
          <DialogDescription>
            {t('documents.aibom.previewBody')}
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-2">
          <Button
            variant={format === 'cyclonedx' ? 'secondary' : 'ghost'}
            size="sm"
            onClick={() => setFormat('cyclonedx')}
          >
            {t('documents.aibom.cyclonedx')}
          </Button>
          <Button
            variant={format === 'spdx' ? 'secondary' : 'ghost'}
            size="sm"
            onClick={() => setFormat('spdx')}
          >
            {t('documents.aibom.spdx')}
          </Button>
        </div>
        <JsonPreview
          isLoading={query.isLoading}
          error={query.error}
          data={query.data}
        />
      </DialogContent>
    </Dialog>
  )
}

/** The live model-card preview — a structured, honest render of the generated card. */
function ModelCardPreviewDialog({
  ownedRef,
  open,
  onOpenChange,
}: {
  ownedRef: string
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('model-ops')
  const { activeTenant } = useAuth()

  const query = useQuery({
    queryKey: modelsKeys.modelCard(activeTenant, ownedRef),
    queryFn: () => modelsApi.modelCard(ownedRef),
    enabled: open,
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t('documents.card.previewTitle')}</DialogTitle>
          <DialogDescription>
            {t('documents.card.previewBody')}
          </DialogDescription>
        </DialogHeader>
        {query.isLoading ? (
          <Skeleton className="h-64 w-full" />
        ) : query.error ? (
          <p className="text-sm text-muted-foreground">
            {t('documents.loadError')}
          </p>
        ) : query.data ? (
          <ModelCardView card={query.data} />
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

function ModelCardView({ card }: { card: ModelCardDoc }) {
  const { t } = useTranslation('model-ops')
  const training = Array.isArray(card.training_data) ? card.training_data : null

  return (
    <div className="flex flex-col gap-4">
      <KvList>
        <KvRow label={t('documents.card.name')}>
          {card.model_details.name}
        </KvRow>
        <KvRow label={t('documents.card.kind')} mono>
          {card.model_details.kind}
        </KvRow>
        <KvRow label={t('documents.card.status')}>
          {card.model_details.status}
        </KvRow>
        <KvRow label={t('documents.card.intendedUse')} align="start">
          {card.intended_use}
        </KvRow>
      </KvList>

      <div>
        <h4 className="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {t('documents.card.versions')}
        </h4>
        {card.model_details.versions.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            {t('documents.card.noVersions')}
          </p>
        ) : (
          <ul className="flex flex-col divide-y divide-border rounded-md border border-border">
            {card.model_details.versions.map((v) => (
              <li
                key={v.version}
                className="flex items-center justify-between gap-2 px-3 py-1.5"
              >
                <span className="font-mono text-xs">{v.version}</span>
                <span className="flex items-center gap-1">
                  {v.admission_recorded ? (
                    v.signature_verified ? (
                      <Badge variant="success">
                        {t('documents.card.admitted')}
                      </Badge>
                    ) : (
                      <Badge variant="danger">
                        {t('documents.card.notVerified')}
                      </Badge>
                    )
                  ) : (
                    <Badge variant="neutral">
                      {t('documents.card.noVerdict')}
                    </Badge>
                  )}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div>
        <h4 className="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {t('documents.card.trainingData')}
        </h4>
        {training && training.length > 0 ? (
          <ul className="flex flex-col gap-1">
            {training.map((d) => (
              <li key={d.name} className="flex items-center gap-2 text-xs">
                <span className="font-medium">{d.name}</span>
                {d.classification && (
                  <Badge variant="outline" className="text-[11px]">
                    {d.classification}
                  </Badge>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-xs text-muted-foreground">
            {t('documents.card.notRecorded')}
          </p>
        )}
      </div>

      <KvList>
        <KvRow label={t('documents.card.admissionsRecorded')}>
          {card.provenance_and_admission.signed_admissions_recorded}
        </KvRow>
        <KvRow label={t('documents.card.admissionsVerified')}>
          {card.provenance_and_admission.signed_admissions_verified}
        </KvRow>
        <KvRow label={t('documents.card.gpaiRecorded')}>
          {card.provenance_and_admission.supplier_gpai_posture_recorded
            ? t('documents.card.yes')
            : t('documents.card.no')}
        </KvRow>
      </KvList>

      <p className="rounded-md border border-border bg-muted/50 px-3 py-2 text-[11px] text-muted-foreground">
        {card.disclaimer}
      </p>
    </div>
  )
}

/** Renders opaque JSON, read-only, in a horizontally-scrollable pane (the body never
 *  scrolls sideways). */
function JsonPreview({
  isLoading,
  error,
  data,
}: {
  isLoading: boolean
  error: unknown
  data: unknown
}) {
  const { t } = useTranslation('model-ops')
  if (isLoading) return <Skeleton className="h-64 w-full" />
  if (error)
    return (
      <p className="text-sm text-muted-foreground">
        {t('documents.loadError')}
      </p>
    )
  return (
    <div className="max-h-[60vh] overflow-auto rounded-md border border-border bg-muted/30">
      <pre className="min-w-0 whitespace-pre p-3 font-mono text-[11px] leading-relaxed">
        {JSON.stringify(data, null, 2)}
      </pre>
    </div>
  )
}
