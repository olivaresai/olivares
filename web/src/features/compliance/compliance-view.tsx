// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Stored evidence and operational records are shared across editions. The build-time
// panel seam supplies the Business Compliance Packs page. Server routes enforce RBAC.
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
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import {
  AsyncSection,
  CaveatNotice,
  DisclaimerNote,
  IntelPage,
  SectionCard,
} from '@/features/_intel'
import { complianceApi, complianceKeys } from './api'
import { ErasureTab } from './erasure-view'
import { HoldsTab } from './holds-view'
import { RetentionTab } from './retention-view'
import { EvidenceCard, ResidencyCard, RiskTable } from './components'
import type {
  EvidenceExportFormat,
  RiskClassification,
  RiskTier,
} from './types'
import './i18n'
import { downloadBlob } from '@/lib/api/download'
import { PANEL_EXTENSIONS } from '@/features/extensions'
import { useOfferedPanels } from '@/features/panels'

const RISK_TIERS: RiskTier[] = ['unacceptable', 'high', 'limited', 'minimal']

export function ComplianceView() {
  const Page = PANEL_EXTENSIONS.complianceView ?? CommunityComplianceView
  return <Page />
}

function CommunityComplianceView() {
  const { t } = useTranslation('compliance')
  const { can } = useAuth()
  const extensionTabs = useOfferedPanels(PANEL_EXTENSIONS.complianceTabs)

  return (
    <IntelPage
      icon={ScrollText}
      title={t('title')}
      description={t('description')}
    >
      <Tabs defaultValue="evidence">
        <TabsList>
          <TabsTrigger value="evidence">{t('tabs.evidence')}</TabsTrigger>
          <TabsTrigger value="risk">{t('tabs.risk')}</TabsTrigger>
          <TabsTrigger value="residency">{t('tabs.residency')}</TabsTrigger>
          <TabsTrigger value="retention">{t('tabs.retention')}</TabsTrigger>
          <TabsTrigger value="holds">{t('tabs.holds')}</TabsTrigger>
          <TabsTrigger value="erasure">{t('tabs.erasure')}</TabsTrigger>
          {extensionTabs.map((p) => (
            <TabsTrigger key={p.id} value={p.id}>
              {p.label()}
            </TabsTrigger>
          ))}
        </TabsList>

        <TabsContent value="evidence" className="flex flex-col gap-4">
          <StoredEvidenceTab canExport={can('compliance:framework:read')} />
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

        {extensionTabs.map((p) => (
          <TabsContent key={p.id} value={p.id} className="flex flex-col gap-4">
            <p.Component />
          </TabsContent>
        ))}
      </Tabs>
    </IntelPage>
  )
}

function StoredEvidenceTab({ canExport }: { canExport: boolean }) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [busy, setBusy] = useState<EvidenceExportFormat | null>(null)
  const evidenceQ = useQuery({
    queryKey: complianceKeys.evidence(activeTenant),
    queryFn: () => complianceApi.evidence(),
  })
  const handleExport = async (id: string, format: EvidenceExportFormat) => {
    setBusy(format)
    try {
      const res = await complianceApi.exportEvidence(id, format)
      downloadBlob(
        new Blob([res.text], {
          type: res.content_type || 'application/octet-stream',
        }),
        res.filename,
      )
      toast.success(t('export.done', { format: t(`export.format.${format}`) }))
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error))
    } finally {
      setBusy(null)
    }
  }
  return (
    <SectionCard
      title={t('evidence.title')}
      description={t('evidence.description')}
    >
      <AsyncSection query={evidenceQ} skeletonHeight={200}>
        {(res) =>
          res.items.length === 0 ? (
            <EmptyState
              icon={<FileCheck2 />}
              title={t('evidence.empty')}
              description={t('evidence.portableHint')}
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
  )
}

export function RiskTab({ canReview }: { canReview: boolean }) {
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
        <CaveatNotice tone="warning" className="mb-3">
          {t('risk.unacceptableNote')}
        </CaveatNotice>
        <AsyncSection query={riskQ} skeletonHeight={220}>
          {(list) =>
            list.items.length === 0 ? (
              <EmptyState
                description={t('risk.emptyHint')}
                icon={<ShieldQuestion />}
                title={t('risk.empty')}
              />
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
          <p className="text-caption text-muted-foreground">
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

export function ResidencyTab({ canScan }: { canScan: boolean }) {
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
              description={t('residency.emptyHint')}
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
