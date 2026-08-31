// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// REFERENCE PAGES MUST LEAD TO THE EFFECTIVE STATE.
//
// Platforms and Rate limits are honest read-only reference: they say `(read-only)` in the
// navigation and in six places besides, so there is no management promise to withdraw and
// none is added here. What they lacked is the same thing the access map lacked — a way OUT
// to where the estate's own configuration actually lives. A page that describes what a
// provider supports, with no route to what THIS estate runs, makes the operator go hunting
// through the sidebar for the surface that answers "and here?".
//
// The criterion is the map's, deliberately: the link is offered only when the principal can
// open the target route (`can()` is membership of the effective set from /v1/auth/whoami),
// and it never restates or recomputes the target's data — it navigates. Nothing about the
// reference becomes a second source of truth for configuration.
import { Link } from '@tanstack/react-router'
import { ArrowUpRight } from 'lucide-react'
import type { ReactNode } from 'react'
import { useAuth } from '@/lib/auth/context'

export interface EffectiveStateTarget {
  /** Registered path (registry.tsx). */
  to: string
  /** The permission of the ROUTE, so the link never lands on a Forbidden page. */
  permission: string
  /** Already-translated label — the caller owns its own i18n namespace. */
  label: ReactNode
}

/**
 * A row of "where the effective state lives" links for a reference page header.
 *
 * Renders NOTHING when the principal can open none of the targets: an operator who cannot
 * reach any of them is better served by an unchanged page than by a list of doors that do
 * not open. (The access map names the missing permission instead, because there the link
 * IS the remedy for a finding on screen; here it is orientation.)
 */
export function EffectiveStateLinks({
  targets,
  label,
}: {
  targets: EffectiveStateTarget[]
  /** Lead-in copy, e.g. "This estate's configuration:". */
  label: ReactNode
}) {
  const { can } = useAuth()
  const allowed = targets.filter((target) => can(target.permission))
  if (allowed.length === 0) return null

  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
      <span className="text-muted-foreground">{label}</span>
      {allowed.map((target) => (
        <Link
          key={target.to}
          // The feature registry IS the route table, so these paths are always valid;
          // they are not in the statically-typed tree because the routes are generated.
          to={target.to as never}
          className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 font-medium text-accent-text outline-none transition-colors hover:bg-accent-soft focus-visible:ring-2 focus-visible:ring-ring [&_svg]:size-3.5"
        >
          {target.label}
          <ArrowUpRight aria-hidden />
        </Link>
      ))}
    </div>
  )
}
