// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHICH ANCESTORS WOULD CUT AN OUT-OF-FLOW PANEL — the walk a screen test makes from an
// open disclosure up to the page root.
//
// ⛔ WHY IT EXISTS. A panel positioned `absolute top-full` inside a 36 px row is painted
//    and cannot be touched: four screens wrapped the page header in
//    `h-9 … overflow-hidden`, and at 390×844 the browser answered `th`, `td` or the
//    document at the centre of the open panel, on all four. `aria-expanded` was `true`,
//    the panel did not carry `hidden`, and both oracles that existed asserted exactly
//    those two things — so a phone with no Launch, no Refresh, no Register and no Export
//    was reported green.
//
// ⛔ AND WHY IT READS CLASSES RATHER THAN COMPUTED STYLE. jsdom loads no stylesheet:
//    `getComputedStyle(el).overflow` answers `visible` for every element in the tree,
//    whatever its classes say, so a walk over computed style is a test that cannot fail.
//    The class list is the evidence available here, and it is the evidence that changed.
//    The COMPUTED fact is measured in a real browser instead —
//    `code-r4/probe/panel.mjs` reads `overflowX`/`overflowY` and `elementFromPoint`, and
//    is the oracle that saw the defect first.

/** The utilities that make an element cut what overflows it. `truncate` is one: it is
 *  `overflow:hidden` with an ellipsis, and on an ANCESTOR it clips a panel just as hard. */
const CLIPS =
  /^(overflow-(hidden|clip|scroll|auto)|overflow-[xy]-(hidden|clip|scroll|auto)|truncate)$/

/** `div[data-slot="work-chrome"]` — enough to name the element in a failure message. */
function describe(el: Element): string {
  const slot = el.getAttribute('data-slot')
  const testId = el.getAttribute('data-testid')
  const tag = el.tagName.toLowerCase()
  const name = slot
    ? `${tag}[data-slot="${slot}"]`
    : testId
      ? `${tag}[data-testid="${testId}"]`
      : tag
  const clipping = [...el.classList].filter((c) => CLIPS.test(c))
  return `${name} .${clipping.join(' .')}`
}

/**
 * Every ancestor of `el`, up to `stop` or the document, that clips its overflow.
 *
 * The answer is a list of descriptions rather than elements so a failing expectation
 * NAMES the row that cut the panel instead of printing an element tree.
 */
export function clippingAncestors(
  el: Element,
  stop?: Element | null,
): string[] {
  const out: string[] = []
  for (
    let node = el.parentElement;
    node && node !== stop && node !== document.body;
    node = node.parentElement
  ) {
    if ([...node.classList].some((c) => CLIPS.test(c))) out.push(describe(node))
  }
  return out
}
