// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import '@/features/work/i18n'
import '@/features/agentops/i18n'
import type { EstateNode } from './types'

export function EstateStatus({ node }: { node: EstateNode }) {
  const { t } = useTranslation('common')
  const prefix =
    node.kind === 'work'
      ? 'work:status.'
      : node.kind === 'session'
        ? 'agentops:state.'
        : 'common:status.'
  return <>{t(prefix + node.status, { defaultValue: node.status })}</>
}
