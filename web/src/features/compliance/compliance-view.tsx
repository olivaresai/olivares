// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Compliance (module XIII) — the container. Tabs over posture / gaps / evidence / risk
// / residency. It wires the queries, the framework selector, and the privileged writes
// (seal evidence, review risk, scan residency), each RBAC-gated. It presents control
// status + evidence; it NEVER says "compliant"/"certified" and NEVER recomputes a
// verdict (the engine does — docs/SECURITY-HARDENING.md). Every response's `disclaimer` is rendered,
// and the privileged, self-audited reads (evidence, risk) carry a SelfAuditNotice.
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { FileCheck2, ScrollText, ShieldQuestion } from 'lucide-react'
import { useTranslation } from 'react-i18next'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { CapabilityCatalog } from './capability-catalog'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import {
  AsyncSection,
  CaveatNotice,
  DisclaimerNote,
  IntelPage,
  SectionCard,
  SelfAuditNotice,
} from '@/features/_intel'
import { complianceApi, complianceKeys } from './api'
import { CalendarTab } from './calendar-view'
import { ErasureTab } from './erasure-view'
import { HoldsTab } from './holds-view'
import { HipaaTab } from './hipaa-view'
import { Nis2Tab } from './nis2-view'
import { RegOpsTab } from './regops-view'
import { RetentionTab } from './retention-view'
import {
  ControlList,
  EvidenceCard,
  FrameworkDisclaimerBanner,
  FrameworkRollupList,
  GapList,
  OscalFindingsPreview,
  ResidencyCard,
  RiskTable,
} from './components'
import type {
  EvidenceExportFormat,
  EvidenceExportResult,
  RiskClassification,
  RiskTier,
} from './types'
import './i18n'

const RISK_TIERS: RiskTier[] = ['unacceptable', 'high', 'limited', 'minimal']

