// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { HTMLAttributes, ReactNode } from 'react'
import { BrandMark } from '@/components/layout/brand'
import { cn } from '@/lib/utils'

/**
 * THE STATE PAGE, as direction A2 draws it (a status page): a large symbolic icon or the
 * product's own mark, a real title at 22/800, one sentence at 15 px, one action. One source
 * for every state that draws it: EmptyState, ErrorState, ForbiddenState, StateBlock, the
 * module-off page and the page that names the edition.
 */
export const STATE_PAGE =
  'mx-auto flex w-full max-w-120 flex-col items-center justify-center gap-2 px-6 pt-26 pb-24 text-center'
/** The large symbolic icon: 72 px with a 2.6 px stroke; its colour is the state's. */
export const STATE_ICON =
  'mb-[18px] flex [&_svg]:size-18 [&_svg]:shrink-0 [&_svg]:stroke-[0.875]'
export const STATE_TITLE =
  'm-0 text-[1.375rem]/7 font-extrabold tracking-[-0.006em] text-balance text-text'
export const STATE_SENTENCE =
  'm-0 max-w-[42ch] text-body-l text-pretty text-text-2'
export const STATE_ACTIONS =
  'mt-5 flex flex-wrap items-center justify-center gap-2'

/**
 * `panel` is the same page inside a side panel, a sheet, a card or a table body: 32 px
 * above and below, a 40 px face (the icon or the mark) and the title at the heading step.
 */
export type StateSize = 'page' | 'panel'
export const STATE_PANEL = {
  page: 'py-8',
  icon: '[&_svg]:size-10',
  mark: 'size-10',
  title: 'text-heading',
} as const

/**
 * The face of an empty page and of the page that names the edition: the four-row mark
 * (BrandMark) at 64 px, never another drawing of it and never the name beside it.
 */
export function StateMark({ size = 'page' }: { size?: StateSize }) {
  return (
    <div data-slot="state-mark" className="mb-5 flex text-text">
      <BrandMark className={size === 'panel' ? STATE_PANEL.mark : 'size-16'} />
    </div>
  )
}

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

interface EmptyStateBase extends Omit<HTMLAttributes<HTMLDivElement>, 'title'> {
  title: ReactNode
  /**
   * ONE sentence: what this surface will show, and — when there is nothing to
   * click — why there is nothing here yet. Required; see the note above.
   */
  description: NonNullable<ReactNode>
}

/**
 * ⛔ AND THE OTHER HALF OF THE RULE: SOME SURFACES ARE EMPTY BECAUSE NOTHING IS WRONG.
 *
 *    "One sentence and the next action" is written for a surface that is empty because
 *    work has not been done yet. An approvals queue with nothing in it, a policy with
 *    no drift, a kill switch never pulled, an identity plane with no leavers — those
 *    are the good state, and manufacturing a button for them invents work the operator
 *    does not owe. Measured on the seeded estate: four of the ten empty states with no
 *    action are exactly this, and the rest of the bar still applies to them — one
 *    sentence, no grey panel with a large icon.
 *
 *    `quiet` is that state, and the type is what keeps it honest: a quiet state CANNOT
 *    carry an action. It is a compile error, not a review comment.
 */
type QuietProps =
  | {
      quiet: true
      action?: never
      secondaryAction?: never
      icon?: never
      size?: never
    }
  | {
      quiet?: false
      /** `panel` inside a side panel, a sheet, a card or a table body. */
      size?: StateSize
      /** Kept so no caller changes. An empty page's face is the product's mark
       *  (direction A2), so the place icon is no longer drawn. */
      icon?: ReactNode
      /** The single primary CTA (typically a <Button>), below the description,
       *  drawn as a pill. */
      action?: ReactNode
      /** An optional quieter alternative beside it — "Learn more", "Clear the
       *  filter". Never a second way to do the same thing. */
      secondaryAction?: ReactNode
    }

export type EmptyStateProps = EmptyStateBase & QuietProps

export function EmptyState({
  icon: _placeIcon,
  title,
  description,
  action,
  secondaryAction,
  quiet,
  size,
  className,
  ...props
}: EmptyStateProps) {
  if (quiet) {
    return (
      <div
        data-slot="empty-state"
        data-quiet="true"
        role="status"
        className={cn(
          // ONE LINE, left where the rows would have started, at the weight of a
          // caption: the reader's eye passes over it instead of stopping at a panel.
          'flex items-baseline gap-2 px-3 py-4 text-body',
          className,
        )}
        {...props}
      >
        <span className="text-text">{title}</span>
        <span className="min-w-0 text-caption text-text-2">{description}</span>
      </div>
    )
  }
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
        STATE_PAGE,
        size === 'panel' && STATE_PANEL.page,
        className,
      )}
      {...props}
    >
      <StateMark size={size} />
      <p className={cn(STATE_TITLE, size === 'panel' && STATE_PANEL.title)}>
        {title}
      </p>
      <p className={STATE_SENTENCE}>{description}</p>
      {action || secondaryAction ? (
        // The state page's one action is a pill (the button has no pill size yet).
        <div className={cn(STATE_ACTIONS, '*:rounded-full')}>
          {action}
          {secondaryAction}
        </div>
      ) : null}
    </div>
  )
}
