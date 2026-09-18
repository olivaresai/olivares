// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

/**
 * EmptyState — the calm "nothing here yet" placeholder for a list, table, or panel
 * that has no rows (a fresh deployment, a filtered view with no matches). This is NOT
 * an error — it must read as expected and unalarming (use ErrorState for failures,
 * and ForbiddenState for permission/paywall views).
 *
 * ⛔ `description` IS REQUIRED, AND THAT IS THE POINT.
 *
 *    An earlier review measured the console's 300 `<EmptyState>` call sites and found
 *    94 that carried neither a description nor an action; re-measured here with
 *    `src/test/empty-state-census.ts` the true figure was **100**, because six more
 *    passed `description=""` and a census that only asks "is the prop present" counts
 *    those as described. A title saying "no data" and nothing telling the operator
 *    what the surface will hold is the reported defect, stated as a number.
 *
 *    All 100 are fixed, and this type is what keeps them fixed. A census would find
 *    the 101st the day after someone writes it; a REQUIRED prop cannot be written
 *    past, because TypeScript demands a required property be present even when its
 *    type admits `undefined` — and `NonNullable<ReactNode>` closes the
 *    `description={undefined}` door as well. `tsc -b`, which every build and every
 *    `task test:web` already runs, IS the gate. The census stays as the SECOND
 *    instrument: the compiler cannot see that a description is the empty string, and
 *    it cannot count how many empty states offer an action.
 *
 *    The cost is stated rather than discovered: an `<EmptyState>` written without a
 *    description gets a compile error, and the error IS the requirement.
 *
 * ⛔ ONE PRIMARY ACTION, and `secondaryAction` is deliberately quieter. An empty state
 *    that offers three equal buttons has not decided what the operator should do next,
 *    which is the same defect as offering none.
 */
export interface EmptyStateProps extends Omit<
  HTMLAttributes<HTMLDivElement>,
  'title'
> {
  /** A lucide icon element, e.g. <Inbox />. Sized down to 20px in a muted chip. */
  icon?: ReactNode
  title: ReactNode
  /**
   * ONE sentence: what this surface will show, and — when there is nothing to
   * click — why there is nothing here yet. Required; see the note above.
   */
  description: NonNullable<ReactNode>
  /** The single primary CTA (typically a <Button>), placed below the description. */
  action?: ReactNode
  /**
   * An optional quieter alternative beside the primary one — "Learn more", "Clear
   * the filter", "Open the docs". Never a second way to do the same thing.
   */
  secondaryAction?: ReactNode
}

export function EmptyState({
  icon,
  title,
  description,
  action,
  secondaryAction,
  className,
  ...props
}: EmptyStateProps) {
  return (
    <div
      // ⛔ `data-slot`, la convención del repo (`field`, `select-trigger`), porque `role="status"`
      // NO distingue: lo comparten los spinners y cualquier región viva. Un arnés que quisiera
      // saber «¿esta pantalla salió vacía?» no podía preguntarlo, y por eso el censo de capturas
      // en estado vacío era un número de un informe viejo en vez de una lista medida.
      data-slot="empty-state"
      // role="status" (aria-live polite) so when an async surface resolves to empty
      // — replacing a spinner — a screen reader hears the empty message instead of
      // silence (4.1.3). The genuine-error sibling ErrorState uses role="alert".
      role="status"
      className={cn(
        'flex flex-col items-center justify-center gap-3 px-6 py-12 text-center',
        className,
      )}
      {...props}
    >
      {icon ? (
        <div className="flex size-10 items-center justify-center rounded-lg bg-muted text-muted-foreground [&_svg]:size-5 [&_svg]:shrink-0">
          {icon}
        </div>
      ) : null}
      <div className="flex flex-col items-center gap-1.5">
        <p className="text-heading text-foreground">{title}</p>
        <p className="max-w-sm text-body text-muted-foreground">
          {description}
        </p>
      </div>
      {action || secondaryAction ? (
        <div className="mt-1 flex flex-wrap items-center justify-center gap-2">
          {action}
          {secondaryAction}
        </div>
      ) : null}
    </div>
  )
}