export function ComplianceView() {
  const { t } = useTranslation('compliance')
  const { activeTenant, can } = useAuth()
  const [framework, setFramework] = useState<string | null>(null)

  const summaryQ = useQuery({
    queryKey: complianceKeys.summary(activeTenant),
    queryFn: () => complianceApi.summary(),
  })

  // Default the selected framework to the first one the roll-up returns.
  const selectedFramework =
    framework ?? summaryQ.data?.frameworks[0]?.framework ?? null

  return (
    <IntelPage
      icon={ScrollText}
      title={t('title')}
      description={t('description')}
      notices={
        summaryQ.data ? (
          <DisclaimerNote text={summaryQ.data.disclaimer} />
        ) : null
      }
    >
      <Tabs defaultValue="posture">
        <TabsList>
          <TabsTrigger value="posture">{t('tabs.posture')}</TabsTrigger>
          <TabsTrigger value="gaps">{t('tabs.gaps')}</TabsTrigger>
          <TabsTrigger value="capabilities">
            {t('tabs.capabilities')}
          </TabsTrigger>
          <TabsTrigger value="evidence">{t('tabs.evidence')}</TabsTrigger>
          <TabsTrigger value="risk">{t('tabs.risk')}</TabsTrigger>
          <TabsTrigger value="residency">{t('tabs.residency')}</TabsTrigger>
          <TabsTrigger value="retention">{t('tabs.retention')}</TabsTrigger>
          <TabsTrigger value="holds">{t('tabs.holds')}</TabsTrigger>
          <TabsTrigger value="erasure">{t('tabs.erasure')}</TabsTrigger>
          <TabsTrigger value="calendar">{t('tabs.calendar')}</TabsTrigger>
          <TabsTrigger value="regops">{t('tabs.regops')}</TabsTrigger>
          <TabsTrigger value="nis2">{t('tabs.nis2')}</TabsTrigger>
          <TabsTrigger value="hipaa">{t('tabs.hipaa')}</TabsTrigger>
        </TabsList>

        <TabsContent value="posture" className="flex flex-col gap-4">
          <SectionCard
            title={t('posture.title')}
            description={t('posture.description')}
          >
            <AsyncSection query={summaryQ} skeletonHeight={220}>
              {(summary) => (
                <>
                  <CaveatNotice tone="info" className="mb-3">
                    {t('status.byDesignHint')}
                  </CaveatNotice>
                  <FrameworkRollupList
                    frameworks={summary.frameworks}
                    selected={selectedFramework ?? undefined}
                    onSelect={setFramework}
                  />
                </>
              )}
            </AsyncSection>
          </SectionCard>

          {selectedFramework ? (
            <ControlsSection framework={selectedFramework} />
          ) : null}
        </TabsContent>

        <TabsContent value="capabilities" className="flex flex-col gap-4">
          <CapabilityCatalog />
        </TabsContent>

        <TabsContent value="gaps" className="flex flex-col gap-4">
          {selectedFramework ? (
            <GapsSection framework={selectedFramework} />
          ) : (
            <SectionCard title={t('gaps.title')}>
              <EmptyState
                icon={<ShieldQuestion />}
                title={t('framework.select')}
              />
            </SectionCard>
          )}
        </TabsContent>

        <TabsContent value="evidence" className="flex flex-col gap-4">
          <EvidenceTab
            framework={selectedFramework}
            canWrite={can('compliance:evidence:write')}
            canExport={can('compliance:framework:read')}
          />
        </TabsContent>

        <TabsContent value="risk" className="flex flex-col gap-4">
          <RiskTab canReview={can('compliance:risk:admin')} />
        </TabsContent>

        <TabsContent value="residency" className="flex flex-col gap-4">
          <ResidencyTab canScan={can('compliance:residency:write')} />
        </TabsContent>

        {/*retention schedules, sweeps and destruction certificates. Six
            engine routes since compliance.go:575-580, of which only the class
            registry had a console: the dropdown of the legal-hold dialog below.
            It sits ahead of holds because that is the order the plane works in —
            a schedule proposes the disposal, a hold overrides it, an erasure is
            the subject-driven exception. */}
        <TabsContent value="retention" className="flex flex-col gap-4">
          <RetentionTab
            canAdmin={can('compliance:retention:admin')}
            canRead={can('compliance:retention:read')}
          />
        </TabsContent>

        {/*the governed data-lifecycle surfaces. The engine has run these
            since until now the only way to reach them was curl. */}
        <TabsContent value="holds" className="flex flex-col gap-4">
          <HoldsTab
            canAdmin={can('compliance:hold:admin')}
            canRead={can('compliance:hold:read')}
          />
        </TabsContent>

        <TabsContent value="erasure" className="flex flex-col gap-4">
          <ErasureTab
            canAdmin={can('compliance:erasure:admin')}
            canRead={can('compliance:erasure:read')}
          />
        </TabsContent>

        <TabsContent value="calendar" className="flex flex-col gap-4">
          <CalendarTab framework={selectedFramework} />
        </TabsContent>

        {/*the twelve writes this tab's own banner already promised. Each
            plane gates in TWO layers, which is the house rule: the READ decides
            whether the panel renders at all, the ADMIN verb decides whether its
            generators and deletes exist. The engine requires exactly these
            (compliance.go:480-534): oscal:admin for ingestion and unregister,
            dora:admin for register generation, classification and both deletes,
            depth:admin for the two pack generators and their deletes, ccm:admin
            for snapshot and drift detection. Four `*:admin` permissions were
            never passed in, so the console could not have offered the actions
            even if the panels had been built. */}
        {/* ⛔ AIMS NO ES DEPTH, y hasta hoy la consola lo trataba como si lo fuera. El
            motor exige `compliance:aims:read|admin` en las cinco rutas `/aims/pack*`
            (`modules/compliance/compliance.go:509-513`) y el panel las servía bajo
            `depth`. Dos caras, las dos visibles: quien tenía `aims:read` y no
            `depth:read` no veía NADA aunque el motor le hubiera servido, y quien tenía
            `depth:read` y no `aims:read` veía la familia AIMS y cada llamada le daba
            403. */}
        <TabsContent value="regops" className="flex flex-col gap-4">
          <RegOpsTab
            canDoraRead={can('compliance:dora:read')}
            canDoraAdmin={can('compliance:dora:admin')}
            canOscalRead={can('compliance:oscal:read')}
            canOscalAdmin={can('compliance:oscal:admin')}
            canDepthRead={can('compliance:depth:read')}
            canDepthAdmin={can('compliance:depth:admin')}
            canAimsRead={can('compliance:aims:read')}
            canAimsAdmin={can('compliance:aims:admin')}
            canCcmRead={can('compliance:ccm:read')}
            canCcmAdmin={can('compliance:ccm:admin')}
          />
        </TabsContent>

        {/*NIS 2 Art 23 significant-incident classification. Six engine
            routes since compliance.go:547-552 that no console reached. */}
        <TabsContent value="nis2" className="flex flex-col gap-4">
          <Nis2Tab
            canAdmin={can('compliance:nis2:admin')}
            canRead={can('compliance:nis2:read')}
          />
        </TabsContent>

        {/*el informe TÉCNICO de 45 CFR §164.312, que NO es el framework
            `hipaa_clinical_ai` del catálogo genérico: `hipaaTechnicalFramework()`
            se usa en un solo sitio (hipaa.go:59) y no está en `catalog`, así que
            esta ruta era la única forma de alcanzarlo y nadie la llamaba. */}
        <TabsContent value="hipaa" className="flex flex-col gap-4">
          <HipaaTab canRead={can('compliance:framework:read')} />
        </TabsContent>
      </Tabs>
    </IntelPage>
  )
}

