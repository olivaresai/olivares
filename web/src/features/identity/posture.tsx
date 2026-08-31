// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ANT2-04/06 + — read-only posture: External Keys/CMEK and Workspace
// residency (ingest), plus the panel's own cert-manager TLS and crypto-agility/
// PQC key inventory. These flow on the connector bus / are not yet HTTP-served
// (External-Keys/residency), and has NOT built the TLS/PQC posture backend at all
// (verified ABSENT) — so every section here sits behind a DECLARED interface and says
// so honestly. Posture, never management.
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
import type {
  CryptoInventoryItem,
  ExternalKeyRef,
  WorkspaceResidency,
} from './types'

export function PostureTab() {
  return (
    <div className="flex flex-col gap-6">
      <ExternalKeysSection />
      <ResidencySection />
      <TlsPostureSection />
      <CryptoInventorySection />
      <AuthorityReferences
        area="posture"
        keys={['externalKeys', 'workspaceUpdate', 'certManager', 'pqc']}
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
          <span className="font-mono text-xs break-all">{row.original.id}</span>
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
            <p className="text-sm text-muted-foreground">
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
                      <span className="font-mono text-xs break-all">
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

function TlsPostureSection() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant } = useAuth()
  const q = useQuery({
    queryKey: identityKeys.tls(activeTenant),
    queryFn: () => identityApi.tlsPosture(),
    retry: false,
  })
  return (
    <SectionCard
      title={t('posture.tls.title')}
      description={t('posture.tls.description')}
    >
      <DeclaredSection
        query={q}
        what={t('posture.tls.seamWhat')}
        skeletonHeight={100}
      >
        {(p) => (
          <KvList>
            <KvRow label={t('posture.tls.issuer')}>{p.issuer ?? '—'}</KvRow>
            <KvRow label={t('posture.tls.notAfter')}>
              {p.not_after ? <RelTimeLabel ts={p.not_after} /> : '—'}
            </KvRow>
            <KvRow label={t('posture.tls.autoRenew')}>
              <StatusBadge status={p.auto_renew ? 'enabled' : 'disabled'} />
            </KvRow>
            {p.chain && p.chain.length > 0 ? (
              <KvRow label={t('posture.tls.chain')} align="start">
                {p.chain.join(' → ')}
              </KvRow>
            ) : null}
          </KvList>
        )}
      </DeclaredSection>
    </SectionCard>
  )
}

function CryptoInventorySection() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant } = useAuth()
  const q = useQuery({
    queryKey: identityKeys.crypto(activeTenant),
    queryFn: () => identityApi.cryptoInventory(),
    retry: false,
  })
  const columns = useMemo<TableColumn<CryptoInventoryItem>[]>(
    () => [
      { accessorKey: 'usage', header: t('posture.crypto.col.usage') },
      {
        accessorKey: 'algorithm',
        header: t('posture.crypto.col.algorithm'),
        cell: ({ row }) => (
          <span className="font-mono text-xs">{row.original.algorithm}</span>
        ),
      },
      {
        accessorKey: 'family',
        header: t('posture.crypto.col.family'),
        cell: ({ row }) => (
          <Badge
            variant={row.original.family === 'pqc' ? 'success' : 'neutral'}
          >
            {t(`posture.crypto.family.${row.original.family}`, {
              defaultValue: row.original.family,
            })}
          </Badge>
        ),
      },
      {
        id: 'pqcReady',
        header: t('posture.crypto.col.pqcReady'),
        enableSorting: false,
        cell: ({ row }) =>
          row.original.pqc_ready ? (
            <Badge variant="success">{t('posture.crypto.ready')}</Badge>
          ) : (
            <Badge variant="warning">{t('posture.crypto.notReady')}</Badge>
          ),
      },
    ],
    [t],
  )
  return (
    <SectionCard
      title={t('posture.crypto.title')}
      description={t('posture.crypto.description')}
    >
      <DeclaredSection
        query={q}
        what={t('posture.crypto.seamWhat')}
        skeletonHeight={120}
      >
        {(data) => (
          <DataTable
            columns={columns}
            data={data.items}
            getRowId={(c) => c.id}
            label={t('posture.crypto.title')}
            empty={
              <EmptyState
                title={t('empty.cryptoInventory.title')}
                description={t('empty.cryptoInventory.description')}
              />
            }
          />
        )}
      </DeclaredSection>
    </SectionCard>
  )
}
