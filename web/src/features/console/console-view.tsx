// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useNavigate } from '@tanstack/react-router'
import { SlidersHorizontal } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { PageHeader } from '@/components/ui/page-header'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
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

function initialTab(): string {
  const fallback = 'people'
  if (typeof window === 'undefined') return fallback
  const want = new URLSearchParams(window.location.search).get('tab')
  return want && (TAB_VALUES as readonly string[]).includes(want)
    ? want
    : fallback
}

export default function ConsoleView() {
  const { t } = useTranslation(['console', 'common'])
  const navigate = useNavigate()
  const [tab, setTabState] = useState(initialTab)

  function setTab(value: string) {
    setTabState(value)
    void navigate({
      search: (prev: Record<string, unknown>) => ({ ...prev, tab: value }),
      replace: true,
    } as never)
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={t('console:title')}
        description={t('console:subtitle')}
        icon={SlidersHorizontal}
      />
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
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
        <TabsContent value="people">
          <PeopleTab />
        </TabsContent>
        <TabsContent value="agents">
          <AgentsTab />
        </TabsContent>
        <TabsContent value="sso">
          <SSOTab />
        </TabsContent>
        <TabsContent value="scopes">
          <ScopesTab />
        </TabsContent>
        <TabsContent value="roles">
          <RolesTab />
        </TabsContent>
        <TabsContent value="bindings">
          <BindingsTab />
        </TabsContent>
        <TabsContent value="secrets">
          <SecretsTab />
        </TabsContent>
        <TabsContent value="connectors">
          <ConnectorsTab />
        </TabsContent>
        <TabsContent value="wsConnectors">
          <WorkspaceConnectorsTab />
        </TabsContent>
        <TabsContent value="apiKeys">
          <ApiKeysTab />
        </TabsContent>
        <TabsContent value="license">
          <LicenseTab />
        </TabsContent>
      </Tabs>
    </div>
  )
}
