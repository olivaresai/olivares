// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Platforms & lifecycle (ANT2-01/17/03) — the container. Two tabs: the
// multi-surface deploy/compliance matrix and the per-platform model lifecycle.
//
// LIVE since: the reference is served by GET /v1/m/models/platforms
// (modules/models/platforms.go ← cmd adapter ← connectors/claude-api accessors), so
// the view queries it and interpolates the AsOf/source citations FROM the response —
// it no longer imports the Go-mirrored *.data.ts arrays. The route always answers
// 200; `available:false` renders an honest unavailable notice, never an empty
// matrix (rate-limits precedent). The ONE web-declared remnant is
// LIFECYCLE_NOTES (lifecycle.data.ts): bedrock.go honesty facts the endpoint does
// not serve. The view PRESENTS, never recomputes, never fabricates (ARCHITECTURE.md/§5).
import { Layers } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '@/lib/auth/context'
import {
  AsyncSection,
  CaveatNotice,
  EffectiveStateLinks,
  IntelPage,
  SectionCard,
} from '@/features/_intel'
import { platformsApi, platformsKeys } from './api'
import {
  ApiSupportMatrix,
  LifecycleMatrix,
  LifecycleNotes,
  ParamDeprecationCard,
  SurfaceMatrix,
  SurfaceNotes,
} from './components'
import { LIFECYCLE_NOTES } from './lifecycle.data'
import type { PlatformsReference } from './types'
import './i18n'

export function PlatformsView() {
  const { t } = useTranslation(['platforms', 'common'])
  // Read-only reference; activeTenant scopes the query cache. No privileged write
  // here — the lead gates the route on `models:read`.
  const { activeTenant } = useAuth()

  const referenceQ = useQuery({
    queryKey: platformsKeys.reference(activeTenant),
    queryFn: () => platformsApi.reference(),
  })

  return (
    <IntelPage
      icon={Layers}
      title={t('title')}
      description={t('description')}
      //this page says what each PROVIDER supports and when its models retire.
      // The estate's own answer ("which of these do we run, and are any deprecated
      // here?") lives in the catalog and in model operations, so say so and link it.
      notices={
        <EffectiveStateLinks
          label={t('effectiveState.label')}
          targets={[
            {
              to: '/models',
              permission: 'models:catalog:read',
              label: t('effectiveState.models'),
            },
            {
              to: '/model-operations',
              permission: 'models:registry:read',
              label: t('effectiveState.modelOps'),
            },
          ]}
        />
      }
    >
      <Tabs defaultValue="surfaces">
        <TabsList>
          <TabsTrigger value="surfaces">{t('tabs.surfaces')}</TabsTrigger>
          <TabsTrigger value="lifecycle">{t('tabs.lifecycle')}</TabsTrigger>
        </TabsList>

        {/* --- Surfaces & compliance (ANT2-01/17) ------------------------- */}
        <TabsContent value="surfaces" className="flex flex-col gap-4">
          <AsyncSection query={referenceQ} skeletonHeight={320}>
            {(data) =>
              data.available ? (
                <div className="flex flex-col gap-4">
                  <CaveatNotice tone="info">
                    {t('liveReference', {
                      date: data.surfaces_as_of,
                      source: data.surfaces_source,
                    })}
                  </CaveatNotice>

                  <SectionCard
                    title={t('surfaces.title')}
                    description={t('surfaces.description')}
                    noPadding
                  >
                    <div className="p-4">
                      <SurfaceMatrix surfaces={data.surfaces} />
                    </div>
                  </SectionCard>

                  <SectionCard
                    title={t('surfaces.apiSupportTitle')}
                    description={t('surfaces.apiSupportDescription')}
                  >
                    <ApiSupportMatrix surfaces={data.surfaces} />
                  </SectionCard>

                  <SectionCard
                    title={t('surfaces.compliance.title')}
                    description={t('surfaces.compliance.description')}
                  >
                    <CaveatNotice tone="neutral" className="mb-3">
                      {t('surfaces.compliance.hipaaNote')}
                    </CaveatNotice>
                    <SurfaceNotes surfaces={data.surfaces} />
                  </SectionCard>
                </div>
              ) : (
                <ReferenceUnavailableNotice reason={data.reason} />
              )
            }
          </AsyncSection>
        </TabsContent>

        {/* --- Model lifecycle (ANT2-03) ---------------------------------- */}
        <TabsContent value="lifecycle" className="flex flex-col gap-4">
          <AsyncSection query={referenceQ} skeletonHeight={320}>
            {(data) =>
              data.available ? (
                <div className="flex flex-col gap-4">
                  <CaveatNotice tone="info">
                    {t('liveReference', {
                      date: data.lifecycle_as_of,
                      source: data.lifecycle_source,
                    })}
                  </CaveatNotice>

                  <SectionCard
                    title={t('lifecycle.title')}
                    description={t('lifecycle.description')}
                    noPadding
                  >
                    <div className="p-4">
                      <LifecycleMatrix lifecycles={data.lifecycles} />
                    </div>
                  </SectionCard>

                  <SectionCard title={t('lifecycle.paramDeprecation.title')}>
                    <ParamDeprecationCard dep={data.param_deprecation} />
                  </SectionCard>

                  <SectionCard title={t('lifecycle.notes.title')}>
                    {/* The notes stay WEB-DECLARED (bedrock.go facts the endpoint
                        does not serve) — labelled as such, never passed off as part
                        of the live response. */}
                    <CaveatNotice tone="neutral" className="mb-3">
                      {t('lifecycle.notes.declaredNote')}
                    </CaveatNotice>
                    <LifecycleNotes notes={LIFECYCLE_NOTES} />
                  </SectionCard>
                </div>
              ) : (
                <ReferenceUnavailableNotice reason={data.reason} />
              )
            }
          </AsyncSection>
        </TabsContent>
      </Tabs>
    </IntelPage>
  )
}

/** 200 with available=false — honest unavailability with the backend's reason,
 *  never an empty matrix presented as "no surfaces". */
function ReferenceUnavailableNotice({
  reason,
}: {
  reason: PlatformsReference['reason']
}) {
  const { t } = useTranslation('platforms')
  return (
    <CaveatNotice tone="warning">
      {t('unavailable')}
      {reason ? (
        <>
          {' — '}
          <span className="text-muted-foreground">{reason}</span>
        </>
      ) : null}
    </CaveatNotice>
  )
}
