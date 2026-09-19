// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { MoreHorizontal, type LucideIcon } from 'lucide-react'
import {
  Fragment,
  isValidElement,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import { useIsPhone } from '@/lib/hooks/use-is-phone'
import { cn } from '@/lib/utils'
import { PagePrimaryActionSlot, PageSecondaryActionsSlot } from './page-actions'

/**
 * PageHeader — the ONE title line of a management view: the page `<h1>`, its
 * description beside it, a row of secondary controls and, last and rightmost, THE
 * PRIMARY ACTION.
 *
 * ⛔ IT IS ONE LINE NOW, AND IT USED TO BE THREE. Measured on
 *    `192-review-home-1440-light.png` at scale 1: a 36 px icon chip, a 24 px `h1` and a
 *    22–44 px description block — about 82 px before any content, on 77 routes, under a
 *    48 px header, and on fourteen of them under a further 36 px notice. The capture
 *    review put that at *"the top 190 px"*; the owner put it as *"igual de fea y
 *    antigua"*. In 23 captures of the product named as the standard, the number of
 *    screens with a description paragraph under a heading is ZERO.
 *
 * ⇒ WHAT CHANGED, AND WHAT DID NOT:
 *    · the icon chip is gone — the rail row and the breadcrumb already name the screen,
 *      and a third copy of its glyph cost 36 px of every viewport;
 *    · the `h1` steps down the SAME ladder, from `text-display` to `text-title`;
 *    · the description stays in the DOM, on the title line, as ONE truncating line with
 *      the full text as its `title`. It is not deleted and it is not hidden: a
 *      screen-reader user reads exactly what they read before, and a sighted user reads
 *      it in 28 px instead of 82.
 *
 *    The `icon` prop is kept and ignored on purpose: 33 views pass it, and a required
 *    change at 33 call sites to remove 36 px would be a merge conflict with every lane
 *    in flight for no behaviour. It is deprecated in place — see the prop.
 *
 * ⛔ WHY `primaryAction` IS ITS OWN SLOT AND NOT "the first child of `actions`".
 *    A review of the console recorded the defect this closes: the front door offered
 *    "six read-only cards, no action anywhere on the page". A single `actions` bag cannot be measured — a header with a range
 *    picker in it looks, to any test and to any census, exactly like a header with a
 *    "New policy" button in it. A named slot can: `page-header.test.tsx` asserts that
 *    the primary action is the LAST control in the header, and a screen that offers
 *    nothing says so by leaving the slot empty rather than by hiding it among filters.
 *
 *    A TABBED screen fills this slot from inside its active tab, through
 *    `PagePrimaryAction` — see page-actions.tsx for why that is a slot and not a prop.
 *
 * ⛔ AND WHY THE HEADING NO LONGER SPELLS ITS OWN TYPE. It used to be
 *    `font-display text-xl font-semibold tracking-tight` written by hand — four
 *    decisions, repeated in six places with three different sizes (measured 2026-09-17:
 *    `text-xl` here and in IntelPage, `text-lg` in login/setup/accept-invite/tenant-gate,
 *    `text-2xl` in settings and the status page). `text-title` is one token that
 *    carries size, leading, tracking and weight together (web/tokens/primitives.tokens.json,
 *    the `type` group), so the ladder moves in one place or not at all — which is
 *    exactly what the work-first pass then used to step the heading DOWN one rung,
 *    in one edit, for all 77 routes. A hand-written stack would have been 77 edits, or (more likely) six.
 */
export interface PageHeaderProps {
  title: ReactNode
  description?: ReactNode
  /**
   * @deprecated Accepted and NOT rendered since the work-first pass. The icon chip
   * cost 36 px
   * of every route's viewport to repeat a glyph the rail row and the breadcrumb already
   * carry. The prop stays so the 33 views that pass it keep compiling; a view that wants
   * an icon for its own content puts it in its own content.
   */
  icon?: LucideIcon
  /**
   * Secondary controls: filters, a range picker, an export, a refresh. Never the
   * verb the page exists for — that is `primaryAction`.
   */
  actions?: ReactNode
  /**
   * THE verb of this screen, rendered last so it is the rightmost control in the
   * header and the first one a reader's eye lands on coming off the title.
   * Omit it only when the screen genuinely offers nothing to do.
   */
  primaryAction?: ReactNode
  /** Notices rendered under the header (honesty markers, caveats, partial reads). */
  notices?: ReactNode
  className?: string
  /**
   * What the phone panel of collapsed controls hangs from. `header` (default): this
   * header, whose right edge is where its controls are. `row`: the positioned row that
   * holds this header beside a tab strip (`WORK_CHROME_ROW`) — there the header is only
   * as wide as its name, so a panel hung from it starts mid-screen and runs off the left
   * edge of a phone; hung from the row it ends at the row's right edge, inside the view.
   */
  actionsPanelAnchor?: 'header' | 'row'
  /**
   * The element the title row is rendered as. `div` by default, because that is what
   * this primitive has always emitted and 32 views depend on it; `IntelPage` passes
   * `header`, which is what ITS 26 views have always emitted. Neither is a landmark
   * inside `<main>`, so the a11y inventory is unchanged either way — the prop exists
   * so consolidating the two headers changed no DOM, and that is checkable.
   */
  as?: 'header' | 'div'
}

/**
 * Does the control row hold a control RIGHT NOW?
 *
 * ⛔ IT IS A DOM QUESTION AND NOT A PROP QUESTION, and that is forced by the slots. A
 *    tabbed screen declares its verb from inside the active tab and it arrives through
 *    a portal (page-actions.tsx), so at the moment this header renders, `actions` and
 *    `primaryAction` can both be undefined and the row can still be about to hold two
 *    buttons. Reading the props would therefore answer "empty" on exactly the screens
 *    that have the most to offer.
 *
 * ⛔ AND IT COUNTS ELEMENTS, NOT PIXELS. Below `sm` the row is `display:none` until the
 *    operator opens it, and a hidden element measures 0 — so a width test would say
 *    "no controls", hide the button that reveals them, and strand every verb on a
 *    phone. `querySelector` sees the DOM whether or not it is painted.
 *
 * The observer is what keeps it true: the portal mutates the row AFTER this effect
 * runs, and a tab switch mutates it again with no re-render of this component.
 */
function useHasControls(row: { current: HTMLElement | null }): boolean {
  const [has, setHas] = useState(false)
  useLayoutEffect(() => {
    const el = row.current
    if (!el) return
    const look = () =>
      setHas(
        !!el.querySelector(
          'button, a, input, select, textarea, [role="button"], [role="switch"]',
        ),
      )
    look()
    const observer = new MutationObserver(look)
    observer.observe(el, { childList: true, subtree: true })
    return () => observer.disconnect()
  }, [row])
  return has
}

/**
 * THE WHOLE OF A DESCRIPTION THAT IS NOT A STRING, or `undefined` when it cannot be
 * known — for the `title` of the one line it is painted on.
 *
 * ⛔ WHY IT EXISTS: `title` takes a string, so a header whose description is a NODE used
 *    to get no tooltip at all — and the description is the element this header truncates
 *    by design. Measured at 1440 on the two session screens, whose description carries
 *    the session counts and the scope note as nodes: 1454 px of text painted in 797,
 *    with no `title` and therefore no way to read the rest. Every other screen, whose
 *    description is a plain string, had one.
 *
 * ⛔ AND WHY IT ANSWERS `undefined` RATHER THAN WHAT IT MANAGED TO READ. A COMPONENT may
 *    render anything — a relative time, a live count, a translated fragment — and this
 *    function cannot see through it. Half a sentence on a tooltip that claims to be the
 *    whole of a truncated one is worse than no tooltip: the reader has no way to tell
 *    which half they are missing. So it reads strings, numbers, arrays, fragments and
 *    intrinsic elements, and gives up honestly on anything else.
 */
export function plainText(node: ReactNode): string | undefined {
  if (node == null || typeof node === 'boolean') return ''
  if (typeof node === 'string') return node
  if (typeof node === 'number') return String(node)
  if (Array.isArray(node)) {
    const parts = node.map(plainText)
    return parts.some((part) => part === undefined) ? undefined : parts.join('')
  }
  if (isValidElement(node)) {
    const { type } = node
    if (typeof type !== 'string' && type !== Fragment) return undefined
    return plainText((node.props as { children?: ReactNode }).children)
  }
  return undefined
}

/**
 * The 36 px row a tabbed screen shares between its header and its tab strip.
 *
 * WHO GIVES WAY, IN ORDER: the description first (it asks for no width of its own), then
 * the tab strip (it scrolls under its own buttons), and never the screen's name or its
 * actions — the header's track cannot go below them (`min-content`, and the name does not
 * wrap, so that floor is the whole name). The strip's track is `auto`: it takes its natural
 * width when the row has it, and when its tabs are wider than the row can give it scrolls
 * under its own buttons — a screen with many tabs needs that affordance at any width.
 * `relative`: the row is what a phone's panel of collapsed controls hangs from.
 */
export const WORK_CHROME_ROW =
  'relative grid h-9 min-w-0 grid-cols-[minmax(min-content,1fr)_minmax(0,auto)] items-center gap-3'

export function PageHeader({
  title,
  description,
  actions,
  primaryAction,
  notices,
  className,
  actionsPanelAnchor = 'header',
  as: Tag = 'div',
}: PageHeaderProps) {
  const { t } = useTranslation('common')
  const row = useRef<HTMLDivElement>(null)
  const hasControls = useHasControls(row)
  const isPhone = useIsPhone()
  const [overflowOpen, setOverflowOpen] = useState(false)

  // The control row always renders: a TABBED screen declares its verb from inside the
  // active tab (`PagePrimaryAction`), which mounts after this header and cannot be seen
  // from here. An empty row is a zero-width flex item and moves nothing.
  return (
    // Named so a test can WALK this header — every `.truncate` in it owes the reader a
    // `title`, and the one that stopped carrying one was found in a browser instead.
    <div
      data-slot="page-header"
      className={cn('flex flex-col gap-2', className)}
    >
      {/* ⛔ ONE ROW AT EVERY WIDTH, AND IT USED TO STACK BELOW `sm`. Measured in the
          browser at 390 px: `flex-col` put the controls UNDER the title and the header
          block went from 28 px to 55 — on a 844 px phone, a fifth of what is left after
          the shell header, spent on a second row of chrome. The budget is 40.
          Stacking is the obvious answer to "it does not fit" and it is the wrong one
          here: a phone has less vertical room than a desktop, not more. */}
      <Tag className="flex items-center justify-between gap-2 sm:gap-4">
        {/* THE TITLE LINE. `min-w-0` on the row AND on the description, or the
            description's own text sets a floor and pushes the controls off the right
            edge instead of truncating — the failure mode measured on five
            other screens. The heading is `shrink-0`: a screen's NAME is never the
            thing that gives way.

            ⛔ AND THE CLIP IS HERE, ON THE TITLE, NOT ON THE ROW THAT HOLDS THE PANEL.
               Four screens wrapped this header in a 36 px `overflow-hidden` row, and the
               phone disclosure opens a panel `absolute top-full` INSIDE it: measured at
               390×844 over these sources, the panel's box ran y 94→168 inside a row that
               ended at y 96, `elementFromPoint` at its centre answered `th`/`td`/the
               document, and a click aimed at Launch, Refresh, Register or Export landed
               on the table underneath. Four screens out of four, with `aria-expanded` and
               the `hidden` class both correct — which is why nothing in jsdom saw it.
               A title that overruns still has to be cut, so the cut happens on the block
               that overruns: the heading and its one truncating line. */}
        <div className="flex min-w-0 flex-1 items-baseline gap-2 overflow-hidden">
          {/* `whitespace-nowrap`: the name's smallest width is the WHOLE name. Left to
              wrap, its smallest width is its longest word, the shared row's floor was set
              from that, and a two-word name was painted cut after its first word. */}
          <h1 className="shrink-0 whitespace-nowrap font-display text-title text-foreground">
            {title}
          </h1>
          {description != null && (
            <p
              // `w-0 flex-1`: the sentence takes the room that is LEFT and asks for none.
              // With an intrinsic width it set the floor of the whole header, and on a
              // row shared with a tab strip it took the strip's room: measured at 1440,
              // five tabs were crushed to a sliver between two scroll buttons and the
              // end button sat on the primary action.
              className="w-0 min-w-0 flex-1 truncate text-caption text-muted-foreground"
              // The full sentence, for a reader whose viewport cut it. `title` takes a
              // string, so a description written as a NODE is read out of the node
              // itself (`plainText`) — and left off entirely when part of it comes from
              // a component this cannot see through, because half a sentence presented
              // as the whole of one is worse than none.
              title={
                typeof description === 'string'
                  ? description
                  : plainText(description)
              }
            >
              {description}
            </p>
          )}
        </div>
        <div
          className={cn(
            'flex shrink-0 items-center gap-2',
            actionsPanelAnchor === 'header' && 'relative',
          )}
        >
          {/* ⛔ THE CONTROLS ARE COLLAPSED, NOT DROPPED. A phone header that hides the
              secondary controls and keeps the verb would meet the budget and lose the
              refresh, the range picker and the export — this pass's own rule is that
              nothing leaves the screen, it changes where it is reached from. So below
              `sm` the same nodes move behind this disclosure; at `sm` and above they
              are back in the row and this button does not exist. They are MOUNTED
              either way (`hidden`, not unmounted), so a control that syncs state on
              mount keeps doing it on a phone. */}
          {isPhone && hasControls && (
            <button
              type="button"
              aria-expanded={overflowOpen}
              aria-label={t('a11y.pageActions')}
              data-testid="page-actions-toggle"
              onClick={() => setOverflowOpen((open) => !open)}
              className={cn(
                'inline-flex size-6 shrink-0 items-center justify-center rounded-md',
                'border border-border text-muted-foreground',
                'transition-colors duration-100 ease-out hover:bg-muted hover:text-foreground',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
              )}
            >
              <MoreHorizontal className="size-4" aria-hidden="true" />
            </button>
          )}
          <div
            ref={row}
            data-slot="page-actions"
            className={cn(
              'flex items-center gap-2',
              isPhone
                ? // A panel under the button, OUT OF FLOW, so the row it came from
                  // costs the header nothing. `hidden` and not unmounted: a control
                  // that registers something on mount keeps doing it on a phone.
                  cn(
                    'absolute right-0 top-full z-20 mt-1 min-w-[11rem] flex-col items-stretch',
                    // A phone is 390 px wide and this panel is anchored to its right edge:
                    // measured on `/audit`, whose header carries a scope select, a filter
                    // popover, saved views and the evidence controls, the panel took its
                    // content width of 455 and started at x=-81 — off the left edge, where
                    // nothing can be pressed. The gutter is the screens' own `px-4`.
                    'max-w-[calc(100vw-2rem)] overflow-x-auto',
                    'rounded-md border border-border bg-elevated p-1 shadow-lg',
                    !overflowOpen && 'hidden',
                  )
                : 'flex-row',
            )}
          >
            {actions}
            <PageSecondaryActionsSlot />
            {primaryAction}
            <PagePrimaryActionSlot />
          </div>
        </div>
      </Tag>
      {notices != null && <div className="flex flex-col gap-2">{notices}</div>}
    </div>
  )
}
