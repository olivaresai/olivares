// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useNavigate, useRouterState } from '@tanstack/react-router'
import { SlidersHorizontal } from 'lucide-react'
import { useCallback, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { PageHeader, WORK_CHROME_ROW } from '@/components/ui/page-header'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  useValidatedUrlState,
  type UrlState,
  type UrlStateDecoded,
} from '@/lib/hooks/use-url-state'
import './i18n'
import { AgentsTab } from './agents-tab'
import { BindingsTab } from './bindings-tab'
import { ConnectorsTab } from './connectors-tab'
import { RolesTab } from './roles-tab'
import { ScopesTab } from './scopes-tab'
import { SecretsTab } from './secrets-tab'
import { WorkspaceConnectorsTab } from './workspace-connectors-tab'
import {
  AGENT_CREATION,
  GROUP_CREATION,
  GRANT_CREATION,
  useConsoleOperation,
} from './console-authority'
import { WorkspaceSwitcher } from '@/components/layout/workspace-switcher'

/**
 * ConsoleView is the FASE X control console, Administration: the tenant's agents,
 * workspaces and agent-groups, source bindings, secrets and connectors. People, SSO, roles
 * and API keys are People & groups' (MOVED_TABS below). It composes one tab per surface;
 * each tab gates its privileged write actions behind RBAC + an AAL3 step-up (RequireAssurance) and
 * runs them through the standard privileged-mutation pattern. Hang their
 * own panels (sources, model governance) off this console.
 *
 * — `?tab=` DEEP LINK. This console OWNS the scoped grants and the agent↔identity
 * bindings, which is where the access map has to send an operator who has just seen a
 * least-privilege finding. Until now the tab was local state seeded at 'people', so a
 * link here landed on the wrong surface and the operator had to go hunting — the exact
 * "leads nowhere" the map suffered from. The seam mirrors model-ops-view.tsx:55-84: an
 * unknown or absent value falls back rather than showing an empty shell, and selecting a
 * tab REPLACES the history entry so Back leaves the view instead of walking the tabs.
 *
 * N2 (2026-09-07) — the URL DRIVES the mounted selection. `initialTab()` used to
 * read `window.location.search` once into `useState`; a later router navigation
 * (Back/Forward, a Link, a command) moved `?tab=` while this view stayed mounted
 * and kept the old panel. `useValidatedUrlState` is the existing contract for
 * that: owned key `tab`, known fallback, refused values cleared with replace
 * without touching unrelated search/hash, user clicks still replace.
 */
const TAB_VALUES = [
  'agents',
  'scopes',
  'roles',
  'bindings',
  'secrets',
  'connectors',
  'wsConnectors',
] as const

/**
 * 0.5 ONE NAVIGATION — the tabs a tenant administrator finds in another place now, and where
 * an old `?tab=` link lands: People & groups holds people, SSO, roles and API keys, License
 * the edition. Roles stays here for a workspace (delegated) administrator, who cannot open
 * People & groups' copy of it, so these wait for the authority and are not route
 * TAB_ALIASES (features/registry.tsx), which already land the old MCP gateway tab.
 */
const MOVED_TABS: Readonly<
  Record<string, { to: string; search?: Readonly<Record<string, string>> }>
> = {
  people: { to: '/identity', search: { tab: 'people' } },
  sso: { to: '/identity', search: { tab: 'sso' } },
  roles: { to: '/identity', search: { tab: 'roles' } },
  apiKeys: { to: '/identity', search: { tab: 'apiKeys' } },
  license: { to: '/settings', search: { section: 'edition' } },
}

const TAB_URL_KEYS = ['tab'] as const
const FALLBACK_TAB = 'agents'

function decodeConsoleTab(
  raw: UrlState,
  fallback = FALLBACK_TAB,
  tenantAdmin = false,
): UrlStateDecoded<string> {
  const want = raw.tab
  if (want === undefined) {
    return { value: fallback, issues: [] }
  }
  const moved = Object.hasOwn(MOVED_TABS, want)
  if (
    (TAB_VALUES as readonly string[]).includes(want) &&
    !(tenantAdmin && moved)
  ) {
    return { value: want, issues: [] }
  }
  // A moved tab is never refused: the view sends the whole address to its new place once
  // the authority says this is a tenant administrator, who may still be answering.
  if (moved) {
    return { value: fallback, issues: [] }
  }
  // Named so the hook can drop it from the address bar (a refused value must
  // not stay in a copied link) without rewriting any other key.
  return { value: fallback, issues: ['tab'] }
}

