// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  Folder,
  Layers,
  ListChecks,
  type LucideIcon,
  Plug,
  Server,
  Shapes,
  Shield,
  SquareTerminal,
} from 'lucide-react'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { AppLink } from '@/components/ui/app-link'
import { useQueries } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { BoxedList, BoxedListRow } from '@/components/ui/boxed-list'
import { EmptyState } from '@/components/ui/empty-state'
import { PageHeader } from '@/components/ui/page-header'
import { StateBlock } from '@/components/ui/state-block'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { sameContext, useCapabilityPreflight } from '@/lib/auth/capabilities'
import { moduleEnabled, moduleOn, useModulesStore } from '@/stores/modules'
import { FEATURE_VIEWS, type FeatureView } from '@/features/registry'
import { viewLabel } from '@/features/navigation/model'
import { isBusinessOnlyPath } from '@/features/business-build'
import { useNewSessionDialog } from '@/features/first-hour/new-session-store'
import { WorkspaceContents } from '@/features/workspace-dashboard/workspace-contents'
import { readEstateFamily } from './api'
import { EstateInspector } from './estate-inspector'
import { EstateStatus } from './estate-status'
import { estateOwnerPaths } from './owner-links'
import {
  ESTATE_FAMILIES,
  estateNodeKey,
  type EstateFamily,
  type EstateNode,
} from './types'
import './i18n'

const permissions: Record<EstateFamily, string> = {
  workspace: 'tenant:read',
  folder: 'sessions:workspace:read',
  session: 'sessions:run:read',
  work: 'sessions:work:read',
  connector: 'system:admin',
  mcp: 'tenant:admin',
  policy: 'governance:policy:read',
}

// Radix gives no item an empty value, so "all" is this stand-in; the filter state stays ''.
const ALL = '__all__'

const FAMILY_ICON: Record<EstateFamily, LucideIcon> = {
  workspace: Layers,
  folder: Folder,
  session: SquareTerminal,
  work: ListChecks,
  connector: Plug,
  mcp: Server,
  policy: Shield,
}

/** Where a resource sits, by the names already read; never a raw reference. */
function placeOf(node: EstateNode, nodes: EstateNode[]): string {
  const named = (kind: EstateFamily, ref?: string) =>
    ref ? nodes.find((n) => n.kind === kind && n.ref === ref)?.label : undefined
  return [named('folder', node.folderRef), named('workspace', node.workspaceId)]
    .filter(Boolean)
    .join(' · ')
}

export default function EstateView() {
  const auth = useAuth()
  const { t } = useTranslation('estate')
  const preflight = useCapabilityPreflight()
  const off = useModulesStore((state) => state.off)
  const liveAuth = useRef(auth)
  useLayoutEffect(() => {
    liveAuth.current = auth
  }, [auth])
  const context = preflight.context
  const families = ESTATE_FAMILIES.filter(
    (kind) =>
      (kind !== 'connector' || auth.isSuperadmin) &&
      auth.can(permissions[kind]) &&
      moduleEnabled(off, permissions[kind]),
  )
  const organization = FEATURE_VIEWS.find((view) => view.id === 'organization')
  const canWriteWork = auth.can('sessions:work:write') && moduleOn('sessions')
  const canStartSession = auth.can('sessions:run:write') && moduleOn('sessions')
  const ownerFamilies = families.filter((family) =>
    auth.can(
      family === 'policy'
        ? 'governance:identity:read'
        : family === 'workspace' || family === 'connector'
          ? 'tenant:admin'
          : permissions[family],
    ),
  )
  if (!moduleEnabled(off, 'sessions:run:read'))
    return <p role="status">{t('off')}</p>
  if (!context || !auth.activeTenant) return <p role="status">{t('loading')}</p>
  return (
    <EstateContent
      key={`${context.lifetime}:${families.join(',')}:${canWriteWork}:${canStartSession}`}
      tenant={auth.activeTenant}
      lifetime={context.lifetime}
      families={families}
      ownerFamilies={ownerFamilies}
      canWriteWork={canWriteWork}
      canStartSession={canStartSession}
      canOpenWorkflow={
        auth.can('orchestration:schedule:read') &&
        moduleOn('orchestration') &&
        !isBusinessOnlyPath('/automations')
      }
      organization={
        organization &&
        (!organization.permission || auth.can(organization.permission)) &&
        moduleEnabled(off, organization.permission)
          ? organization
          : undefined
      }
      canReadContents={auth.can('tenant:admin')}
      isCurrent={() =>
        sameContext(context, preflight.live()) &&
        liveAuth.current.can('sessions:work:write') &&
        moduleOn('sessions')
      }
    />
  )
}

