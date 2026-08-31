// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The verified-facts reference panels for each managed-* surface. Everything here
// is rendered from the verified schema constants (no inventes claves) —
// these are the authoritative facts the operator authors against.
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { CaveatNotice, SectionCard } from '@/features/_intel'
import { ReferenceRow } from './components'
import {
  BLOCK_DECISION_EVENTS,
  DOMAIN_FRONTING_CAVEAT,
  DROP_IN_MERGE,
  EGRESS_ALLOWLIST,
  EGRESS_ENV_VARS,
  HOOK_EVENTS,
  MANAGED_MCP_EXCLUSIVE,
  MANAGED_MCP_PATHS,
  MANAGED_SETTINGS_PATHS,
  MCP_EVALUATION_ORDER,
  MCP_GATING_KEYS,
  NOT_MANAGED_ONLY_NOTES,
  PERMISSION_REQUEST_DECISION,
  PLUGIN_MARKETPLACE_GOVERNANCE,
  PRECEDENCE_PERMISSION_RULES_MERGE,
  PRE_TOOL_USE_DECISION,
  SANDBOX_PRIMITIVES,
  SERVER_MANAGED_TIER,
  SETTINGS_PRECEDENCE,
  SOCKS_UNSUPPORTED,
} from './schema'

export function ManagedSettingsReference() {
  const { t } = useTranslation('claudePolicy')
  return (
    <>
      <SectionCard title={t('ref.ms.paths')}>
        <dl>
          <ReferenceRow label="macOS" mono>
            {MANAGED_SETTINGS_PATHS.macos}
          </ReferenceRow>
          <ReferenceRow label="Linux / WSL" mono>
            {MANAGED_SETTINGS_PATHS.linux}
          </ReferenceRow>
          <ReferenceRow label="Windows" mono>
            {MANAGED_SETTINGS_PATHS.windows}
          </ReferenceRow>
        </dl>
        <CaveatNotice className="mt-2">
          {t('ref.ms.legacyPath', {
            since: MANAGED_SETTINGS_PATHS.windowsLegacyRemovedSince,
          })}
        </CaveatNotice>
      </SectionCard>

      <SectionCard title={t('ref.ms.precedence')}>
        <ol className="flex flex-col gap-1">
          {SETTINGS_PRECEDENCE.map((p, i) => (
            <li key={p.scope} className="flex items-baseline gap-2 text-xs">
              <span className="font-mono text-muted-foreground">{i + 1}.</span>
              <span className="font-medium text-foreground">{p.scope}</span>
              <span className="text-muted-foreground">{p.note}</span>
            </li>
          ))}
        </ol>
        <CaveatNotice tone="info" className="mt-2">
          {PRECEDENCE_PERMISSION_RULES_MERGE}
        </CaveatNotice>
      </SectionCard>

      <SectionCard title={t('ref.ms.dropIn', { dir: DROP_IN_MERGE.dir })}>
        <ul className="flex list-disc flex-col gap-1 pl-4 text-xs text-muted-foreground">
          {DROP_IN_MERGE.rules.map((r) => (
            <li key={r}>{r}</li>
          ))}
        </ul>
      </SectionCard>

      <SectionCard title={t('ref.ms.serverManaged')}>
        <dl>
          <ReferenceRow label={t('ref.ms.failClosed')}>
            <code className="font-mono">
              {SERVER_MANAGED_TIER.failClosedKey}
            </code>{' '}
            — {SERVER_MANAGED_TIER.failClosed}
          </ReferenceRow>
          <ReferenceRow label={t('ref.ms.noMerge')}>
            {SERVER_MANAGED_TIER.noMerge}
          </ReferenceRow>
          <ReferenceRow label={t('ref.ms.poll')}>
            {SERVER_MANAGED_TIER.poll}
          </ReferenceRow>
        </dl>
      </SectionCard>

      <SectionCard title={t('ref.ms.notManagedOnly')}>
        <dl>
          {NOT_MANAGED_ONLY_NOTES.map((k) => (
            <ReferenceRow
              key={k.key}
              label={<code className="font-mono">{k.key}</code>}
            >
              {k.summary}
            </ReferenceRow>
          ))}
        </dl>
      </SectionCard>
    </>
  )
}

export function HooksReference() {
  const { t } = useTranslation('claudePolicy')
  return (
    <>
      <CaveatNotice tone="warning">
        {t('ref.hooks.enforcementOff')}
      </CaveatNotice>

      <SectionCard title={t('ref.hooks.decisions')}>
        <dl>
          <ReferenceRow label="PreToolUse">
            <code className="font-mono">permissionDecision</code>:{' '}
            {PRE_TOOL_USE_DECISION.permissionDecisionValues.join(' | ')}.{' '}
            <span className="text-warning">
              {t('ref.hooks.noApplyRuleHere')}
            </span>
          </ReferenceRow>
          <ReferenceRow label="PermissionRequest">
            <code className="font-mono">decision.behavior</code>:{' '}
            {PERMISSION_REQUEST_DECISION.behaviorValues.join(' | ')} +{' '}
            <code className="font-mono">applyPermissionRule</code> (
            {PERMISSION_REQUEST_DECISION.applyPermissionRule.ruleModeValues.join(
              ' | ',
            )}
            ) {t('ref.hooks.applyRuleHere')}
          </ReferenceRow>
        </dl>
      </SectionCard>

      <SectionCard title={t('ref.hooks.events', { count: HOOK_EVENTS.length })}>
        <ul className="flex flex-col gap-1">
          {HOOK_EVENTS.map((e) => (
            <li
              key={e.name}
              className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs"
            >
              <code className="font-mono text-foreground">{e.name}</code>
              {e.governance && (
                <Badge variant="outline" className="text-[0.6rem]">
                  {t('ref.hooks.gov')}
                </Badge>
              )}
              {BLOCK_DECISION_EVENTS.includes(e.name) && (
                <Badge variant="outline" className="text-[0.6rem]">
                  block
                </Badge>
              )}
              <span className="text-muted-foreground">{e.fires}</span>
            </li>
          ))}
        </ul>
      </SectionCard>
    </>
  )
}