export default function ConsoleView() {
  const { t } = useTranslation(['console', 'common'])
  const authority = useConsoleOperation('', 'tenant:admin')
  const tenantAdmin = authority.current
  const agents = useConsoleOperation(AGENT_CREATION, 'agent:write')
  const groups = useConsoleOperation(GROUP_CREATION, 'agent:write')
  const grants = useConsoleOperation(GRANT_CREATION, 'governance:rbac:admin')
  const fallback = tenantAdmin
    ? FALLBACK_TAB
    : agents.current
      ? 'agents'
      : groups.current
        ? 'scopes'
        : grants.current
          ? 'roles'
          : 'agents'
  const decode = useCallback(
    (raw: UrlState) => decodeConsoleTab(raw, fallback, tenantAdmin),
    [fallback, tenantAdmin],
  )
  const [tab, patchTab] = useValidatedUrlState(
    TAB_URL_KEYS,
    decode,
    // NOT a page change, so the router takes no part in scroll restoration for it.
    // With `scrollRestoration: true` (app/router.tsx) every navigation otherwise
    // restores, after render, the cached offsets of every element that ever fired a
    // `scroll` event — the tab strip included, whose cached position predates the
    // reveal of the tab just selected (its own scroll event is still pending when the
    // router snapshots) — and scrolls the window to the top. Measured 2026-09-06
    // (console-tab-scroll-restoration): 3/11 selected tabs visible at 390 px because
    // the strip was written back to a stale scrollLeft. The strip also re-reveals
    // after a router restore (components/ui/tabs.tsx); this keeps the router out of
    // a tab switch altogether, so nothing is restored, reset or re-revealed.
    { resetScroll: false },
  )

  const setTab = useCallback(
    (value: string) => {
      patchTab({ tab: value })
    },
    [patchTab],
  )

  const navigate = useNavigate()
  const requested = new URLSearchParams(
    useRouterState({
      select: (s: { location: { searchStr: string } }) => s.location.searchStr,
    }),
  ).get('tab')
  const moved =
    tenantAdmin && requested !== null && Object.hasOwn(MOVED_TABS, requested)
      ? MOVED_TABS[requested]
      : undefined
  useEffect(() => {
    if (!moved) return
    // An alias's redirect: the rest of the query and the hash come along, and the old
    // address leaves history, so Back returns to where the link was followed from.
    void navigate({
      to: moved.to,
      search: ({ tab: _tab, ...rest }: Record<string, unknown>) => ({
        ...rest,
        ...moved.search,
      }),
      hash: true,
      replace: true,
    } as never)
  }, [moved, navigate])
  if (moved) return null

  if (!tenantAdmin && authority.access !== null && !authority.workspace) {
    return (
      <>
        <PageHeader title={t('console:title')} icon={SlidersHorizontal} />
        <WorkspaceSwitcher variant="card" />
      </>
    )
  }

  return (
    <Tabs value={tab} onValueChange={setTab}>
      {/* Title and the one control line share a 36 px row so the first table
          row can sit at y ≤ 136 (header 48 + this row + thead). A stacked
          title, then tabs, then a section heading is what measured 272. */}
      <div data-slot="work-chrome" className={WORK_CHROME_ROW}>
        <PageHeader
          className="min-w-0"
          actionsPanelAnchor="row"
          title={t('console:title')}
          description={t('console:subtitle')}
          icon={SlidersHorizontal}
        />
        <TabsList className="min-w-0">
          <TabsTrigger value="agents">{t('console:tabs.agents')}</TabsTrigger>
          <TabsTrigger value="scopes">{t('console:tabs.scopes')}</TabsTrigger>
          {!tenantAdmin && (
            <TabsTrigger value="roles">{t('console:tabs.roles')}</TabsTrigger>
          )}
          {tenantAdmin && (
            <>
              <TabsTrigger value="bindings">
                {t('console:tabs.bindings')}
              </TabsTrigger>
              <TabsTrigger value="secrets">
                {t('console:tabs.secrets')}
              </TabsTrigger>
              <TabsTrigger value="connectors">
                {t('console:tabs.connectors')}
              </TabsTrigger>
              <TabsTrigger value="wsConnectors">
                {t('console:tabs.wsConnectors', 'Workspace Connectors')}
              </TabsTrigger>
            </>
          )}
        </TabsList>
      </div>
      <TabsContent value="agents" className="pt-0">
        <AgentsTab />
      </TabsContent>
      <TabsContent value="scopes" className="pt-0">
        <ScopesTab />
      </TabsContent>
      <TabsContent value="roles" className="pt-0">
        <RolesTab />
      </TabsContent>
      <TabsContent value="bindings" className="pt-0">
        <BindingsTab />
      </TabsContent>
      <TabsContent value="secrets" className="pt-0">
        <SecretsTab />
      </TabsContent>
      <TabsContent value="connectors" className="pt-0">
        <ConnectorsTab />
      </TabsContent>
      <TabsContent value="wsConnectors" className="pt-0">
        <WorkspaceConnectorsTab />
      </TabsContent>
    </Tabs>
  )
}
