// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { StatusBadge } from '@/components/data/badges'
import { StatusChip, type StatusTone } from '@/components/ui/status-chip'
import '@/features/work/i18n'
import '@/features/agentops/i18n'
import type { EstateNode } from './types'

// Session and work states carry their own words, so they cannot go through StatusBadge.
const TONE: Record<string, StatusTone> = {
  running: 'ok',
  active: 'ok',
  completed: 'ok',
  done: 'ok',
  pending: 'info',
  ready: 'info',
  blocked: 'warn',
  dependencyBlocked: 'warn',
  review: 'warn',
  failed: 'bad',
}

export function EstateStatus({ node }: { node: EstateNode }) {
  const { t } = useTranslation('common')
  if (node.kind !== 'work' && node.kind !== 'session')
    return <StatusBadge status={node.status} />
  const prefix = node.kind === 'work' ? 'work:status.' : 'agentops:state.'
  return (
    <StatusChip tone={TONE[node.status] ?? 'neutral'}>
      {t(prefix + node.status, { defaultValue: node.status || '—' })}
    </StatusChip>
  )
}
