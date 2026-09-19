// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A ROW LOCATOR THAT CANNOT LIE, FOR THE SESSIONS TABLE.
//
// ⛔ WHY IT EXISTS, and it is a measured defect rather than a style preference.
//    `8e8aafd19f` made the session cell paint a NAME instead of a raw reference, which
//    is right — an operator reads names. What it cost the tests over it is that the
//    DOM stopped naming which session a row IS: every observed row with no run name,
//    no summary and no goal paints the same `Untitled session`. So
//
//      * a POSITIVE `findByText(ref)` finds nothing, and the test dies on its setup;
//      * a NEGATIVE `queryByText(ref)` → null — *"the row left"* — passes WHATEVER the
//        view does, because that string is not painted for any row, ever.
//
//    The second is the dangerous one: repairing only the first turns a file that guards
//    refusals, partial reads and cancellation into a file that asserts nothing.
//
// SO THE RULES ARE IN THE INSTRUMENT, NOT IN A CONVENTION:
//
//   1. Rows are found by `data-address` — the row ADDRESS the view paints
//      (`sess:`/`live:`/`run:` + the reference), never by visible text.
//   2. `gone()` REFUSES TO PASS unless this locator saw that exact row painted earlier
//      in the same test. A negative with no positive control behind it is a failure of
//      the test, reported as one.
//   3. Every absence assertion also names something true AT THAT MOMENT — a sibling row
//      that must stay, an empty grid, or at the very least a mounted grid — so an
//      unmounted screen can never stand in for "the row left".
//   4. A failure prints the addresses that ARE painted, which is the fact a DOM dump
//      makes you go looking for.

import { waitFor } from '@testing-library/react'

/** The attribute the sessions table writes on a row's session cell. Named once: a
 *  locator that guesses at two spellings is the defect it exists to catch. */
const ROW_REF_ATTR = 'data-address'

/**
 * What the caller asserts is true AT THE MOMENT of an absence — the live half of the
 * positive control, which rules out "the row left" being satisfied by a screen that
 * paints nothing at all.
 */
export type RowControl =
  /** …and THIS sibling row is painted at the same moment. The strongest form, and the
   *  one to use whenever the case has a half that must survive. */
  | { alsoPainted: string }
  /** …and the grid is mounted and paints NO rows: a refusal that replaced the body, or
   *  a current answer that is genuinely empty. */
  | { andNoRowAtAll: true }
  /** …and the grid is mounted, rows mid-flight. The weakest of the three: it rules out
   *  an unmounted screen and nothing more. Every use has to say why no sibling can be
   *  named at that instant. */
  | { andTheGridIsMounted: true }

export interface SessionRowLocator {
  /** The row (`<tr>`) painted for `address` right now, or null. No assertion. */
  query(address: string): HTMLElement | null
  /** Every row address painted right now, in DOM order. */
  painted(): string[]
  /** Wait for the row and return it, failing under `name`. Records the sighting, which
   *  is what later licenses a `gone()` about the same address. */
  find(address: string, name: string): Promise<HTMLElement>
  /** The row is painted right now — the synchronous positive control. Records it. */
  get(address: string, name: string): HTMLElement
  /**
   * "The row LEFT." Validates the two halves of the positive control HARD — a prior
   * sighting of that exact address by this locator, and `control` being true right now
   * — and then RETURNS the row (or null) for the caller to assert on:
   *
   *     expect.soft(rows.gone(addr, name, { alsoPainted: other }), name).toBeNull()
   *
   * The split is deliberate. A missing control is a defect in the TEST and must stop it
   * at once; whether the row is still painted is a fact about the VIEW, and these files
   * report several of those per case with `expect.soft` so one regression does not hide
   * the next.
   */
  gone(address: string, name: string, control: RowControl): HTMLElement | null
  /** "The row was NEVER admitted" — a frame's payload, a stranger's page. No prior
   *  sighting is possible, so the control carries the whole weight and the caller must
   *  have proven elsewhere in the case that the locator does see admitted rows. Same
   *  return contract as `gone`. */
  neverPainted(
    address: string,
    name: string,
    control: RowControl,
  ): HTMLElement | null
}

