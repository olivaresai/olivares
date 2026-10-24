// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PROOF LINE (#2546, C2-README "from Ledger"): "Chain verified at 16:40 · 12,408
// entries" in the title row, opening the Ledger. The verdict is the one the run read
// carries (`proof.chain_verified`, #2545): the engine re-hashes only the entries appended
// since its last read, so the home asks for no walk of the whole chain. The time is when
// that read answered, the count the ledger's head. No verdict, no line: an unknown chain
// is never shown as verified.
import { Link } from '@tanstack/react-router'
import { ShieldAlert, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { formatInt, formatShortDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import './i18n'

export function ProofLine({
  verified,
  at,
  entries,
}: {
  /** The engine's verdict on the tenant's chain; `null` or absent when it gave none. */
  verified: boolean | null | undefined
  /** When the read that carried the verdict answered (epoch ms). */
  at: number
  /** The ledger's head sequence; `null` when this person may not read the ledger. */
  entries: number | null
}) {
  const { t } = useTranslation('home')
  if (verified == null) return null
  // The time alone today, with the day otherwise, in the person's own hour cycle.
  const time = formatShortDateTime(new Date(at).toISOString())
  const text = verified
    ? [
        t('proof.verified', { time }),
        entries !== null
          ? t('proof.entries', { count: entries, n: formatInt(entries) })
          : null,
      ]
        .filter(Boolean)
        .join(' · ')
    : t('proof.broken')
  const Icon = verified ? ShieldCheck : ShieldAlert
  return (
    <Link
      to={'/audit' as never}
      data-testid="estate-proof"
      className={cn(
        'inline-flex min-h-9 items-center gap-1.5 rounded-full border border-line px-3 text-caption tabular-nums outline-none transition-colors hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus',
        verified ? 'text-text-2' : 'border-danger-line text-danger',
      )}
    >
      <Icon aria-hidden className="size-3.5 shrink-0" />
      {text}
    </Link>
  )
}
