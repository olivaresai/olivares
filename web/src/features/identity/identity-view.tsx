// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Identity & NHI admin console — the buyer's first look at the identity story
// (C). Presentation over the SAME API (ARCHITECTURE.md): it RENDERS, AUDITS and
// LINTS what expose; it never federates, mints credentials, or creates
// WIF/service-account objects (writes are Console-only lists + reconciles them).
// Read-first, RBAC-reflected, self-audited.
//
// Tabs:
//   federation — SSO/SAML/OIDC config + SCIM 2.0 status + claude-console blind-spot (ADM-IDN-01)
//   inventory  — reconciled identity roster, external_id convergence → access map
//   lifecycle  — NHI staleness, enforcement, ownership, rotation and offboarding
//   mcp        — auth-MCP console, PRM RFC 9728 audience-bound model (ADM-IDN-02)
//   wif        — WIF graph + linter (AAL3-gated) (ANT2-08/07)
//   posture    — External Keys/CMEK + residency + cert-manager TLS + crypto/PQC (ANT2-04/06)
//   login      — privileged WebAuthn/PIV login + AAL gating
import { useNavigate } from '@tanstack/react-router'
import { Fingerprint } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { PageHeader } from '@/components/ui/page-header'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { RecordingNotice } from '@/features/recordings/recording-notice'
import { FederationTab } from './federation'
import { McpAuthTab } from './mcp-auth'
import { NhiLifecycleTab } from './nhi-lifecycle'
import { NhiRosterTab } from './nhi-roster'
import { PostureTab } from './posture'
import { PrivilegedLoginTab } from './privileged-login'
import { WifGraphTab } from './wif/wif-graph'
import './i18n'

type TabKey =
  | 'federation'
  | 'inventory'
  | 'lifecycle'
  | 'mcp'
  | 'wif'
  | 'posture'
  | 'login'

const TABS: TabKey[] = [
  'federation',
  'inventory',
  'lifecycle',
  'mcp',
  'wif',
  'posture',
  'login',
]

/**
 * — `?tab=` deep link. The access map sends an operator here to review the origin
 * identity of an observed access, and the roster lives on `inventory`; with the tab as plain
 * local state seeded at `federation` the link landed on a different surface, which is the
 * "leads nowhere" this work removes. Unknown/absent falls back rather than rendering an empty
 * shell (same contract as model-ops-view.tsx:55-60).
 *
 * NOTE it accepts only `tab`. An earlier version of the map's link also carried
 * `focus=<ref>`, which nothing here reads — a parameter the destination ignores is a link
 * that pretends to lead somewhere. Pre-selecting the identity needs the roster's DataTable
 * search to take an initial value; that is declared, not built, and the link's label
 * promises the roster and nothing more.
 *
 * ⚠ — THE SEAM WAS HALF BUILT, and its own write-up said otherwise. Recorded that
 * all three `?tab=` seams follow model-ops-view.tsx "y `replace: true` para que Atrás salga
 * de la vista en vez de recorrer pestañas" (the session record). Console and Claude
 * policy do; this one only READ the parameter and never wrote it back, so a deep link here
 * went stale the moment the operator touched a tab — copy‑and‑share reopened the roster no
 * matter which tab was on screen. The claim is now true rather than corrected away.
 */
function initialTab(): TabKey {
  const fallback: TabKey = 'federation'
  if (typeof window === 'undefined') return fallback
  const want = new URLSearchParams(window.location.search).get('tab')
  return want && (TABS as readonly string[]).includes(want)
    ? (want as TabKey)
    : fallback
}

export default function IdentityView() {
  const { t } = useTranslation('identity')
  const navigate = useNavigate()
  const [tab, setTabState] = useState<TabKey>(initialTab)

  function setTab(value: TabKey) {
    setTabState(value)
    // REPLACE, so Back leaves the view instead of undoing a tab click.
    void navigate({
      search: (prev: Record<string, unknown>) => ({ ...prev, tab: value }),
      replace: true,
    } as never)
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={t('title')}
        description={t('subtitle')}
        icon={Fingerprint}
      />
      <RecordingNotice namespace="identity" />
      <Tabs value={tab} onValueChange={(v) => setTab(v as TabKey)}>
        <TabsList>
          {TABS.map((key) => (
            <TabsTrigger key={key} value={key}>
              {t(`tabs.${key}`)}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="federation">
          <FederationTab />
        </TabsContent>
        <TabsContent value="inventory">
          <NhiRosterTab />
        </TabsContent>
        <TabsContent value="lifecycle">
          <NhiLifecycleTab />
        </TabsContent>
        <TabsContent value="mcp">
          <McpAuthTab />
        </TabsContent>
        <TabsContent value="wif">
          <WifGraphTab />
        </TabsContent>
        <TabsContent value="posture">
          <PostureTab />
        </TabsContent>
        <TabsContent value="login">
          <PrivilegedLoginTab />
        </TabsContent>
      </Tabs>
    </div>
  )
}
