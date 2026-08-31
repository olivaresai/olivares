// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { UntrustedBadge } from '@/components/data/untrusted-badge'
import './i18n'
import type { ToolDTO } from './types'

/**
 * ToolAnnotations renders an MCP tool's declared hints (readOnlyHint /
 * destructiveHint). These are UNTRUSTED (annotation_trust is always 'untrusted',
 * ARCHITECTURE.md): every hint is wrapped in an UntrustedBadge that reads "self-reported
 * — not verified", and the UI never uses them to gate or color a security decision.
 */
export function ToolAnnotations({ tool }: { tool: ToolDTO }) {
  const { t } = useTranslation('capabilities')
  const hint = t('tools.untrustedNote')
  const badges = [] as React.ReactNode[]
  if (tool.read_only_hint) {
    badges.push(
      <UntrustedBadge key="ro" label={t('tools.readOnly')} hint={hint} />,
    )
  }
  if (tool.destructive_hint) {
    badges.push(
      <UntrustedBadge key="d" label={t('tools.destructive')} hint={hint} />,
    )
  }
  if (badges.length === 0) {
    return <span className="text-xs text-muted-foreground">—</span>
  }
  return <div className="flex flex-wrap gap-1">{badges}</div>
}
