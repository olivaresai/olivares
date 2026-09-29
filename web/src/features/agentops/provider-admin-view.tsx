// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ContactRound, IdCard, Link2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { PageHeader, WORK_CHROME_ROW } from '@/components/ui/page-header'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '@/lib/auth/context'
import { useAuthBoundary } from './auth-boundary'
import { BindingsTable } from './bindings-table'
import { ProfilesPanel } from './profiles-panel'
import { ProviderAccountsPanel } from './provider-accounts-panel'
import './i18n'

/** Which door opened this view: `/provider-profiles`, `/provider-bindings` or
 * `/provider-accounts`. All land on the same room; the entrance names the tab that
 * opens first, and the page title, so a bookmark to any reads as what it is. */
export type ProviderAdminEntrance = 'profiles' | 'bindings' | 'accounts'

/** The tabs in the order they are offered, and the fallback order when the entrance's
 * own tier is missing. */
const TAB_ORDER: readonly ProviderAdminEntrance[] = [
  'profiles',
  'bindings',
  'accounts',
]

/**
 * ProviderAdminView — the provider-profile plane's OWN room, with three doors.
 *
 * Before this, the plane lived only as a tab inside the sessions workspace, whose
 * two routes require `sessions:run:read` (`/agentops`) or `sessions:live:read`
 * (`/sessions`). A principal holding only `sessions:profile:read`, or only the
 * binding tiers, had no entrance at all — a permission the engine declares, that no
 * screen could be reached with. This view follows the two-doors-one-room
 * pattern: `/provider-profiles` is gated on `sessions:profile:read` and opens on the
 * profiles tab; `/provider-bindings` is gated on `sessions:profile-binding:read` and
 * opens on the tenant-wide bindings table; `/provider-accounts` is gated on
 * `sessions:account:read` and opens on the named accounts. Each tab is offered on ITS
 * read permission and every control inside checks its own write/admin tier where it
 * acts. The generic route guard is untouched: each entry declares exactly one
 * permission.
 *
 * The room is remounted on the authority boundary (principal, tenant, credential):
 * nothing read or half-done under one operator survives into the next.
 */
export function ProviderAdminView({
  entrance,
}: {
  entrance: ProviderAdminEntrance
}) {
  const boundary = useAuthBoundary()
  return <Inner key={boundary.key} entrance={entrance} />
}

function Inner({ entrance }: { entrance: ProviderAdminEntrance }) {
  const { t } = useTranslation('agentops')
  const { can } = useAuth()
  const allowed: Record<ProviderAdminEntrance, boolean> = {
    profiles: can('sessions:profile:read'),
    bindings: can('sessions:profile-binding:read'),
    accounts: can('sessions:account:read'),
  }
  // The given tab when this principal may read it; else the first one they may.
  const readable = (prefer: ProviderAdminEntrance): ProviderAdminEntrance =>
    allowed[prefer] ? prefer : (TAB_ORDER.find((k) => allowed[k]) ?? prefer)

  const [tab, setTab] = useState<ProviderAdminEntrance>(() =>
    readable(entrance),
  )
  // A tab whose permission is gone right now is neither shown nor left selected.
  const effective = readable(tab)

  const door = {
    profiles: {
      icon: IdCard,
      title: t('profiles.view.profilesTitle'),
      description: t('profiles.subtitle'),
    },
    bindings: {
      icon: Link2,
      title: t('profiles.view.bindingsTitle'),
      description: t('profiles.bindings.subtitle'),
    },
    accounts: {
      icon: ContactRound,
      title: t('profiles.view.accountsTitle'),
      description: t('accounts.subtitle'),
    },
  }[entrance]

  return (
    <Tabs
      value={effective}
      onValueChange={(v) => setTab(v as ProviderAdminEntrance)}
    >
      {/* Title and the one control line share a 36 px row so the first table
          row can sit at y ≤ 136 (header 48 + this row + thead). A stacked
          title, then tabs, then a subtitle/register band is what measured 293. */}
      <div data-slot="work-chrome" className={WORK_CHROME_ROW}>
        <PageHeader
          className="min-w-0"
          actionsPanelAnchor="row"
          icon={door.icon}
          title={door.title}
          description={door.description}
        />
        <TabsList className="min-w-0">
          {allowed.profiles && (
            <TabsTrigger value="profiles">
              {t('profiles.view.tabProfiles')}
            </TabsTrigger>
          )}
          {allowed.bindings && (
            <TabsTrigger value="bindings">
              {t('profiles.view.tabBindings')}
            </TabsTrigger>
          )}
          {allowed.accounts && (
            <TabsTrigger value="accounts">
              {t('profiles.view.tabAccounts')}
            </TabsTrigger>
          )}
        </TabsList>
      </div>
      {allowed.profiles && (
        <TabsContent value="profiles" className="pt-0">
          <ProfilesPanel describe={false} pageSurface />
        </TabsContent>
      )}
      {allowed.bindings && (
        <TabsContent value="bindings" className="pt-0">
          <BindingsTable describe={false} />
        </TabsContent>
      )}
      {allowed.accounts && (
        <TabsContent value="accounts" className="pt-0">
          <ProviderAccountsPanel />
        </TabsContent>
      )}
    </Tabs>
  )
}
