// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { BadgeVariant } from '@/components/ui/badge'

export function criticalityVariant(value: string): BadgeVariant {
  if (value === 'critical') return 'danger'
  if (value === 'high') return 'warning'
  if (value === 'medium') return 'info'
  return 'neutral'
}

export function stalenessVariant(value: string): BadgeVariant {
  if (value === 'stale') return 'danger'
  if (value === 'ok') return 'success'
  return 'neutral'
}

export function enforcementVariant(value: string): BadgeVariant {
  if (value === 'blocked') return 'danger'
  if (value === 'alert') return 'warning'
  return 'neutral'
}

export function offboardVariant(value: string): BadgeVariant {
  if (value === 'finalized') return 'danger'
  if (value === 'soft_deleted') return 'warning'
  return 'outline'
}