export function ManagedMcpReference() {
  const { t } = useTranslation('claudePolicy')
  return (
    <>
      <CaveatNotice tone="info">{MANAGED_MCP_EXCLUSIVE}</CaveatNotice>

      <SectionCard title={t('ref.mcp.paths')}>
        <dl>
          <ReferenceRow label="macOS" mono>
            {MANAGED_MCP_PATHS.macos}
          </ReferenceRow>
          <ReferenceRow label="Linux / WSL" mono>
            {MANAGED_MCP_PATHS.linux}
          </ReferenceRow>
          <ReferenceRow label="Windows" mono>
            {MANAGED_MCP_PATHS.windows}
          </ReferenceRow>
        </dl>
      </SectionCard>

      <SectionCard title={t('ref.mcp.evalOrder')}>
        <ol className="flex flex-col gap-1">
          {MCP_EVALUATION_ORDER.map((s) => (
            <li key={s.step} className="flex items-baseline gap-2 text-xs">
              <span className="font-mono text-muted-foreground">{s.step}.</span>
              <span className="text-foreground">{s.rule}</span>
            </li>
          ))}
        </ol>
        <CaveatNotice tone="warning" className="mt-2">
          {t('ref.mcp.serverNameCaveat')}
        </CaveatNotice>
      </SectionCard>

      <SectionCard title={t('ref.mcp.gatingKeys')}>
        <dl>
          {MCP_GATING_KEYS.map((k) => (
            <ReferenceRow
              key={k.key}
              label={<code className="font-mono">{k.key}</code>}
            >
              {k.summary}
            </ReferenceRow>
          ))}
        </dl>
      </SectionCard>

      <SectionCard title={t('ref.mcp.marketplaces')}>
        <dl>
          <ReferenceRow label={t('ref.mcp.enforcer')} mono>
            {PLUGIN_MARKETPLACE_GOVERNANCE.enforcer}
          </ReferenceRow>
          <ReferenceRow label={t('ref.mcp.matching')}>
            {PLUGIN_MARKETPLACE_GOVERNANCE.matching}
          </ReferenceRow>
          <ReferenceRow label={t('ref.mcp.airGap')} mono>
            {PLUGIN_MARKETPLACE_GOVERNANCE.airGap.seedDir} ·{' '}
            {PLUGIN_MARKETPLACE_GOVERNANCE.airGap.cacheDir}
          </ReferenceRow>
        </dl>
        <ul className="mt-1 flex list-disc flex-col gap-0.5 pl-4 text-xs text-muted-foreground">
          {PLUGIN_MARKETPLACE_GOVERNANCE.behavior.map((b) => (
            <li key={b}>{b}</li>
          ))}
        </ul>
      </SectionCard>
    </>
  )
}

export function SandboxReference() {
  const { t } = useTranslation('claudePolicy')
  return (
    <>
      <CaveatNotice tone="warning">{DOMAIN_FRONTING_CAVEAT}</CaveatNotice>

      <SectionCard title={t('ref.sandbox.primitives')}>
        <dl>
          <ReferenceRow label="macOS">{SANDBOX_PRIMITIVES.macos}</ReferenceRow>
          <ReferenceRow label="Linux / WSL2">
            {SANDBOX_PRIMITIVES.linux}
          </ReferenceRow>
        </dl>
      </SectionCard>

      <SectionCard
        title={t('ref.sandbox.egress')}
        description={t('ref.sandbox.egressSub')}
      >
        <ul className="flex flex-col gap-1">
          {EGRESS_ALLOWLIST.map((e) => (
            <li
              key={e.host}
              className="flex flex-wrap items-baseline gap-x-2 text-xs"
            >
              <code className="font-mono text-foreground">{e.host}</code>
              <span className="text-muted-foreground">{e.purpose}</span>
            </li>
          ))}
        </ul>
      </SectionCard>

      <SectionCard title={t('ref.sandbox.proxy')}>
        <dl>
          {EGRESS_ENV_VARS.map((v) => (
            <ReferenceRow
              key={v.name}
              label={<code className="font-mono">{v.name}</code>}
            >
              {v.summary}
            </ReferenceRow>
          ))}
        </dl>
        <CaveatNotice className="mt-2">{SOCKS_UNSUPPORTED}</CaveatNotice>
      </SectionCard>
    </>
  )
}
