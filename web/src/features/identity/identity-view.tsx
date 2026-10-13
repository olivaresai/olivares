// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useNavigate } from '@tanstack/react-router'
import { Fingerprint } from 'lucide-react'
import { Fragment, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { PageHeader } from '@/components/ui/page-header'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '@/lib/auth/context'
import { PANEL_EXTENSIONS } from '@/features/extensions'
import { useOfferedPanels } from '@/features/panels'
import { useMinWidth } from '@/lib/hooks/use-min-width'
import { usePendingCommandAction } from '@/features/navigation/command-actions'
import { ApiKeysTab } from '@/features/console/api-keys-tab'
import { PeopleTab } from '@/features/console/people-tab'
import { RolesTab } from '@/features/console/roles-tab'
import { SSOTab } from '@/features/console/sso-tab'
import '@/features/console/i18n'
import { RecordingNotice } from '@/features/recordings/recording-notice'
import { FederationTab } from './federation'
import { McpAuthTab } from './mcp-auth'
import { NhiRosterTab } from './nhi-roster'
import { PrivilegedLoginTab } from './privileged-login'
import { TOTPTab } from './totp'
import './i18n'

type TabKey = string

/** The sections that come from Administration, and the permission that screen required. */
const ADMIN_TABS: ReadonlySet<TabKey> = new Set([
  'people',
  'roles',
  'sso',
  'apiKeys',
])
const ADMIN_PERMISSION = 'tenant:admin'

/** The groups, in order: people first, then how they sign in, then the machines, then posture. */
const GROUPS: readonly { id: string; tabs: readonly TabKey[] }[] = [
  { id: 'people', tabs: ['people', 'roles'] },
  { id: 'signIn', tabs: ['sso', 'federation', 'login', 'totp'] },
  { id: 'machines', tabs: ['inventory', 'mcp', 'apiKeys'] },
]

function initialTab(available: readonly TabKey[]): TabKey {
  const fallback: TabKey = available.includes('people')
    ? 'people'
    : 'federation'
  if (typeof window === 'undefined') return fallback
  const want = new URLSearchParams(window.location.search).get('tab')
  return want && (available as readonly string[]).includes(want)
    ? (want as TabKey)
    : fallback
}

export default function IdentityView() {
  const { t } = useTranslation(['identity', 'console'])
  const navigate = useNavigate()
  const { can } = useAuth()
  const admin = can(ADMIN_PERMISSION)
  const panels = useOfferedPanels(PANEL_EXTENSIONS.identityTabs)
  const groups = GROUPS.map((g) => ({
    id: g.id,
    tabs: [
      ...panels.filter((p) => p.group === g.id).map((p) => p.id),
      ...g.tabs,
    ].filter((k) => admin || !ADMIN_TABS.has(k)),
  })).filter((g) => g.tabs.length > 0)
  for (const panel of panels) {
    if (!groups.some((g) => g.id === panel.group)) {
      groups.push({
        id: panel.group,
        tabs: panels.filter((p) => p.group === panel.group).map((p) => p.id),
      })
    }
  }
  const available = groups.flatMap((g) => g.tabs)
  const [tab, setTabState] = useState<TabKey>(() => initialTab(available))
  const selectedTab = available.includes(tab) ? tab : initialTab(available)
  const pendingTab = useRef<TabKey | null>(null)
  useEffect(() => {
    pendingTab.current = null
  }, [selectedTab])
  // A vertical section list beside the work at `lg` (Up/Down move between sections), a
  // scrolling strip below it (Left/Right): the orientation decides the arrow keys.
  const wide = useMinWidth(1024)
  // ⌘K "Invite people" (registry `identity.invite`, membership:write): People, with its
  // onboarding dialog open in invite mode. People is an administration section, so the
  // verb lands only where that section is offered.
  const [inviteRequested, setInviteRequested] = useState(false)
  usePendingCommandAction('identity', 'invite', () => {
    if (!admin) return
    setTab('people')
    setInviteRequested(true)
  })

  function setTab(value: TabKey) {
    if (value === selectedTab || value === pendingTab.current) return
    // Radix focus and mouse-down can request the same tab before state commits.
    pendingTab.current = value
    setTabState(value)
    // `replace`, not push: the section is a facet of this screen, and preserving the
    // other params keeps a link from the access map intact.
    void navigate({
      search: (prev: Record<string, unknown>) => ({ ...prev, tab: value }),
      replace: true,
      resetScroll: false,
    } as never)
  }

  const label = (key: TabKey) =>
    panels.find((p) => p.id === key)?.label() ??
    (key === 'people'
      ? t('console:tabs.people')
      : key === 'roles'
        ? t('console:tabs.roles')
        : key === 'sso'
          ? t('console:tabs.sso')
          : key === 'apiKeys'
            ? t('console:tabs.apiKeys')
            : t(`identity:tabs.${key}`))

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title={t('identity:title')}
        description={t('identity:subtitle')}
        icon={Fingerprint}
      />
      <RecordingNotice namespace="identity" />
      <Tabs
        value={selectedTab}
        onValueChange={(v) => setTab(v as TabKey)}
        orientation={wide ? 'vertical' : 'horizontal'}
        className="flex flex-col gap-5 lg:flex-row lg:items-start lg:gap-8"
      >
        <TabsList
          aria-label={t('identity:sections')}
          className="min-w-0 lg:sticky lg:top-0 lg:w-56 lg:shrink-0"
        >
          {groups.map((group, i) => (
            <Fragment key={group.id}>
              <span
                role="presentation"
                className={
                  'hidden px-2 pb-1 text-overline font-medium tracking-[0.06em] text-text-3 uppercase lg:block' +
                  (i > 0 ? ' lg:mt-3' : '')
                }
              >
                {t(`identity:groups.${group.id}`)}
              </span>
              {group.tabs.map((key) => (
                <TabsTrigger key={key} value={key}>
                  {label(key)}
                </TabsTrigger>
              ))}
            </Fragment>
          ))}
        </TabsList>
        <div className="min-w-0 flex-1">
          {admin ? (
            <>
              <TabsContent value="people" className="pt-0">
                <PeopleTab
                  inviteRequested={inviteRequested}
                  onInviteHandled={() => setInviteRequested(false)}
                />
              </TabsContent>
              <TabsContent value="roles" className="pt-0">
                <RolesTab />
              </TabsContent>
              <TabsContent value="sso" className="pt-0">
                <SSOTab />
              </TabsContent>
              <TabsContent value="apiKeys" className="pt-0">
                <ApiKeysTab />
              </TabsContent>
            </>
          ) : null}
          <TabsContent value="federation" className="pt-0">
            <FederationTab />
          </TabsContent>

          <TabsContent value="inventory" className="pt-0">
            <NhiRosterTab />
          </TabsContent>
          <TabsContent value="mcp" className="pt-0">
            <McpAuthTab />
          </TabsContent>

          {panels.map(({ id, Component }) => (
            <TabsContent key={id} value={id} className="pt-0">
              <Component />
            </TabsContent>
          ))}
          <TabsContent value="login" className="pt-0">
            <PrivilegedLoginTab />
          </TabsContent>
          <TabsContent value="totp" className="pt-0">
            <TOTPTab />
          </TabsContent>
        </div>
      </Tabs>
    </div>
  )
}