// --- posture: controls for the selected framework ----------------------------

function ControlsSection({ framework }: { framework: string }) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const statusQ = useQuery({
    queryKey: complianceKeys.status(activeTenant, framework),
    queryFn: () => complianceApi.status(framework),
  })
  return (
    <SectionCard
      title={t('posture.controlsTitle')}
      description={t('posture.controlsDescription')}
    >
      <AsyncSection query={statusQ} skeletonHeight={300}>
        {(res) => (
          <div className="flex flex-col gap-3">
            {/* For a design-toward crosswalk / in-development framework, the
                no-conformance-claim disclaimer is rendered PROMINENTLY up top. */}
            <FrameworkDisclaimerBanner
              framework={res.assessment.framework}
              disclaimer={res.assessment.disclaimer}
            />
            <ControlList controls={res.assessment.controls} />
            <DisclaimerNote text={res.disclaimer} />
          </div>
        )}
      </AsyncSection>
    </SectionCard>
  )
}

// --- gaps --------------------------------------------------------------------

function GapsSection({ framework }: { framework: string }) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const gapsQ = useQuery({
    queryKey: complianceKeys.gaps(activeTenant, framework),
    queryFn: () => complianceApi.gaps(framework),
  })
  return (
    <SectionCard title={t('gaps.title')} description={t('gaps.description')}>
      <AsyncSection query={gapsQ} skeletonHeight={260}>
        {(res) =>
          res.gaps.length === 0 ? (
            <EmptyState icon={<FileCheck2 />} title={t('gaps.empty')} />
          ) : (
            <div className="flex flex-col gap-3">
              <GapList gaps={res.gaps} />
              <DisclaimerNote text={res.disclaimer} />
            </div>
          )
        }
      </AsyncSection>
    </SectionCard>
  )
}

// --- evidence ----------------------------------------------------------------

