// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ANT2-04/06 — read-only posture: External Keys/CMEK and Workspace residency (ingest), with their honest unavailable states. Posture, never management.
//
// The cert-manager TLS and PQC key inventory sections are gone (EU-CB08, 09b sweep):
// their routes were never built, and reading them on every visit made 6 GET 404s and
// console errors per open, under cards that said "Backend pending".
import { ModuleGate } from '@/components/layout/query-error-state'
import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { SectionCard } from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { StatusBadge } from '@/components/data/badges'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { KvList, KvRow } from '@/components/ui/kv'
import { EmptyState } from '@/components/ui/empty-state'
import { useAuth } from '@/lib/auth/context'
import { identityApi, identityKeys } from './api'
import { DeclaredSection, PostureUnavailableNotice } from './components'
import { AuthorityReferences } from './references'
import type { ExternalKeyRef, WorkspaceResidency } from './types'

export function PostureTab() {
  // Keys and residency posture are read from the identity module (EU18).
  return (
    <ModuleGate module="identity">
      <PostureSections />
    </ModuleGate>
  )
}

function PostureSections() {
  return (
    <div className="flex flex-col gap-6">
      <ExternalKeysSection />
      <ResidencySection />
      <AuthorityReferences
        area="posture"
        keys={['externalKeys', 'workspaceUpdate']}
      />
    </div>
  )
}

function ExternalKeysSection() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant } = useAuth()
  const q = useQuery({
    queryKey: identityKeys.externalKeys(activeTenant),
    queryFn: () => identityApi.externalKeys(),
    retry: false,
  })
  const columns = useMemo<TableColumn<ExternalKeyRef>[]>(
    () => [
      {
        accessorKey: 'id',
        header: t('posture.ek.col.id'),
        cell: ({ row }) => (
          <span className="font-mono text-caption break-all">
            {row.original.id}
          </span>
        ),
      },
      {
        accessorKey: 'provider',
        header: t('posture.ek.col.provider'),
        cell: ({ row }) => (
          <Badge variant="outline">{row.original.provider}</Badge>
        ),
      },
      {
        id: 'state',
        header: t('posture.ek.col.state'),
        enableSorting: false,
        cell: ({ row }) => (
          <StatusBadge status={row.original.state ?? 'unknown'} />
        ),
      },
      {
        id: 'inUse',
        header: t('posture.ek.col.inUse'),
        enableSorting: false,
        cell: ({ row }) =>
          row.original.in_use ? (
            <Badge variant="neutral">{t('posture.ek.immutable')}</Badge>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        id: 'validated',
        header: t('posture.ek.col.validated'),
        enableSorting: false,
        cell: ({ row }) =>
          row.original.last_validated_at ? (
            <RelTimeLabel ts={row.original.last_validated_at} />
          ) : (
            <span className="text-muted-foreground">
              {t('posture.ek.never')}
            </span>
          ),
      },
    ],
    [t],
  )
  return (
    <SectionCard
      title={t('posture.ek.title')}
      description={t('posture.ek.description')}
    >
      <DeclaredSection
        query={q}
        what={t('posture.ek.seamWhat')}
        skeletonHeight={120}
      >
        {(data) =>
          data.available === false ? (
            <PostureUnavailableNotice
              reason={data.reason ?? t('posture.unavailable.fallback')}
            />
          ) : (
            <DataTable
              columns={columns}
              data={data.items}
              getRowId={(k) => k.id}
              label={t('posture.ek.title')}
              empty={
                <EmptyState
                  title={t('empty.externalKeys.title')}
                  description={t('empty.externalKeys.description')}
                />
              }
            />
          )
        }
      </DeclaredSection>
    </SectionCard>
  )
}

function ResidencySection() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant } = useAuth()
  const q = useQuery({
    queryKey: identityKeys.residency(activeTenant),
    queryFn: () => identityApi.workspaceResidency(),
    retry: false,
  })
  return (
    <SectionCard
      title={t('posture.residency.title')}
      description={t('posture.residency.description')}
    >
      <DeclaredSection
        query={q}
        what={t('posture.residency.seamWhat')}
        skeletonHeight={120}
      >
        {(data) =>
          data.available === false ? (
            <PostureUnavailableNotice
              reason={data.reason ?? t('posture.unavailable.fallback')}
            />
          ) : data.items.length === 0 ? (
            <p className="text-body text-muted-foreground">
              {t('posture.residency.none')}
            </p>
          ) : (
            <div className="flex flex-col gap-3">
              {data.items.map((w: WorkspaceResidency) => (
                <KvList
                  key={w.id}
                  className="rounded-md border border-border p-3"
                >
                  <KvRow
                    label={t('posture.residency.workspace')}
                    mono
                    align="start"
                  >
                    {w.name ?? w.id}
                  </KvRow>
                  <KvRow label={t('posture.residency.geo')}>
                    {w.geo ? <Badge variant="neutral">{w.geo}</Badge> : '—'}
                  </KvRow>
                  <KvRow label={t('posture.residency.cmek')} align="start">
                    {w.external_key_id ? (
                      <span className="font-mono text-caption break-all">
                        {w.external_key_id}
                      </span>
                    ) : (
                      <Badge variant="warning">
                        {t('posture.residency.providerManaged')}
                      </Badge>
                    )}
                  </KvRow>
                  {w.compartment_id ? (
                    <KvRow
                      label={t('posture.residency.compartment')}
                      mono
                      align="start"
                    >
                      {w.compartment_id}
                    </KvRow>
                  ) : null}
                  <KvRow
                    label={t('posture.residency.inferenceGeos')}
                    align="start"
                  >
                    {(w.data_residency?.allowed_inference_geos ?? []).length > 0
                      ? w.data_residency?.allowed_inference_geos?.join(', ')
                      : t('posture.residency.unrestricted')}
                  </KvRow>
                </KvList>
              ))}
            </div>
          )
        }
      </DeclaredSection>
    </SectionCard>
  )
}
