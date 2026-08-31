// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { Archive, Ban } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import type { WorkItem, WorkStatus } from './types'

/** The FSM status, coloured by what it means operationally rather than by mood.
 * Mirrors modules/sessions/work_state.go workStatuses — all eight, so a status the
 * engine can produce never renders as a bare string. */
const STATUS_VARIANT: Record<
  WorkStatus,
  'neutral' | 'accent' | 'success' | 'warning' | 'danger' | 'info' | 'outline'
> = {
  draft: 'outline',
  ready: 'info',
  active: 'accent',
  blocked: 'warning',
  review: 'info',
  completed: 'success',
  failed: 'danger',
  canceled: 'neutral',
}

export function StatusBadge({ item }: { item: WorkItem }) {
  const { t } = useTranslation('work')
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <Badge variant={STATUS_VARIANT[item.status] ?? 'neutral'}>
        {t(`status.${item.status}`)}
      </Badge>
      {/* dependency_blocked is a DERIVED fact the engine computes, distinct from the
          `blocked` status: an item can be `ready` on its own terms and still not be
          startable because a dependency is open. Folding it into the status would hide
          exactly the thing an operator scanning a backlog needs to see. */}
      {item.dependency_blocked ? (
        <Badge variant="warning" className="gap-1">
          <Ban aria-hidden className="size-3" />
          {t('status.dependencyBlocked')}
        </Badge>
      ) : null}
      {item.archived_at ? (
        <Badge variant="outline" className="gap-1">
          <Archive aria-hidden className="size-3" />
          {t('status.archived')}
        </Badge>
      ) : null}
    </span>
  )
}
