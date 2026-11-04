// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ClaudePolicyView (registry id 'claude-policy') — the Claude Code governance
// console: author managed-settings / hooks / managed-mcp / sandbox policy and
// Cedar/OPA policy-as-code, and read the PERMITTED-vs-OBSERVED drift the engine
// emits. Presentation + audited action over the SAME API (ARCHITECTURE.md): authoring is
// wired to the DECLARED contract with an honest pending seam; drift is real.
import { ScrollText } from 'lucide-react'
import { useTabParam } from '@/lib/hooks/use-tab-param'
import { useTranslation } from 'react-i18next'
import { ForbiddenState } from '@/components/ui/error-state'
import { PageHeader } from '@/components/ui/page-header'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { RecordingNoticeSlot } from '@/features/recording-notice-slot'
import { useAuth } from '@/lib/auth/context'
import { CedarOpaView } from './cedar-opa-view'
import { DepartmentInstructionsPanel } from './department-instructions-panel'
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
  'department-instructions',
  'policy-as-code',
  'hitl',
] as const

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
  // `?tab=` is the section: the access map links straight to policy-as-code, and a reload
  // or Back keeps it. Unknown values fall back to the first tab.
  const [tab, setTab] = useTabParam(TAB_VALUES)

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

      <RecordingNoticeSlot namespace="claude-policy" />

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="drift">{t('tabs.drift')}</TabsTrigger>
          <TabsTrigger value="managed-settings">
            {t('tabs.managedSettings')}
          </TabsTrigger>
          <TabsTrigger value="hooks">{t('tabs.hooks')}</TabsTrigger>
          <TabsTrigger value="managed-mcp">{t('tabs.managedMcp')}</TabsTrigger>
          <TabsTrigger value="sandbox">{t('tabs.sandbox')}</TabsTrigger>
          <TabsTrigger value="department-instructions">
            {t('tabs.departmentInstructions')}
          </TabsTrigger>
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

        <TabsContent value="department-instructions">
          <DepartmentInstructionsPanel />
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