/** Trigger a browser download of the EXACT server bytes (no client recompute). */
function downloadExport(res: EvidenceExportResult) {
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

function EvidenceTab({
  framework,
  canWrite,
  canExport,
}: {
  framework: string | null
  canWrite: boolean
  canExport: boolean
}) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [dialogOpen, setDialogOpen] = useState(false)
  const [busy, setBusy] = useState<EvidenceExportFormat | null>(null)
  // The most-recent OSCAL export, kept so its honest finding-status preview renders.
  const [oscalResult, setOscalResult] = useState<EvidenceExportResult | null>(
    null,
  )
  const evidenceQ = useQuery({
    queryKey: complianceKeys.evidence(activeTenant),
    queryFn: () => complianceApi.evidence(),
  })

  const handleExport = (id: string, format: EvidenceExportFormat) => {
    setBusy(format)
    complianceApi
      .exportEvidence(id, format)
      .then((res) => {
        downloadExport(res)
        if (format === 'oscal') setOscalResult(res)
        toast.success(
          t('export.done', { format: t(`export.format.${format}`) }),
        )
      })
      .catch((e: unknown) => toast.error(String((e as Error).message ?? e)))
      .finally(() => setBusy(null))
  }

  return (
    <>
      <SectionCard
        title={t('evidence.title')}
        description={t('evidence.description')}
        actions={
          canWrite && framework ? (
            <Button
              variant="primary"
              size="sm"
              onClick={() => setDialogOpen(true)}
            >
              <FileCheck2 />
              {t('evidence.generate')}
            </Button>
          ) : null
        }
      >
        {/* Sealing/reading/exporting evidence is a privileged, self-audited read. */}
        <SelfAuditNotice className="mb-3" />
        <AsyncSection query={evidenceQ} skeletonHeight={200}>
          {(res) =>
            res.items.length === 0 ? (
              <EmptyState
                icon={<FileCheck2 />}
                title={t('evidence.empty')}
                description={t('evidence.emptyHint')}
              />
            ) : (
              <div className="flex flex-col gap-3">
                {res.items.map((pkg) => (
                  <EvidenceCard
                    key={pkg.id}
                    pkg={pkg}
                    canExport={canExport}
                    exportBusy={busy}
                    onExport={handleExport}
                  />
                ))}
                <DisclaimerNote text={res.disclaimer} />
              </div>
            )
          }
        </AsyncSection>
      </SectionCard>

      {/* After an OSCAL export, show the honest finding-status preview (a by_design
          control reads "not-satisfied (by_design)", never laundered to satisfied). */}
      {oscalResult?.oscal ? (
        <SectionCard
          title={t('export.oscalSectionTitle')}
          description={t('export.oscalSectionDescription')}
        >
          <OscalFindingsPreview oscal={oscalResult.oscal} />
        </SectionCard>
      ) : null}

      {canWrite && framework ? (
        <EvidenceDialog
          framework={framework}
          open={dialogOpen}
          onOpenChange={setDialogOpen}
        />
      ) : null}
    </>
  )
}

