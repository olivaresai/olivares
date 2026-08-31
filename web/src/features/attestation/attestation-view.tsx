// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Supply-chain attestation — the container. Two provenance classes:
//
//  • MEASURED (live, AsyncSection) — GET /v1/m/observability/attestation: what the
//    RUNNING binary proves about itself + the MEASURED release state. Since
//    2026-08-13 that state has two reachable polarities (the backend used to emit
//    not_published for every build in existence), and ReleaseStatePanel was already
//    data-driven, so it renders either without a change. No fixture is rendered.
//  • DECLARED (attestation.data.ts, consumed directly) — the release-verification
//    CONTRACT (what ships, verify commands, SLAs). The engine cannot observe
//    repository or CI state, so this half stays declared reference with its AsOf
//    notice, exactly as honest as before.
//
// The prominent beta disclaimer is no longer unconditional (2026-08-14): it asserts
// "no releases exist yet", which the live panel beside it can refute, so it is gated
// on that same measured fact and the two can no longer contradict each other on one
// screen. Nothing is re-verified cryptographically in the browser (ARCHITECTURE.md —
// present, never recompute — never fabricate).
import { ShieldCheck } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '@/lib/auth/context'
import {
  AsyncSection,
  CaveatNotice,
  IntelPage,
  SectionCard,
  SelfAuditNotice,
} from '@/features/_intel'
import { ForbiddenState } from '@/components/ui/error-state'
import {
  AIRGAP_CONTRACT,
  ATTESTATION_AS_OF,
  HELM_CHART_CONTRACT,
  IDENTITY_REGEXP,
  OIDC_ISSUER,
  PATCH_VELOCITY,
  RELEASE_ARTIFACTS,
  SBOM_CONTRACT,
  SCORECARD_CONTRACT,
  SLSA_PROVENANCE,
  VEX_CONTRACT,
} from './attestation.data'
import { attestationApi, attestationKeys } from './api'
import {
  AirgapPanel,
  ArtifactTable,
  BinaryPanel,
  ReleaseStatePanel,
  SbomPanel,
  ScorecardPanel,
  SlaPanel,
  SlsaPanel,
  VexPanel,
} from './components'
import './i18n'

