// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Cable, Inbox, Plus, RefreshCcw } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { AsyncSection, IntelPage, SectionCard } from '@/features/_intel'
import type { TenantRequestOptions } from '@/lib/api/client'
import { useAuth } from '@/lib/auth/context'
import { useWorkspaceStore } from '@/stores/workspace'
import {
  listProtocolBindings,
  listProtocolBindingSpecs,
  protocolBindingKeys,
} from './api'
import { ProtocolBindingDetailSheet } from './binding-detail'
import {
  ProtocolComposerDialog,
  type ProtocolComposerPermissionPreview,
} from './composer-dialog'
import { ProtocolSpecDetailSheet } from './spec-detail'
import { ProtocolVerdictBadge, SpecStateBadge } from './status'
import type {
  BindingProtocol,
  ListProtocolBindingsParams,
  ListProtocolBindingSpecsParams,
  ProtocolBinding,
  ProtocolBindingSpec,
  ProtocolBindingSpecState,
  ProtocolObservationVerdict,
  ProtocolPage,
} from './types'
import './i18n'

const SPEC_STATES: ProtocolBindingSpecState[] = [
  'draft',
  'active',
  'disabled',
  'superseded',
]
const VERDICTS: ProtocolObservationVerdict[] = ['CLEAN', 'BROKEN', 'UNKNOWN']

