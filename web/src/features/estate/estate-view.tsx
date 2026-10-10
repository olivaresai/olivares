// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useQueries } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { PageHeader } from '@/components/ui/page-header'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/field'
import { useAuth } from '@/lib/auth/context'
import { sameContext, useCapabilityPreflight } from '@/lib/auth/capabilities'
import { moduleEnabled, moduleOn, useModulesStore } from '@/stores/modules'
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
        auth.can('orchestration:schedule:read') && moduleOn('orchestration')
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
            {canOpenWorkflow && (
              <Button asChild>
                <a href="/automations">{t('workflows')}</a>
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
              variant="primary"
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
          <select
            id="estate-workspace"
            value={workspace}
            onChange={(event) => {
              setWorkspace(event.target.value)
              setSelected(null)
            }}
            className="min-h-11 w-full rounded-ctl border border-ctl-border bg-surface px-2 text-body"
          >
            <option value="">{t('allWorkspaces')}</option>
            {nodes
              .filter((node) => node.kind === 'workspace')
              .map((node) => (
                <option key={node.ref} value={node.ref}>
                  {node.label}
                </option>
              ))}
          </select>
        </Field>
        <Field label={t('kind')} htmlFor="estate-kind">
          <select
            id="estate-kind"
            value={kind}
            onChange={(event) => setKind(event.target.value)}
            className="min-h-11 w-full rounded-ctl border border-ctl-border bg-surface px-2 text-body"
          >
            <option value="">{t('allKinds')}</option>
            {families.map((family) => (
              <option key={family} value={family}>
                {t('kinds.' + family)}
              </option>
            ))}
          </select>
        </Field>
      </div>
      <p className="text-caption text-text-2">{t('sharedScope')}</p>
      {workspace && canReadContents && (
        <WorkspaceContents tenant={tenant} workspaceId={workspace} />
      )}
      {busy && <p role="status">{t('loading')}</p>}
      {queries.map((query, index) =>
        query.error || query.data?.hasMore || cursors[families[index]!] ? (
          <div
            key={families[index]}
            role={query.error ? 'alert' : 'status'}
            className="flex flex-wrap items-center gap-2 text-caption text-text-2"
          >
            <span>
              {t('kinds.' + families[index])}:{' '}
              {query.error ? t('unavailable') : t('more')}
            </span>
            {!query.error &&
              query.data?.hasMore &&
              query.data.cursor &&
              query.data.cursor !== cursors[families[index]!] && (
                <Button
                  size="sm"
                  disabled={query.isFetching}
                  onClick={() => {
                    setSelected(null)
                    setCursors((before) => ({
                      ...before,
                      [families[index]!]: query.data!.cursor,
                    }))
                  }}
                >
                  {t('nextPage')}
                </Button>
              )}
            {cursors[families[index]!] && (
              <Button
                size="sm"
                onClick={() => {
                  setSelected(null)
                  setCursors((before) => ({
                    ...before,
                    [families[index]!]: undefined,
                  }))
                }}
              >
                {t('firstPage')}
              </Button>
            )}
            {ownerFamilies.includes(families[index]!) && (
              <Button asChild size="sm">
                <a href={estateOwnerPaths[families[index]!]}>
                  {t('openOwner')}
                </a>
              </Button>
            )}
          </div>
        ) : null,
      )}
      <ul
        className="divide-y divide-line rounded-panel border border-line"
        aria-busy={busy}
      >
        {visible.map((node) => (
          <li key={estateNodeKey(tenant, node)}>
            <button
              onClick={(event) => {
                opener.current = event.currentTarget
                setSelected(node)
              }}
              className="flex min-h-14 w-full items-center gap-4 px-3 py-3 text-left outline-none hover:bg-hover focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
            >
              <span className="min-w-0 flex-1">
                <span className="block text-body font-medium break-words">
                  {node.label}
                </span>
                <span className="block text-caption text-text-2">
                  {t('kinds.' + node.kind)}
                </span>
              </span>
              <span className="shrink-0 text-caption text-text-2">
                <EstateStatus node={node} />
              </span>
            </button>
          </li>
        ))}
      </ul>
      {!busy &&
        queries.some((query) => query.isSuccess) &&
        visible.length === 0 && (
          <p role="status" className="text-body text-text-2">
            {t('empty')}
          </p>
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
