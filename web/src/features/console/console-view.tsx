// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { SlidersHorizontal } from 'lucide-react'
import { useCallback } from 'react'
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
import { ApiKeysTab } from './api-keys-tab'
import { BindingsTab } from './bindings-tab'
import { ConnectorsTab } from './connectors-tab'
import { LicenseTab } from './license-tab'
import { PeopleTab } from './people-tab'
import { RolesTab } from './roles-tab'
import { SSOTab } from './sso-tab'
import { ScopesTab } from './scopes-tab'
import { SecretsTab } from './secrets-tab'
import { WorkspaceConnectorsTab } from './workspace-connectors-tab'

/**
 * ConsoleView is the FASE X control console: the single hub an enterprise
 * uses to CONFIGURE its tenant — onboard users, connect SSO/IdP, and shape
 * workspaces and agent-groups. It composes one tab per surface; each tab gates its
 * privileged write actions behind RBAC + an AAL3 step-up (RequireAssurance) and
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
  'people',
  'agents',
  'sso',
  'scopes',
  'roles',
  'bindings',
  'secrets',
  'connectors',
  'wsConnectors',
  'apiKeys',
  'license',
] as const

const TAB_URL_KEYS = ['tab'] as const
const FALLBACK_TAB = 'people'

function decodeConsoleTab(raw: UrlState): UrlStateDecoded<string> {
  const want = raw.tab
  if (want === undefined) {
    return { value: FALLBACK_TAB, issues: [] }
  }
  if ((TAB_VALUES as readonly string[]).includes(want)) {
    return { value: want, issues: [] }
  }
  // Named so the hook can drop it from the address bar (a refused value must
  // not stay in a copied link) without rewriting any other key.
  return { value: FALLBACK_TAB, issues: ['tab'] }
}

export default function ConsoleView() {
  const { t } = useTranslation(['console', 'common'])
  const [tab, patchTab] = useValidatedUrlState(
    TAB_URL_KEYS,
    decodeConsoleTab,
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
          <TabsTrigger value="people">{t('console:tabs.people')}</TabsTrigger>
          <TabsTrigger value="agents">{t('console:tabs.agents')}</TabsTrigger>
          <TabsTrigger value="sso">{t('console:tabs.sso')}</TabsTrigger>
          <TabsTrigger value="scopes">{t('console:tabs.scopes')}</TabsTrigger>
          <TabsTrigger value="roles">{t('console:tabs.roles')}</TabsTrigger>
          <TabsTrigger value="bindings">
            {t('console:tabs.bindings')}
          </TabsTrigger>
          <TabsTrigger value="secrets">{t('console:tabs.secrets')}</TabsTrigger>
          <TabsTrigger value="connectors">
            {t('console:tabs.connectors')}
          </TabsTrigger>
          <TabsTrigger value="wsConnectors">
            {t('console:tabs.wsConnectors', 'Workspace Connectors')}
          </TabsTrigger>
          <TabsTrigger value="apiKeys">{t('console:tabs.apiKeys')}</TabsTrigger>
          <TabsTrigger value="license">{t('console:tabs.license')}</TabsTrigger>
        </TabsList>
      </div>
      <TabsContent value="people" className="pt-0">
        <PeopleTab />
      </TabsContent>
      <TabsContent value="agents" className="pt-0">
        <AgentsTab />
      </TabsContent>
      <TabsContent value="sso" className="pt-0">
        <SSOTab />
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
      <TabsContent value="apiKeys" className="pt-0">
        <ApiKeysTab />
      </TabsContent>
      <TabsContent value="license" className="pt-0">
        <LicenseTab />
      </TabsContent>
    </Tabs>
  )
}