export function AttestationView() {
  const { t } = useTranslation(['attestation', 'common'])
  const { activeTenant, can } = useAuth()

  // Privileged, detective read — gate on compliance:read (the lead wires the registry
  // permission). A denied read shows a calm boundary, never a red error.
  const canRead = can('observability:attestation:read')

  const binaryQ = useQuery({
    queryKey: attestationKeys.binary(activeTenant),
    queryFn: () => attestationApi.binary(),
    enabled: canRead,
  })

  // The ONE fact both the notice and the live panel read, so neither can say
  // something the other denies. `=== true` on purpose: undefined (loading, error,
  // denied) is not "published", and the beta notice stays up.
  const showsStampedRelease = binaryQ.data?.release.published === true

  if (!canRead) {
    return (
      <IntelPage
        icon={ShieldCheck}
        title={t('title')}
        description={t('description')}
      >
        <SectionCard>
          <div className="flex min-h-40 items-center justify-center">
            <ForbiddenState
              title={t('forbiddenTitle')}
              description={t('forbidden')}
            />
          </div>
        </SectionCard>
      </IntelPage>
    )
  }

  return (
    <IntelPage
      icon={ShieldCheck}
      title={t('title')}
      description={t('description')}
      notices={
        <>
          {/* PROMINENT beta disclaimer — now GATED ON THE SAME FACT the panel beside
              it renders, so the two cannot contradict each other by construction.
              Its first clause ("no releases exist yet") is the one claim on this page
              that a release-stamped binary refutes, and until 2026-08-14 it was shown
              unconditionally next to a live badge saying Published: two mutually
              exclusive propositions on one screen (Codex sol max contrast, PR #730).

              Gating beats splitting HERE because splitting needs a new string in all
              seven locales, which CLAUDE.md routes through Codex sol max (a
              mechanical translation inverted deny-closed polarity on five pages), and
              gating needs none. Nothing evergreen is lost: the never-re-verify-in-
              the-browser honesty is ALSO in this page's own description, rendered
              unconditionally above (i18n `description`), and the self-declared
              provenance of the positive state arrives with the badge from the
              backend (ReleaseStatePanel).

              DENY-CLOSED while loading, on error, and on any non-positive state: the
              disclaimer shows unless the engine has actually answered `published`. */}
          {!showsStampedRelease && (
            <CaveatNotice tone="warning">
              <span className="font-semibold text-warning">
                {t('disclaimer.tag')}
              </span>{' '}
              {t('disclaimer.body')}
            </CaveatNotice>
          )}
          {/* Privileged, self-audited read. */}
          <SelfAuditNotice />
        </>
      }
    >
      {/* --- MEASURED: the running binary (live) --------------------------- */}
      <SectionCard title={t('live.title')} description={t('live.description')}>
        <AsyncSection query={binaryQ} skeletonHeight={260}>
          {(data) => (
            <div className="grid gap-6 lg:grid-cols-2">
              <BinaryPanel binary={data.binary} capturedAt={data.captured_at} />
              <ReleaseStatePanel
                release={data.release}
                pipeline={data.pipeline}
              />
            </div>
          )}
        </AsyncSection>
      </SectionCard>

      {/* --- DECLARED: the release-verification contract -------------------- */}
      <CaveatNotice tone="info">
        {t('disclaimer.declaredRef', { date: ATTESTATION_AS_OF })}
      </CaveatNotice>

      <Tabs defaultValue="artifacts">
        <TabsList>
          <TabsTrigger value="artifacts">{t('tabs.artifacts')}</TabsTrigger>
          <TabsTrigger value="provenance">{t('tabs.provenance')}</TabsTrigger>
          <TabsTrigger value="sbom">{t('tabs.sbom')}</TabsTrigger>
          <TabsTrigger value="scorecard">{t('tabs.scorecard')}</TabsTrigger>
          <TabsTrigger value="remediation">{t('tabs.remediation')}</TabsTrigger>
          <TabsTrigger value="airgap">{t('tabs.airgap')}</TabsTrigger>
        </TabsList>

        <TabsContent value="artifacts" className="flex flex-col gap-4">
          <SectionCard
            title={t('artifacts.title')}
            description={t('artifacts.description')}
            noPadding
          >
            <div className="p-4">
              {/* The keyless identity facts every verify command pins against. */}
              <CaveatNotice tone="neutral" className="mb-3">
                {t('artifacts.identityNote', {
                  issuer: OIDC_ISSUER,
                  regexp: IDENTITY_REGEXP,
                })}
              </CaveatNotice>
              <ArtifactTable artifacts={RELEASE_ARTIFACTS} />
            </div>
          </SectionCard>
        </TabsContent>

        <TabsContent value="provenance" className="flex flex-col gap-4">
          <SectionCard
            title={t('slsa.title')}
            description={t('slsa.description')}
          >
            <SlsaPanel slsa={SLSA_PROVENANCE} />
          </SectionCard>
          <SectionCard
            title={t('vex.title')}
            description={t('vex.description')}
          >
            <VexPanel vex={VEX_CONTRACT} />
          </SectionCard>
        </TabsContent>

        <TabsContent value="sbom" className="flex flex-col gap-4">
          <SectionCard
            title={t('sbom.title')}
            description={t('sbom.description')}
          >
            <SbomPanel sbom={SBOM_CONTRACT} />
          </SectionCard>
        </TabsContent>

        <TabsContent value="scorecard" className="flex flex-col gap-4">
          <SectionCard
            title={t('scorecard.title')}
            description={t('scorecard.description')}
          >
            <ScorecardPanel scorecard={SCORECARD_CONTRACT} />
          </SectionCard>
        </TabsContent>

        <TabsContent value="remediation" className="flex flex-col gap-4">
          <SectionCard
            title={t('sla.title')}
            description={t('sla.description')}
          >
            <SlaPanel pv={PATCH_VELOCITY} />
          </SectionCard>
        </TabsContent>

        <TabsContent value="airgap" className="flex flex-col gap-4">
          <SectionCard
            title={t('airgap.title')}
            description={t('airgap.description')}
          >
            <AirgapPanel airgap={AIRGAP_CONTRACT} helm={HELM_CHART_CONTRACT} />
          </SectionCard>
        </TabsContent>
      </Tabs>
    </IntelPage>
  )
}
