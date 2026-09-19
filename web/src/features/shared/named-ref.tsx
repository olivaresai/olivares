// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cn } from '@/lib/utils'
import { shortRef } from './entity-names'

/**
 * A REFERENCE, PAINTED AS A NAME — one line, name first, identifier where an
 * identifier belongs.
 *
 * ⛔ THE RULE IT CARRIES (visual bar §6: names, never UUIDs). What the reader sees is
 *    the name. The reference stays reachable on `title`, so it can still be read and
 *    copied out of a truncated cell, and it is shown as a muted short form ONLY when
 *    there is no name — because then the reference is the only thing the reader has,
 *    and hiding it would leave a row that identifies nothing.
 *
 * ⛔ AND IT IS ONE LINE. A name stacked over its identifier is what made `/recordings`
 *    paint a 45 px row against a 36 px budget; the second line is the defect, not the
 *    information. Name and short form share the line, and the small one is centred on
 *    it rather than hung from its baseline — two type sizes on one baseline is what
 *    put four other rows at 37 px.
 */
export function NamedRef({
  name,
  reference,
  fallback,
  className,
  mono,
  title,
}: {
  /** The resolved display name, or `null`/`undefined` when it could not be known. */
  name?: string | null
  /** The identifier this row is really about. Always reachable on `title`. */
  reference: string
  /** What to paint when there is no name. A sentence the reader can act on — never
   *  the reference dressed up as a name. */
  fallback: string
  className?: string
  /** Paint the name itself in the monospaced face (a machine reference that IS the
   *  name of the thing, like a host or a slug). */
  mono?: boolean
  /** What `title` carries, when the whole truth is wider than the reference — an audit
   *  target is a KIND and an id, and both belong on the tooltip. Defaults to the
   *  reference. */
  title?: string
}) {
  const label = name?.trim() || fallback
  const short = shortRef(reference)
  // ⛔ AND IT DOES NOT SAY THE SAME THING TWICE. When the fallback IS the reference —
  //    `system`, `token:ci`, a target named by its kind — a short form beside it is
  //    noise that reads like a second fact. The caption appears only when it adds one.
  const showShort = !name?.trim() && !label.includes(short)
  return (
    <span
      className={cn('flex min-w-0 items-center gap-1.5', className)}
      title={title ?? reference}
    >
      <span className={cn('truncate', mono && 'font-mono')}>{label}</span>
      {showShort && (
        <span className="shrink-0 font-mono text-caption leading-none text-muted-foreground">
          {short}
        </span>
      )}
    </span>
  )
}
