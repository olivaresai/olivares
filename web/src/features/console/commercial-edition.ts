// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useCallback } from 'react'
import {
  isPaidEdition,
  type Edition,
  type PaidEdition,
} from '@/components/ui/edition-note'
import { useServerInfo } from '@/lib/hooks/use-server-info'

type EditionFacts = {
  edition?: string
  /** The edition the live licence entitles: community | engineer | business | enterprise.
   * Absent on an engine that does not name it. */
  licensed_edition?: string
  license?: { status: string; support_profile?: string }
}

/** Only a Business or Enterprise build holds a license above Community: a Community
 * build refuses every placed route at Community, whatever license it shows. */
function commercialBuild(info: EditionFacts | undefined): info is EditionFacts {
  return ['business', 'enterprise'].includes(info?.edition ?? '')
}

/** A build is not a licence. Only current valid commercial facts get a badge: the edition
 * the engine names, else the guess from the support profile. */
export function commercialEdition(
  info: EditionFacts | undefined,
): PaidEdition | null {
  if (!commercialBuild(info)) return null
  if (info.licensed_edition !== undefined)
    return isPaidEdition(info.licensed_edition) ? info.licensed_edition : null
  if (info.license?.status !== 'valid') return null
  return info.license.support_profile === 'enterprise'
    ? 'enterprise'
    : 'business'
}

/** The editions in the engine's order: each holds what the ones before it hold. */
const EDITION_ORDER: readonly Edition[] = [
  'community',
  'engineer',
  'business',
  'enterprise',
]

/**
 * Whether the licensed edition holds what `minimum` holds: the engine's own rule
 * (license.EditionCovers, core/license/envelope.go). An absent minimum is always held; an
 * unknown or absent licensed value holds nothing above Community.
 */
export function editionCovers(
  licensed: string | null | undefined,
  minimum?: PaidEdition,
): boolean {
  if (!minimum) return true
  const rank = (EDITION_ORDER as readonly unknown[]).indexOf(licensed)
  return rank >= EDITION_ORDER.indexOf(minimum)
}

/** A registry view or a closed row: the lowest edition that holds it, if any. */
type Placed = { minEdition?: PaidEdition }

/**
 * Whether a view (or a closed row) opens at the edition this installation holds. While
 * server-info has not answered, everything counts as open: the engine is what refuses; the
 * console only explains. Once it has, the licensed edition it names decides, read as named
 * and never guessed: a missing value is a product this build does not know, and it, an
 * unknown value or a Community build holds nothing above Community.
 */
export function useOpensAtEdition(): (view: Placed) => boolean {
  const info = useServerInfo()
  const answered = info.isSuccess === true
  const facts: EditionFacts | undefined = answered ? info.data : undefined
  const licensed = commercialBuild(facts) ? facts.licensed_edition : undefined
  return useCallback(
    (view: Placed) => !answered || editionCovers(licensed, view.minEdition),
    [answered, licensed],
  )
}