function EstateContent({
  tenant,
  lifetime,
  families,
  ownerFamilies,
  canWriteWork,
  canStartSession,
  canOpenWorkflow,
  organization,
  canReadContents,
  isCurrent,
}: {
  tenant: string
  lifetime: number
  families: EstateFamily[]
  ownerFamilies: EstateFamily[]
  canWriteWork: boolean
  canStartSession: boolean
  canOpenWorkflow: boolean
  organization?: FeatureView
  canReadContents: boolean
  isCurrent: () => boolean
}) {
  const { t } = useTranslation('estate')
  const [search, setSearch] = useState('')
  const [workspace, setWorkspace] = useState('')
  const [kind, setKind] = useState('')
  const [selected, setSelected] = useState<EstateNode | null>(null)
  const opener = useRef<HTMLButtonElement | null>(null)
  const [cursors, setCursors] = useState<Partial<Record<EstateFamily, string>>>(
    {},
  )
  const queries = useQueries({
    queries: families.map((family) => ({
      queryKey: ['estate', tenant, lifetime, family, cursors[family]],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        readEstateFamily(family, { tenant, signal }, cursors[family]),
      retry: false,
      gcTime: 0,
      refetchOnWindowFocus: false,
    })),
  })
  const nodes = queries.flatMap((query) =>
    query.error ? [] : (query.data?.nodes ?? []),
  )
  const visible = nodes.filter(
    (node) =>
      (!kind || node.kind === kind) &&
      (!workspace ||
        (node.kind === 'workspace'
          ? node.ref === workspace
          : !node.workspaceId || node.workspaceId === workspace)) &&
      `${node.label} ${node.ref}`
        .toLocaleLowerCase()
        .includes(search.toLocaleLowerCase()),
  )
  const busy = queries.some((query) => query.isFetching)
  const allFailed = queries.length > 0 && queries.every((query) => query.error)
  // The owner pages help only a read that failed, not a refused one.
  const refused = queries.some(
    ({ error }) => error instanceof ApiError && error.isForbidden,
  )
  const refresh = () => {
    for (const query of queries) void query.refetch()
  }
  // The header opens the shell's New session form. The list is read again when it and
  // the launch it may hand over to are closed. This content is keyed on the launch
  // permission, the families and the lifetime: when one changes, a launch it opened
  // closes with it.
  const latestRefresh = useRef(refresh)
  useLayoutEffect(() => {
    latestRefresh.current = refresh
  })
  const offered = useRef(false)
  useEffect(() => {
    const shown = (s: ReturnType<typeof useNewSessionDialog.getState>) =>
      s.open || !!s.advanced?.open
    const stop = useNewSessionDialog.subscribe((now, before) => {
      if (!shown(before) || shown(now)) return
      offered.current = false
      latestRefresh.current()
    })
    return () => {
      stop()
      if (!offered.current) return
      const dialogs = useNewSessionDialog.getState()
      dialogs.setOpen(false)
      dialogs.closeAdvanced()
    }
  }, [])
  // A revoked/failed owner read cannot leave an inspector showing its former data.
  const inspected =
    selected &&
    nodes.find(
      (node) => node.kind === selected.kind && node.ref === selected.ref,
    )

  return (
    <div className="space-y-5">
      <PageHeader
        title={t('title')}
        description={t('subtitle')}
        actions={
          <>
            {organization && (
              <Button asChild>
                <AppLink href={organization.path}>
                  {viewLabel(t, organization.id)}
                </AppLink>
              </Button>
            )}
            {canOpenWorkflow && (
              <Button asChild>
                <AppLink href="/automations">{t('workflows')}</AppLink>
              </Button>
            )}
            <Button onClick={refresh} disabled={busy}>
              {t('refresh')}
            </Button>
          </>
        }
        primaryAction={
          canStartSession && (
            <Button
              onClick={(event) => {
                offered.current = true
                useNewSessionDialog
                  .getState()
                  .setOpen(true, event.currentTarget)
              }}
            >
              {t('startSession')}
            </Button>
          )
        }
      />
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label={t('search')} htmlFor="estate-search">
          <Input
            id="estate-search"
            type="search"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
        </Field>
        <Field label={t('workspace')} htmlFor="estate-workspace">
          <Select
            value={workspace || ALL}
            onValueChange={(value) => {
              setWorkspace(value === ALL ? '' : value)
              setSelected(null)
            }}
          >
            <SelectTrigger id="estate-workspace">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('allWorkspaces')}</SelectItem>
              {nodes
                .filter((node) => node.kind === 'workspace')
                .map((node) => (
                  <SelectItem key={node.ref} value={node.ref}>
                    {node.label}
                  </SelectItem>
                ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label={t('kind')} htmlFor="estate-kind">
          <Select
            value={kind || ALL}
            onValueChange={(value) => setKind(value === ALL ? '' : value)}
          >
            <SelectTrigger id="estate-kind">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('allKinds')}</SelectItem>
              {families.map((family) => (
                <SelectItem key={family} value={family}>
                  {t('kinds.' + family)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      </div>
      <p className="text-caption text-text-2">{t('sharedScope')}</p>
      {workspace && canReadContents && (
        <WorkspaceContents tenant={tenant} workspaceId={workspace} />
      )}
      {busy && nodes.length > 0 && (
        <p role="status" className="sr-only">
          {t('loading')}
        </p>
      )}
      {allFailed ? (
        <>
          <QueryErrorState error={queries[0]?.error} retry={refresh} />
          {!refused && (
            <div className="flex flex-wrap gap-2">
              {ownerFamilies.map((family) => (
                <Button key={family} asChild size="sm">
                  <AppLink href={estateOwnerPaths[family]}>
                    {t('openOwner', { resource: t('kinds.' + family) })}
                  </AppLink>
                </Button>
              ))}
            </div>
          )}
        </>
      ) : busy && nodes.length === 0 ? (
        <StateBlock state="loading" label={t('loading')} />
      ) : nodes.length === 0 && queries.every((query) => query.isSuccess) ? (
        <EmptyState
          icon={<Shapes />}
          title={t('noResources.title')}
          description={t('noResources.description')}
        />
      ) : nodes.length > 0 && visible.length === 0 ? (
        <StateBlock
          state="filtered"
          title={t('empty')}
          filters={[
            search,
            nodes.find((n) => n.kind === 'workspace' && n.ref === workspace)
              ?.label,
            kind && t('kinds.' + kind),
          ]
            .filter(Boolean)
            .join(' · ')}
          hiddenCount={nodes.length}
          onClear={() => {
            setSearch('')
            setWorkspace('')
            setKind('')
          }}
        />
      ) : (
        <div
          className="grid items-start gap-x-4 gap-y-6 lg:grid-cols-2"
          aria-busy={busy}
        >
          {families.map((family, index) => {
            if (kind && kind !== family) return null
            const query = queries[index]!
            const rows = visible.filter((node) => node.kind === family)
            const note = query.error || query.data?.hasMore || cursors[family]
            if (rows.length === 0 && !note) return null
            const Icon = FAMILY_ICON[family]
            const heading = `estate-group-${family}`
            return (
              <section
                key={family}
                aria-labelledby={heading}
                className="min-w-0 space-y-2"
              >
                <div className="flex items-baseline justify-between gap-3">
                  <h2
                    id={heading}
                    className="text-body font-semibold text-text"
                  >
                    {t('groups.' + family)}
                  </h2>
                  {!query.error && (
                    <span className="text-caption text-text-2 tabular-nums">
                      {rows.length}
                    </span>
                  )}
                </div>
                {query.error && (
                  <QueryErrorState
                    error={query.error}
                    description={t('unavailable')}
                    retry={() => void query.refetch()}
                  />
                )}
                {note ? (
                  <div
                    role="status"
                    className="flex flex-wrap items-center gap-2 text-caption text-text-2"
                  >
                    <span>
                      {!query.error && query.data?.hasMore
                        ? t('more', { count: query.data.nodes.length })
                        : ''}
                    </span>
                    {!query.error &&
                      query.data?.hasMore &&
                      query.data.cursor &&
                      query.data.cursor !== cursors[family] && (
                        <Button
                          size="sm"
                          disabled={query.isFetching}
                          onClick={() => {
                            setSelected(null)
                            setCursors((before) => ({
                              ...before,
                              [family]: query.data!.cursor,
                            }))
                          }}
                        >
                          {t('nextPage')}
                        </Button>
                      )}
                    {cursors[family] && (
                      <Button
                        size="sm"
                        onClick={() => {
                          setSelected(null)
                          setCursors((before) => ({
                            ...before,
                            [family]: undefined,
                          }))
                        }}
                      >
                        {t('firstPage')}
                      </Button>
                    )}
                    {ownerFamilies.includes(family) && (
                      <Button asChild size="sm">
                        <AppLink href={estateOwnerPaths[family]}>
                          {t('openOwner', {
                            resource: t('kinds.' + family),
                          })}
                        </AppLink>
                      </Button>
                    )}
                  </div>
                ) : null}
                {rows.length > 0 && (
                  <BoxedList role="list">
                    {rows.map((node) => (
                      <BoxedListRow
                        key={estateNodeKey(tenant, node)}
                        role="listitem"
                      >
                        <span
                          aria-hidden="true"
                          className="flex size-8 shrink-0 items-center justify-center rounded-ctl bg-hover text-text-2 [&_svg]:size-4"
                        >
                          <Icon />
                        </span>
                        <span className="min-w-0 flex-1">
                          <button
                            type="button"
                            onClick={(event) => {
                              opener.current = event.currentTarget
                              setSelected(node)
                            }}
                            className="flex w-full items-center rounded-sm text-start text-body font-medium break-words text-text hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus max-md:min-h-11"
                          >
                            {node.label}
                          </button>
                          <span className="block truncate text-caption text-text-2">
                            {placeOf(node, nodes) || t('kinds.' + node.kind)}
                          </span>
                        </span>
                        <span className="shrink-0">
                          <EstateStatus node={node} />
                        </span>
                      </BoxedListRow>
                    ))}
                  </BoxedList>
                )}
              </section>
            )
          })}
        </div>
      )}
      {inspected && (
        <EstateInspector
          key={estateNodeKey(tenant, inspected)}
          tenant={tenant}
          lifetime={lifetime}
          node={inspected}
          nodes={nodes}
          canWriteWork={canWriteWork}
          sourceReadAt={queries[families.indexOf(inspected.kind)]?.data?.readAt}
          canOpenOwner={ownerFamilies.includes(inspected.kind)}
          isCurrent={isCurrent}
          onClose={() => setSelected(null)}
          onRefresh={refresh}
          onRestoreFocus={() => {
            if (opener.current?.isConnected) opener.current.focus()
            else document.getElementById('estate-search')?.focus()
          }}
        />
      )}
    </div>
  )
}
