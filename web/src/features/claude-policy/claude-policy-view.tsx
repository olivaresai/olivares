// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ClaudePolicyView (registry id 'claude-policy') — the Claude Code governance
// console: author managed-settings / hooks / managed-mcp / sandbox policy and
// Cedar/OPA policy-as-code, and read the PERMITTED-vs-OBSERVED drift the engine
// emits. Presentation + audited action over the SAME API (ARCHITECTURE.md): authoring is
// wired to the DECLARED contract with an honest pending seam; drift is real.
import { useNavigate } from '@tanstack/react-router'
import { ScrollText } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ForbiddenState } from '@/components/ui/error-state'
import { PageHeader } from '@/components/ui/page-header'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { RecordingNotice } from '@/features/recordings/recording-notice'
import { useAuth } from '@/lib/auth/context'
import { CedarOpaView } from './cedar-opa-view'
import { DriftView } from './drift'
import { ManagedAgentsHitl } from './managed-agents-hitl'
import { PolicyAuthoringPanel } from './policy-authoring-panel'
import {
  HooksReference,
  ManagedMcpReference,
  ManagedSettingsReference,
  SandboxReference,
} from './references'
import { MANAGED_ONLY_KEYS, SANDBOX_KEYS } from './schema'
import './i18n'

const TAB_VALUES = [
  'drift',
  'managed-settings',
  'hooks',
  'managed-mcp',
  'sandbox',
  'policy-as-code',
  'hitl',
] as const

type TabKey = (typeof TAB_VALUES)[number]

/**
 * — `?tab=` DEEP LINK. The policy-as-code tab is where a policy-derived access edge
 * is actually changed, so the access map links straight to it; with the tab as plain
 * local state seeded at 'drift', that link landed on a different surface and the operator
 * had to go looking. Unknown/absent values fall back to 'drift' rather than rendering an
 * empty shell (same contract as model-ops-view.tsx:55-60).
 */
function initialTab(): TabKey {
  const fallback: TabKey = 'drift'
  if (typeof window === 'undefined') return fallback
  const want = new URLSearchParams(window.location.search).get('tab')
  return want && (TAB_VALUES as readonly string[]).includes(want)
    ? (want as TabKey)
    : fallback
}

const MANAGED_SETTINGS_DEFAULT = `{
  "permissions": {
    "defaultMode": "default"
  }
}`
const HOOKS_DEFAULT = `{
  "PreToolUse": []
}`
const MANAGED_MCP_DEFAULT = `{
  "mcpServers": {}
}`
const SANDBOX_DEFAULT = `{
  "failIfUnavailable": false
}`

/** Top-level managed-only keys for the guided form (nested sandbox.* keys live in
 *  the Sandbox tab). */
const MANAGED_SETTINGS_FORM_KEYS = MANAGED_ONLY_KEYS.filter(
  (k) => !k.key.includes('.'),
)

export default function ClaudePolicyView() {
  const { t } = useTranslation(['claudePolicy', 'common'])
  const { can } = useAuth()
  const canRead = can('governance:claude-policy:read')
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

  if (!canRead) {
    return (
      <div className="flex flex-col gap-5 pb-10">
        <PageHeader
          title={t('title')}
          description={t('subtitle')}
          icon={ScrollText}
        />
        <ForbiddenState />
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-5 pb-10">
      <PageHeader
        title={t('title')}
        description={t('subtitle')}
        icon={ScrollText}
      />

      <RecordingNotice namespace="claude-policy" />

      <Tabs value={tab} onValueChange={(v) => setTab(v as TabKey)}>
        <TabsList>
          <TabsTrigger value="drift">{t('tabs.drift')}</TabsTrigger>
          <TabsTrigger value="managed-settings">
            {t('tabs.managedSettings')}
          </TabsTrigger>
          <TabsTrigger value="hooks">{t('tabs.hooks')}</TabsTrigger>
          <TabsTrigger value="managed-mcp">{t('tabs.managedMcp')}</TabsTrigger>
          <TabsTrigger value="sandbox">{t('tabs.sandbox')}</TabsTrigger>
          <TabsTrigger value="policy-as-code">
            {t('tabs.policyAsCode')}
          </TabsTrigger>
          <TabsTrigger value="hitl">{t('tabs.hitl')}</TabsTrigger>
        </TabsList>

        <TabsContent value="drift">
          <DriftView active={tab === 'drift'} />
        </TabsContent>

        <TabsContent value="managed-settings">
          <PolicyAuthoringPanel
            surface="managed-settings"
            formKeys={MANAGED_SETTINGS_FORM_KEYS}
            defaultDoc={MANAGED_SETTINGS_DEFAULT}
            reference={<ManagedSettingsReference />}
          />
        </TabsContent>

        <TabsContent value="hooks">
          <PolicyAuthoringPanel
            surface="hooks"
            defaultDoc={HOOKS_DEFAULT}
            reference={<HooksReference />}
          />
        </TabsContent>

        <TabsContent value="managed-mcp">
          <PolicyAuthoringPanel
            surface="managed-mcp"
            defaultDoc={MANAGED_MCP_DEFAULT}
            reference={<ManagedMcpReference />}
          />
        </TabsContent>

        <TabsContent value="sandbox">
          <PolicyAuthoringPanel
            surface="sandbox"
            formKeys={SANDBOX_KEYS}
            formBasePrefix="sandbox."
            defaultDoc={SANDBOX_DEFAULT}
            reference={<SandboxReference />}
          />
        </TabsContent>

        <TabsContent value="policy-as-code">
          <CedarOpaView active={tab === 'policy-as-code'} />
        </TabsContent>

        <TabsContent value="hitl">
          <ManagedAgentsHitl active={tab === 'hitl'} />
        </TabsContent>
      </Tabs>
    </div>
  )
}