function EvidenceDialog({
  framework,
  open,
  onOpenChange,
}: {
  framework: string
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [scopeNote, setScopeNote] = useState('')

  const seal = useMutation({
    mutationFn: () =>
      complianceApi.generateEvidence(framework, {
        scope_note: scopeNote.trim() || undefined,
      }),
    onSuccess: () => {
      toast.success(t('evidence.dialog.sealed'))
      void qc.invalidateQueries({
        queryKey: complianceKeys.evidence(activeTenant),
      })
      onOpenChange(false)
      setScopeNote('')
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('evidence.dialog.title')}</DialogTitle>
          <DialogDescription>
            {t('evidence.dialog.description')}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            seal.mutate()
          }}
        >
          <Field
            label={t('evidence.dialog.scopeNote')}
            description={t('evidence.dialog.scopeHint')}
          >
            {({ id }) => (
              <Textarea
                id={id}
                value={scopeNote}
                onChange={(e) => setScopeNote(e.target.value)}
                rows={3}
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
            <Button type="submit" variant="primary" disabled={seal.isPending}>
              {t('evidence.dialog.seal')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- risk --------------------------------------------------------------------

function RiskTab({ canReview }: { canReview: boolean }) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [reviewing, setReviewing] = useState<RiskClassification | null>(null)
  const riskQ = useQuery({
    queryKey: complianceKeys.risk(activeTenant),
    queryFn: () => complianceApi.risk(),
  })

  return (
    <>
      <SectionCard title={t('risk.title')} description={t('risk.description')}>
        <SelfAuditNotice className="mb-3" />
        <CaveatNotice tone="warning" className="mb-3">
          {t('risk.unacceptableNote')}
        </CaveatNotice>
        <AsyncSection query={riskQ} skeletonHeight={220}>
          {(list) =>
            list.items.length === 0 ? (
              <EmptyState icon={<ShieldQuestion />} title={t('risk.empty')} />
            ) : (
              <RiskTable
                rows={list.items}
                canReview={canReview}
                onReview={setReviewing}
              />
            )
          }
        </AsyncSection>
      </SectionCard>

      {canReview && reviewing ? (
        <RiskReviewDialog
          row={reviewing}
          open={reviewing !== null}
          onOpenChange={(v) => {
            if (!v) setReviewing(null)
          }}
        />
      ) : null}
    </>
  )
}

function RiskReviewDialog({
  row,
  open,
  onOpenChange,
}: {
  row: RiskClassification
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [tier, setTier] = useState<RiskTier>(row.suggested_tier)
  const [note, setNote] = useState('')

  const review = useMutation({
    mutationFn: () =>
      complianceApi.reviewRisk(row.id, {
        tier,
        note: note.trim() || undefined,
      }),
    onSuccess: () => {
      toast.success(t('risk.dialog.reviewed'))
      void qc.invalidateQueries({ queryKey: complianceKeys.risk(activeTenant) })
      onOpenChange(false)
      setNote('')
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('risk.dialog.title')}</DialogTitle>
          <DialogDescription>{t('risk.dialog.description')}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            review.mutate()
          }}
        >
          <p className="text-xs text-muted-foreground">
            {t('risk.rationale')}:{' '}
            <span className="text-foreground">{row.rationale}</span>
          </p>
          <Field label={t('risk.dialog.tier')}>
            <Select value={tier} onValueChange={(v) => setTier(v as RiskTier)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {RISK_TIERS.map((tr) => (
                  <SelectItem key={tr} value={tr}>
                    {t(`tiers.${tr}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('risk.dialog.note')}
            description={t('risk.dialog.noteHint')}
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
            <Button type="submit" variant="primary" disabled={review.isPending}>
              {t('risk.dialog.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- residency ---------------------------------------------------------------

function ResidencyTab({ canScan }: { canScan: boolean }) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const residencyQ = useQuery({
    queryKey: complianceKeys.residency(activeTenant),
    queryFn: () => complianceApi.residency(),
  })

  const scan = useMutation({
    mutationFn: () => complianceApi.scanResidency(),
    onSuccess: (report) => {
      toast.success(
        t('residency.scanned') +
          ` · ${t('residency.violations', { count: report.violations })}`,
      )
      void qc.invalidateQueries({
        queryKey: complianceKeys.residency(activeTenant),
      })
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  return (
    <SectionCard
      title={t('residency.title')}
      description={t('residency.description')}
      actions={
        canScan ? (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => scan.mutate()}
            disabled={scan.isPending}
          >
            {t('residency.scan')}
          </Button>
        ) : null
      }
    >
      <AsyncSection query={residencyQ} skeletonHeight={200}>
        {(list) =>
          list.items.length === 0 ? (
            <EmptyState
              icon={<ShieldQuestion />}
              title={t('residency.empty')}
            />
          ) : (
            <div className="grid gap-3 md:grid-cols-2">
              {list.items.map((region) => (
                <ResidencyCard key={region.id} region={region} />
              ))}
            </div>
          )
        }
      </AsyncSection>
    </SectionCard>
  )
}

export default ComplianceView
