// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Home overview — PURE presentational tile for the estate front door. The home
// is the FIRST screen (README.md §2bis: the front door of the dashboards layer, not a
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
import {
  CoverageLinkTile,
  LinkTile,
  PartialSourceNote,
  type PartialCoverage,
} from '@/features/executive/components'

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
  /** WHAT THE FIGURE COVERS — a scope disclosure (e.g. "tenant-wide, not filtered by
   *  workspace") rendered under the caption in EVERY state. A figure that could not
   *  load, or is still loading, still has the scope it would have had, and a reader
   *  comparing it with a scoped neighbour must not be told the scope only once it
   *  succeeds. It describes coverage, never a grant and never completeness. */
  scope?: ReactNode
  /** THE FIGURE IS A FLOOR — the source(s) that answered incompletely (an aggregate
   *  that hit its scan ceiling, or a list that returned one page with rows beyond it),
   *  stated by the container. Each becomes a compact, named caption line inside the
   *  tile link, and the tile composes the complete explanation as a disclosure OUTSIDE
   *  the link (`CoverageLinkTile`). Unlike `scope` it is rendered ONLY in the `ready`
   *  state, and that is the point: it qualifies a figure, so with no figure there is
   *  nothing to qualify and a marker left over from a retired answer would describe
   *  data that is no longer on screen. It never replaces the count — a floor is a real
   *  answer, not a missing one. */
  partial?: PartialCoverage
}

/** The caption line plus the optional qualifier lines beneath it: the floor marker
 *  (this figure is partial) and the scope disclosure (what it covers). Both are blocks
 *  inside MetricStat's caption span, so they inherit the caption's muted, small style
 *  and stay attached to the figure they qualify. The marker comes first: it is about
 *  the number just above it, while the scope frames the whole tile. */
function CaptionLines({
  caption,
  partial,
  scope,
}: {
  caption: ReactNode
  partial?: ReactNode
  scope?: ReactNode
}) {
  if (!partial && !scope) return <>{caption}</>
  return (
    <>
      {caption}
      {partial}
      {scope ? <span className="block">{scope}</span> : null}
    </>
  )
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
  scope,
  partial,
}: EstateTileProps) {
  const { t } = useTranslation('home')

  if (state === 'loading') {
    // No link while loading — there is no figure to drill into yet.
    return (
      <MetricStat
        icon={icon}
        label={label}
        value={<Skeleton className="h-7 w-20" />}
        caption={
          <CaptionLines
            caption={<Skeleton className="h-3 w-28" />}
            scope={scope}
          />
        }
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
            <CaptionLines
              caption={
                <span className="text-muted-foreground">
                  {t('state.unavailable')}
                </span>
              }
              scope={scope}
            />
          }
        />
      </LinkTile>
    )
  }

  const partialLines =
    partial && partial.sources.length > 0
      ? partial.sources.map((s) => (
          <PartialSourceNote
            key={s.testId}
            source={s.source}
            kind={s.kind}
            testId={s.testId}
          />
        ))
      : undefined

  return (
    <CoverageLinkTile to={to} partial={partial}>
      <MetricStat
        icon={icon}
        label={label}
        value={value}
        caption={
          partialLines || scope ? (
            <CaptionLines
              caption={caption}
              partial={partialLines}
              scope={scope}
            />
          ) : (
            caption
          )
        }
        trend={trend}
        tone={tone}
      />
    </CoverageLinkTile>
  )
}
