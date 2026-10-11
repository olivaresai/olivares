// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE OUTCOME CARD STAYS INSIDE ITS DIALOG, AND THE WHOLE INTENT ID STAYS ON ITS BUTTON.
// "Open intent <uuid>" is 48 characters, and the id may arrive with no hyphen to break at.
// A button that never wraps (`whitespace-nowrap`, fixed height) made the card's min-content
// wider than the 390 px dialog; the dialog is a grid, whose column cannot shrink below a
// child's min-content, so the card and its button ran past the dialog's right edge. A
// button that may break anywhere has almost no min-content, so wrapping is the whole fix.
// One helper now renders that button for all four cards, so the tests that press it and
// that expect no button without an intent id sit here too: they guard the same helper.
// jsdom has no layout, so this pins the arrangement; the browser capture at 390 and
// 1280 px in both themes measures the overflow.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { PublicationOutcome } from './model'
import { OutcomePanel } from './parts'
import type { PublicationIntent } from './types'
import './i18n'

const INTENT_ID = '0b3f6c1e-5d2a-4c8e-9f10-2a3b4c5d6e7f'

const intent: PublicationIntent = {
  id: INTENT_ID,
  target_id: 'tg-1',
  target_version: 2,
  effect: 'push',
  operation_id: 'op-1',
  attempt: 1,
  state: 'applied',
  receipt: 'caused',
  requested: {},
  observed: { present: true, sha: 'a'.repeat(40), merged: false },
  acknowledged: { acknowledged: true, status: 200 },
}

const outcomes: Record<string, PublicationOutcome> = {
  settled: { kind: 'settled', intent },
  uncertain: { kind: 'uncertain', intent: { ...intent, state: 'uncertain' } },
  rejected: {
    kind: 'rejected',
    intent: { ...intent, state: 'rejected', reason: 'host_refused' },
  },
  refused: {
    kind: 'refused',
    code: 'target_busy',
    status: 409,
    intentId: INTENT_ID,
  },
}

const classes = (el: Element) => el.className.split(/\s+/)

describe.each(Object.entries(outcomes))('the %s outcome card', (_, outcome) => {
  function renderCard(onOpenIntent = vi.fn()) {
    render(
      <OutcomePanel
        outcome={outcome}
        onOpenIntent={onOpenIntent}
        canReconcile={false}
      />,
    )
    return screen.getByRole('button', { name: `Open intent ${INTENT_ID}` })
  }

  it('lets the open-intent button wrap, with the whole id on it', () => {
    const open = renderCard()
    expect(open).toHaveTextContent(`Open intent ${INTENT_ID}`)
    expect(classes(open)).toEqual(
      expect.arrayContaining([
        'h-auto',
        'max-w-full',
        'break-all',
        'whitespace-normal',
      ]),
    )
    expect(classes(open)).not.toContain('whitespace-nowrap')
    expect(classes(open)).not.toContain('h-7')
  })

  it('opens that intent, and only that intent, when pressed', async () => {
    const onOpenIntent = vi.fn()
    await userEvent.click(renderCard(onOpenIntent))
    expect(onOpenIntent).toHaveBeenCalledTimes(1)
    expect(onOpenIntent).toHaveBeenCalledWith(INTENT_ID)
  })
})

describe('an outcome that names no intent offers no look at one', () => {
  it.each<[string, PublicationOutcome]>([
    [
      'a refusal before dispatch',
      { kind: 'refused', code: 'target_busy', status: 409 },
    ],
    ['an answer that never arrived', { kind: 'unknown' }],
  ])('%s', (_, outcome) => {
    render(
      <OutcomePanel
        outcome={outcome}
        onOpenIntent={vi.fn()}
        canReconcile={false}
      />,
    )
    expect(
      screen.queryByRole('button', { name: /^Open intent/ }),
    ).not.toBeInTheDocument()
  })
})

describe('the uncertain card keeps reconcile beside the open-intent button', () => {
  it('reconciles that intent, and the open-intent button stays', async () => {
    const onReconcile = vi.fn()
    render(
      <OutcomePanel
        outcome={outcomes.uncertain}
        onOpenIntent={vi.fn()}
        onReconcile={onReconcile}
        canReconcile
      />,
    )
    await userEvent.click(screen.getByRole('button', { name: 'Reconcile' }))
    expect(onReconcile).toHaveBeenCalledWith(INTENT_ID)
    const open = screen.getByRole('button', {
      name: `Open intent ${INTENT_ID}`,
    })
    // Both buttons are shrink-0, so only a wrapping row keeps them inside a 390 px dialog.
    expect(classes(open.parentElement!)).toContain('flex-wrap')
  })
})
