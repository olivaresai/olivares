// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Home overview — PURE presentational tile for the estate front door. The home
// is the FIRST screen (README.mdbis: the front door of the dashboards layer, not a
// second executive report), so it leads with a grid of glanceable KPI tiles, each a
// drill-down link to its operational view. The tile reuses the executive primitives
// (LinkTile chrome + MetricStat) so there is ONE source of truth for the tile, never a
// copy (ARCHITECTURE.md — present, never recompute). It renders three HONEST states:
//   • ready       — the figure the source module already decided;
//   • loading     — skeletons (no link, nothing to drill into yet);
//   • unavailable — the source query errored: an em-dash + a muted retry hint, NEVER a
//                   fabricated 0. It still links out so the user can retry
//                   inside the module.
// The container (home-view) owns the queries and the RBAC gating; a tile is only mounted
// for a module the role can read (docs/SECURITY-HARDENING.md) — so a viewer never sees a KPI whose
// module they could not open.
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Skeleton } from '@/components/ui/skeleton'
import { MetricStat, type MetricStatProps } from '@/features/_intel'
import { LinkTile } from '@/features/executive/components'

export type TileState = 'ready' | 'loading' | 'unavailable'

export interface EstateTileProps {
  /** Drill-down route (the feature registry IS the route table, so this is valid). */
  to: string
  icon: ReactNode
  label: ReactNode
  /** The headline figure — only read in the `ready` state. */
  value?: ReactNode
  caption?: ReactNode
  trend?: ReactNode
  tone?: MetricStatProps['tone']
  state: TileState
}

export function EstateTile({
  to,
  icon,
  label,
  value,
  caption,
  trend,
  tone,
  state,
}: EstateTileProps) {
  const { t } = useTranslation('home')

  if (state === 'loading') {
    // No link while loading — there is no figure to drill into yet.
    return (
      <MetricStat
        icon={icon}
        label={label}
        value={<Skeleton className="h-7 w-20" />}
        caption={<Skeleton className="h-3 w-28" />}
      />
    )
  }

  if (state === 'unavailable') {
    // The source errored: never fabricate a 0 — show an em-dash + an honest retry hint,
    // and still link out so the user can open the module and retry there.
    return (
      <LinkTile to={to}>
        <MetricStat
          icon={icon}
          label={label}
          value="—"
          caption={
            <span className="text-muted-foreground">
              {t('state.unavailable')}
            </span>
          }
        />
      </LinkTile>
    )
  }

  return (
    <LinkTile to={to}>
      <MetricStat
        icon={icon}
        label={label}
        value={value}
        caption={caption}
        trend={trend}
        tone={tone}
      />
    </LinkTile>
  )
}