export function ProtocolBindingsView() {
  const { t } = useTranslation('protocolBindings')
  const { activeTenant, can } = useAuth()
  const { activeWorkspace, activeWorkspaceName } = useWorkspaceStore()
  const queryClient = useQueryClient()
  const [protocol, setProtocol] = useState<BindingProtocol | 'all'>('all')
  const [specState, setSpecState] = useState<ProtocolBindingSpecState | 'all'>(
    'all',
  )
  const [verdict, setVerdict] = useState<ProtocolObservationVerdict | 'all'>(
    'all',
  )
  const [composerOperation, setComposerOperation] = useState<{
    request: TenantRequestOptions
    workspaceId: string
  } | null>(null)
  const [specId, setSpecId] = useState<string | null>(null)
  const [bindingId, setBindingId] = useState<string | null>(null)

  const specParams: ListProtocolBindingSpecsParams = useMemo(
    () => ({
      workspace_id: activeWorkspace ?? '',
      protocol: protocol === 'all' ? undefined : protocol,
      state: specState === 'all' ? undefined : specState,
      limit: 100,
    }),
    [activeWorkspace, protocol, specState],
  )
  const bindingParams: ListProtocolBindingsParams = useMemo(
    () => ({
      workspace_id: activeWorkspace ?? '',
      protocol: protocol === 'all' ? undefined : protocol,
      verdict: verdict === 'all' ? undefined : verdict,
      limit: 100,
    }),
    [activeWorkspace, protocol, verdict],
  )
  const composerCatalogParams: ListProtocolBindingSpecsParams = useMemo(
    () => ({
      workspace_id: composerOperation?.workspaceId ?? '',
      state: 'active',
      limit: 200,
    }),
    [composerOperation?.workspaceId],
  )
  const composerCatalogRequest = composerOperation?.request ?? { tenant: null }

  const specs = useQuery({
    queryKey: protocolBindingKeys.specs(activeTenant, specParams),
    queryFn: ({ signal }) =>
      listProtocolBindingSpecs(specParams, { tenant: activeTenant }, signal),
    enabled: !!activeWorkspace,
  })
  const bindings = useQuery({
    queryKey: protocolBindingKeys.bindings(activeTenant, bindingParams),
    queryFn: ({ signal }) =>
      listProtocolBindings(bindingParams, { tenant: activeTenant }, signal),
    enabled: !!activeWorkspace,
  })
  const composerCatalog = useQuery({
    queryKey: protocolBindingKeys.specs(
      composerCatalogRequest.tenant,
      composerCatalogParams,
    ),
    queryFn: ({ signal }) =>
      listProtocolBindingSpecs(
        composerCatalogParams,
        composerCatalogRequest,
        signal,
      ),
    enabled: composerOperation !== null,
  })

  const refresh = () => {
    void specs.refetch()
    void bindings.refetch()
  }
  const invalidate = (tenant: string | null = activeTenant) =>
    void queryClient.invalidateQueries({
      queryKey: protocolBindingKeys.all(tenant),
    })
  const composerPermissions: ProtocolComposerPermissionPreview = {
    createDraft: can('sessions:protocol-binding:write'),
    activate: can('sessions:protocol-binding:admin'),
    localRead: {
      work_item: can('sessions:work:read'),
      agent: can('agent:read'),
      model: can('models:catalog:read'),
      channel: can('sessions:channel:read'),
    },
  }

  return (
    <IntelPage
      icon={Cable}
      title={t('title')}
      description={t('subtitle')}
      actions={
        <>
          {can('sessions:protocol-binding:write') && activeWorkspace ? (
            <Button
              size="sm"
              onClick={() =>
                setComposerOperation({
                  request: { tenant: activeTenant },
                  workspaceId: activeWorkspace,
                })
              }
            >
              <Plus className="size-4" aria-hidden="true" />
              {t('actions.newDraft')}
            </Button>
          ) : null}
          <Button
            variant="outline"
            size="sm"
            disabled={!activeWorkspace}
            onClick={refresh}
          >
            <RefreshCcw className="size-4" aria-hidden="true" />
            {t('actions.refresh')}
          </Button>
        </>
      }
    >
      {!activeWorkspace ? (
        <EmptyState
          icon={<Cable />}
          title={t('workspace.requiredTitle')}
          description={t('workspace.requiredBody')}
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
            <span>{t('workspace.scope')}</span>
            <Badge variant="outline">
              {activeWorkspaceName || activeWorkspace}
            </Badge>
          </div>
          <Tabs defaultValue="specs">
            <TabsList>
              <TabsTrigger value="specs">{t('tabs.specs')}</TabsTrigger>
              <TabsTrigger value="bindings">{t('tabs.bindings')}</TabsTrigger>
            </TabsList>
            <TabsContent value="specs" className="mt-4">
              <SectionCard
                title={t('specs.title')}
                description={t('specs.description')}
                actions={
                  <div className="flex flex-wrap gap-2">
                    <ProtocolFilter value={protocol} onChange={setProtocol} />
                    <Select
                      value={specState}
                      onValueChange={(value) =>
                        setSpecState(value as ProtocolBindingSpecState | 'all')
                      }
                    >
                      <SelectTrigger
                        className="w-40"
                        aria-label={t('filters.state')}
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="all">
                          {t('filters.allStates')}
                        </SelectItem>
                        {SPEC_STATES.map((state) => (
                          <SelectItem key={state} value={state}>
                            {t(`state.${state}`)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                }
              >
                <AsyncSection query={specs} skeletonHeight={180}>
                  {(page) => <SpecList page={page} onOpen={setSpecId} />}
                </AsyncSection>
              </SectionCard>
            </TabsContent>
            <TabsContent value="bindings" className="mt-4">
              <SectionCard
                title={t('bindings.title')}
                description={t('bindings.description')}
                actions={
                  <div className="flex flex-wrap gap-2">
                    <ProtocolFilter value={protocol} onChange={setProtocol} />
                    <Select
                      value={verdict}
                      onValueChange={(value) =>
                        setVerdict(value as ProtocolObservationVerdict | 'all')
                      }
                    >
                      <SelectTrigger
                        className="w-48"
                        aria-label={t('filters.verdict')}
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="all">
                          {t('filters.allVerdicts')}
                        </SelectItem>
                        {VERDICTS.map((value) => (
                          <SelectItem key={value} value={value}>
                            {t(`verdict.${value}`)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                }
              >
                <AsyncSection query={bindings} skeletonHeight={180}>
                  {(page) => <BindingList page={page} onOpen={setBindingId} />}
                </AsyncSection>
              </SectionCard>
            </TabsContent>
          </Tabs>
        </>
      )}

      {composerOperation ? (
        <ProtocolComposerDialog
          open
          workspaceId={composerOperation.workspaceId}
          request={composerOperation.request}
          catalogScope={composerOperation.request.tenant ?? ''}
          catalogSpecs={composerCatalog.data?.items ?? []}
          catalogSpecsComplete={
            !!composerCatalog.data && !composerCatalog.data.has_more
          }
          permissions={composerPermissions}
          onOpenChange={(open) => {
            if (!open) setComposerOperation(null)
          }}
          onCreated={(result) => {
            const operationTenant = composerOperation.request.tenant
            setComposerOperation(null)
            invalidate(operationTenant)
            if (activeTenant === operationTenant) setSpecId(result.spec.id)
          }}
        />
      ) : null}
      <ProtocolSpecDetailSheet
        specId={specId}
        onOpenChange={(open) => {
          if (!open) setSpecId(null)
        }}
        onChanged={invalidate}
      />
      <ProtocolBindingDetailSheet
        bindingId={bindingId}
        onOpenChange={(open) => {
          if (!open) setBindingId(null)
        }}
        onChanged={invalidate}
      />
    </IntelPage>
  )
}

function ProtocolFilter({
  value,
  onChange,
}: {
  value: BindingProtocol | 'all'
  onChange: (value: BindingProtocol | 'all') => void
}) {
  const { t } = useTranslation('protocolBindings')
  return (
    <Select
      value={value}
      onValueChange={(next) => onChange(next as BindingProtocol | 'all')}
    >
      <SelectTrigger className="w-36" aria-label={t('filters.protocol')}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="all">{t('filters.allProtocols')}</SelectItem>
        <SelectItem value="a2a">A2A</SelectItem>
        <SelectItem value="mcp">MCP</SelectItem>
      </SelectContent>
    </Select>
  )
}

function SpecList({
  page,
  onOpen,
}: {
  page: ProtocolPage<ProtocolBindingSpec>
  onOpen: (id: string) => void
}) {
  const { t } = useTranslation('protocolBindings')
  if (page.items.length === 0)
    return (
      <EmptyState
        icon={<Inbox />}
        title={t('specs.emptyTitle')}
        description={t('specs.emptyBody')}
      />
    )
  return (
    <div>
      <ul className="divide-y divide-border">
        {page.items.map((spec) => (
          <li key={spec.id}>
            <button
              type="button"
              onClick={() => onOpen(spec.id)}
              className="flex w-full items-start justify-between gap-4 py-3 text-left hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <div className="min-w-0 space-y-1">
                <p className="truncate text-sm font-medium">
                  {spec.binding_key}
                </p>
                <p className="truncate font-mono text-xs text-muted-foreground">
                  {spec.protocol.toUpperCase()} {spec.protocol_version} ·{' '}
                  {t('fields.generation')} {spec.generation} ·{' '}
                  {spec.peer_authority}
                </p>
              </div>
              <div className="flex shrink-0 flex-wrap justify-end gap-2">
                <SpecStateBadge state={spec.state} />
                <ProtocolVerdictBadge verdict={spec.validation.verdict} />
              </div>
            </button>
          </li>
        ))}
      </ul>
      {page.has_more ? (
        <p className="border-t border-border pt-2 text-xs text-muted-foreground">
          {t('pagination.truncated')}
        </p>
      ) : null}
    </div>
  )
}

function BindingList({
  page,
  onOpen,
}: {
  page: ProtocolPage<ProtocolBinding>
  onOpen: (id: string) => void
}) {
  const { t } = useTranslation('protocolBindings')
  if (page.items.length === 0)
    return (
      <EmptyState
        icon={<Inbox />}
        title={t('bindings.emptyTitle')}
        description={t('bindings.emptyBody')}
      />
    )
  return (
    <div>
      <ul className="divide-y divide-border">
        {page.items.map((binding) => (
          <li key={binding.id}>
            <button
              type="button"
              onClick={() => onOpen(binding.id)}
              className="flex w-full items-start justify-between gap-4 py-3 text-left hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <div className="min-w-0 space-y-1">
                <p className="truncate font-mono text-sm font-medium">
                  {binding.external_kind}:
                  {binding.external_id || binding.synthetic_sid}
                </p>
                <p className="truncate font-mono text-xs text-muted-foreground">
                  {binding.protocol.toUpperCase()} {binding.protocol_version} ·{' '}
                  {binding.local_state || '—'} ↔ {binding.remote_state || '—'} ·{' '}
                  {binding.peer_authority}
                </p>
              </div>
              <div className="flex shrink-0 flex-wrap justify-end gap-2">
                <ProtocolVerdictBadge verdict={binding.observation_verdict} />
                <Badge variant={binding.terminal ? 'neutral' : 'info'}>
                  {binding.terminal
                    ? t('binding.terminal')
                    : t('binding.nonTerminal')}
                </Badge>
              </div>
            </button>
          </li>
        ))}
      </ul>
      {page.has_more ? (
        <p className="border-t border-border pt-2 text-xs text-muted-foreground">
          {t('pagination.truncated')}
        </p>
      ) : null}
    </div>
  )
}

export default ProtocolBindingsView