function gridIsMounted(): boolean {
  return !!document.querySelector('[role="grid"]')
}

function refOf(el: Element): string {
  return el.getAttribute(ROW_REF_ATTR) ?? ''
}

function markers(): HTMLElement[] {
  return Array.from(
    document.querySelectorAll<HTMLElement>(`[${ROW_REF_ATTR}]`),
  ).filter((el) => !!el.closest('[role="grid"]'))
}

/**
 * One locator per test. `createSessionRowLocator()` in the `beforeEach` that resets the
 * harness: the sighting record is per-case on purpose, so one test's positive control
 * can never license another test's negative.
 */
export function createSessionRowLocator(): SessionRowLocator {
  const seen = new Set<string>()

  const query = (address: string): HTMLElement | null => {
    const hits = markers().filter((el) => refOf(el) === address)
    if (hits.length > 1) {
      throw new Error(
        `session-row-locator: ${hits.length} rows painted for ${address} — ` +
          `the address is supposed to be the row's identity. Painted: ${JSON.stringify(painted())}`,
      )
    }
    const row = hits[0]?.closest('tr')
    return (row as HTMLElement | null) ?? null
  }

  const painted = (): string[] => markers().map(refOf)

  const record = (address: string) => {
    seen.add(address)
  }

  const describeScreen = () =>
    gridIsMounted()
      ? `painted now: ${JSON.stringify(painted())}`
      : 'no grid is mounted at all (is the table tab open? did the view unmount?)'

  const checkControl = (name: string, control: RowControl) => {
    if ('alsoPainted' in control) {
      if (!query(control.alsoPainted)) {
        throw new Error(
          `${name}: the POSITIVE CONTROL is missing — ${control.alsoPainted} was ` +
            `supposed to be painted at this moment, so the absence above proves ` +
            `nothing. ${describeScreen()}`,
        )
      }
      return
    }
    if (!gridIsMounted()) {
      throw new Error(
        `${name}: the POSITIVE CONTROL is missing — no grid is mounted, so "the row ` +
          `left" is indistinguishable from "there is no table". ${describeScreen()}`,
      )
    }
    if ('andNoRowAtAll' in control && painted().length > 0) {
      throw new Error(
        `${name}: the control said the grid paints no rows, and it paints ` +
          `${JSON.stringify(painted())}.`,
      )
    }
  }

  const absent = (
    address: string,
    name: string,
    control: RowControl,
    requireSighting: boolean,
  ): HTMLElement | null => {
    if (requireSighting && !seen.has(address)) {
      throw new Error(
        `${name}: this case never SAW ${address} painted, so asserting it left is ` +
          `vacuous. Find the row with the same locator first, or use neverPainted() ` +
          `if the claim is that it was never admitted. ${describeScreen()}`,
      )
    }
    checkControl(name, control)
    return query(address)
  }

  return {
    query,
    painted,
    async find(address, name) {
      let row: HTMLElement | null = null
      await waitFor(() => {
        row = query(address)
        if (!row) {
          throw new Error(
            `${name}: ${address} is not painted. ${describeScreen()}`,
          )
        }
      })
      record(address)
      return row as unknown as HTMLElement
    },
    get(address, name) {
      const row = query(address)
      if (!row) {
        throw new Error(
          `${name}: ${address} is not painted. ${describeScreen()}`,
        )
      }
      record(address)
      return row
    },
    gone(address, name, control) {
      return absent(address, name, control, true)
    },
    neverPainted(address, name, control) {
      return absent(address, name, control, false)
    },
  }
}

/** The address of a LEGACY observed row — what `liveRowKey` builds for an unscoped
 *  live row. Spelled out here rather than imported: a test that derives the address
 *  with the production function only proves the view agrees with itself. */
export const sessAddress = (sessionRef: string) => `sess:${sessionRef}`
/** The address of a run-only row, and of a profiled run with no proven managed row. */
export const runAddress = (runRef: string) => `run:${runRef}`
/** The address of a profile-scoped observed row. */
export const liveAddress = (liveRef: string) => `live:${liveRef}`
