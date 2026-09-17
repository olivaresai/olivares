// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PROTECTED DETAIL: read fresh through the CARRIER delivery, replaced rather
// than merged, cleared on close, and never bound to the wrong precondition. A
// terminal or stale offer has its state, its reason and a refresh — and no response
// control at all.
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({ getHandoffDetail: vi.fn() }))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})

import { ApiError, NetworkError } from '@/lib/api/errors'
import { HandoffSheet } from './handoff-sheet'
import {
  deferred,
  handoffDetailOf,
  HANDOFF_DELIVERY_ID,
  HANDOFF_ETAG,
  HANDOFF_ID,
  renderWithQuery,
  scopeOf,
  WORK_ITEM_ID,
} from './test-harness'
import type { HandoffDetail } from './types'
import './i18n'

/** Mutable `open`, so one case can CLOSE the sheet through a re-render and prove
 *  the protected content leaves the document with it. */
const openState = { value: true }

beforeEach(() => {
  vi.clearAllMocks()
  openState.value = true
})

function mount(
  over: {
    canRespond?: boolean
    canDeliveryRead?: boolean
    deliveryId?: string | null
    onRespond?: (t: string, target: unknown) => void
    scope?: ReturnType<typeof scopeOf>
  } = {},
) {
  const onRespond = over.onRespond ?? vi.fn()
  const rendered = renderWithQuery(() => (
    <HandoffSheet
      open
      onOpenChange={() => {}}
      deliveryId={
        over.deliveryId === undefined ? HANDOFF_DELIVERY_ID : over.deliveryId
      }
      scope={over.scope ?? scopeOf()}
      canDeliveryRead={over.canDeliveryRead ?? true}
      canRespond={over.canRespond ?? true}
      onRespond={onRespond as never}
    />
  ))
  return { ...rendered, onRespond }
}

describe('the fresh, Delivery-bound protected read', () => {
  it('reads by the CARRIER delivery id with the captured tenant and an abort signal', async () => {
    api.getHandoffDetail.mockResolvedValue(handoffDetailOf())
    mount()
    await waitFor(() =>
      expect(api.getHandoffDetail).toHaveBeenCalledWith(
        HANDOFF_DELIVERY_ID,
        { tenant: 't1' },
        expect.any(AbortSignal),
      ),
    )
    expect(
      await screen.findByText('Deploy freeze needs an owner'),
    ).toBeVisible()
    expect(screen.getByText(WORK_ITEM_ID)).toBeVisible()
    expect(screen.getByText(HANDOFF_ID)).toBeVisible()
  })

  it('a fresh re-read REPLACES the previous protected content instead of merging with it', async () => {
    api.getHandoffDetail
      .mockResolvedValueOnce(
        handoffDetailOf({
          summary: 'First summary',
          nextAction: 'First action',
        }),
      )
      .mockResolvedValueOnce(
        handoffDetailOf({
          summary: 'Second summary',
          nextAction: 'Second action',
        }),
      )
    const user = userEvent.setup()
    mount()
    expect(await screen.findByText('First summary')).toBeVisible()
    // ASSERTED ON `document.body`, NOT ON THE RENDER CONTAINER. The sheet is
    //    PORTALLED, so `result.container` is empty and every negative assertion made
    //    against it would pass without measuring anything. Caught here by the
    //    positive control below failing on the same selector.
    expect(document.body.textContent).toMatch(/First summary/)
    await user.click(screen.getByRole('button', { name: 'Re-read' }))
    expect(await screen.findByText('Second summary')).toBeVisible()
    // The first read's content is GONE, not stacked underneath the second.
    await waitFor(() =>
      expect(document.body.textContent).not.toMatch(/First summary/),
    )
    expect(document.body.textContent).not.toMatch(/First action/)
  })

  // Two reads of the SAME delivery under an unchanged scope: the slow first read
  // resolves last and must not paint. The workspace A→B→A transition is a
  // different property and is measured on the mounted room, in
  // communications-view.test.tsx.
  it('a slow first read that resolves after a newer one cannot paint over it', async () => {
    const slowA = deferred<HandoffDetail>()
    api.getHandoffDetail
      .mockReturnValueOnce(slowA.promise)
      .mockResolvedValueOnce(handoffDetailOf({ summary: 'B is current' }))
    const user = userEvent.setup()
    mount()
    await waitFor(() => expect(api.getHandoffDetail).toHaveBeenCalledTimes(1))
    // A second explicit read starts and resolves first.
    await user.click(screen.getByRole('button', { name: 'Re-read' }))
    expect(await screen.findByText('B is current')).toBeVisible()
    // …and only now does the FIRST request answer.
    slowA.resolve(handoffDetailOf({ summary: 'A is stale' }))
    await new Promise((r) => setTimeout(r, 0))
    // The newer read is still what the operator sees; the older never appeared.
    expect(document.body.textContent).toMatch(/B is current/)
    expect(document.body.textContent).not.toMatch(/A is stale/)
  })

  it('closing the sheet clears the protected content: it is not kept for the next open', async () => {
    api.getHandoffDetail.mockResolvedValue(
      handoffDetailOf({ summary: 'Protected body' }),
    )
    const { rerender } = renderWithQuery(() => (
      <HandoffSheet
        open={openState.value}
        onOpenChange={() => {}}
        deliveryId={HANDOFF_DELIVERY_ID}
        scope={scopeOf()}
        canDeliveryRead
        canRespond
        onRespond={() => {}}
      />
    ))
    expect(await screen.findByText('Protected body')).toBeVisible()
    expect(document.body.textContent).toMatch(/Protected body/)
    openState.value = false
    rerender()
    await waitFor(() =>
      expect(document.body.textContent).not.toMatch(/Protected body/),
    )
  })

  it('a 503 stays UNKNOWN and is not shown as an empty or refused offer', async () => {
    api.getHandoffDetail.mockRejectedValue(
      new ApiError(503, 'evidence_unavailable', 'no evidence', undefined, {
        code: 'evidence_unavailable',
      }),
    )
    mount()
    await waitFor(() =>
      expect(
        document.querySelector('[data-failure-kind="unavailable"]'),
      ).not.toBeNull(),
    )
    expect(
      screen.queryByRole('button', { name: 'Accept responsibility' }),
    ).toBeNull()
  })

  it('a 404 keeps its generic concealment, distinct from a refusal and from unknown', async () => {
    api.getHandoffDetail.mockRejectedValue(new ApiError(404, 'not_found', 'no'))
    mount()
    await waitFor(() =>
      expect(
        document.querySelector('[data-failure-kind="not_found"]'),
      ).not.toBeNull(),
    )
  })

  it('a dropped response is AMBIGUOUS on a read too, and never an empty result', async () => {
    api.getHandoffDetail.mockRejectedValue(new NetworkError('dropped'))
    mount()
    await waitFor(() =>
      expect(
        document.querySelector('[data-failure-kind="ambiguous"]'),
      ).not.toBeNull(),
    )
  })
})

describe('the response controls exist only for a CURRENT offer', () => {
  it('hands the response the HANDOFF etag — not the carrier delivery version', async () => {
    api.getHandoffDetail.mockResolvedValue(handoffDetailOf())
    const onRespond = vi.fn()
    const user = userEvent.setup()
    mount({ onRespond })
    await screen.findByText('Deploy freeze needs an owner')
    await user.click(
      screen.getByRole('button', { name: 'Accept responsibility' }),
    )
    expect(onRespond).toHaveBeenCalledWith('accept', {
      handoffId: HANDOFF_ID,
      etag: HANDOFF_ETAG,
      workItemId: WORK_ITEM_ID,
      recipient: { kind: 'user', ref: '0192f2c0-eeee-7000-8000-00000000000b' },
      deliveryId: HANDOFF_DELIVERY_ID,
    })
    // The carrier's version is 1; an ETag rebuilt from it would be `"v1"`.
    expect(onRespond.mock.calls[0][1].etag).not.toBe('"v1"')
  })

  it.each([
    ['stale', 'This offer is no longer current'],
    ['terminal', 'This offer is already resolved'],
  ] as const)(
    'a %s offer shows its actual state and a refresh, and has NO response control',
    async (offerContext, expected) => {
      api.getHandoffDetail.mockResolvedValue(
        handoffDetailOf({
          offerContext,
          state: offerContext === 'terminal' ? 'withdrawn' : 'offered',
          terminalReason:
            offerContext === 'terminal'
              ? { code: 'withdrawn_by_sender', text: 'Sender took it back.' }
              : undefined,
        }),
      )
      mount()
      expect(await screen.findByText(expected)).toBeVisible()
      expect(
        screen.queryByRole('button', { name: 'Accept responsibility' }),
      ).toBeNull()
      expect(
        screen.queryByRole('button', { name: 'Reject handoff' }),
      ).toBeNull()
      // The refresh path is always available.
      expect(screen.getByRole('button', { name: 'Re-read' })).toBeEnabled()
      if (offerContext === 'terminal') {
        const reason = document.querySelector(
          '[data-slot="handoff-terminal-reason"]',
        ) as HTMLElement
        expect(within(reason).getByText('withdrawn_by_sender')).toBeVisible()
        expect(within(reason).getByText('Sender took it back.')).toBeVisible()
      }
    },
  )

  it('an elapsed deadline on a CURRENT offer keeps the response control and is not called an expiry', async () => {
    api.getHandoffDetail.mockResolvedValue(
      handoffDetailOf({ deadlineElapsed: true, offerContext: 'current' }),
    )
    mount()
    await screen.findByText('Deploy freeze needs an owner')
    // The engine says the offer is answerable; the window merely elapsed.
    expect(
      document.querySelector('[data-slot="handoff-deadline-elapsed"]'),
    ).not.toBeNull()
    expect(
      screen.getByRole('button', { name: 'Accept responsibility' }),
    ).toBeEnabled()
    expect(screen.queryByText('Expired')).toBeNull()
  })

  it('without the response permission it says so and offers no control', async () => {
    api.getHandoffDetail.mockResolvedValue(handoffDetailOf())
    mount({ canRespond: false })
    expect(
      await screen.findByText(/requires sessions:handoff-response:write/i),
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Accept responsibility' }),
    ).toBeNull()
  })

  it('renders supplied text safely and never turns an artifact reference into a link', async () => {
    api.getHandoffDetail.mockResolvedValue(
      handoffDetailOf({
        summary: '<img src=x onerror=alert(1)> & <b>bold</b>',
        artifacts: [
          { kind: 'url', ref: 'https://example.invalid/x', hash: 'sha256:aa' },
        ],
      }),
    )
    mount()
    expect(
      await screen.findByText('<img src=x onerror=alert(1)> & <b>bold</b>'),
    ).toBeVisible()
    // The markup arrived as TEXT: no element was created from it. Queried on
    // `document.body` because the sheet is portalled out of the render container.
    expect(document.body.querySelector('img')).toBeNull()
    expect(document.body.querySelector('b')).toBeNull()
    // …and the reference is a label, not an anchor.
    const ref = document.querySelector(
      '[data-slot="handoff-artifact-ref"]',
    ) as HTMLElement
    expect(within(ref).getByText('https://example.invalid/x')).toBeVisible()
    expect(ref.querySelector('a')).toBeNull()
  })
})

describe('keyboard, focus and naming on the ACTUAL composed sheet', () => {
  it('has a visible accessible name, is reachable by keyboard, and Escape closes it', async () => {
    api.getHandoffDetail.mockResolvedValue(handoffDetailOf())
    const closes: boolean[] = []
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffSheet
        open
        onOpenChange={(o) => closes.push(o)}
        deliveryId={HANDOFF_DELIVERY_ID}
        scope={scopeOf()}
        canDeliveryRead
        canRespond
        onRespond={() => {}}
      />
    ))
    const sheet = await screen.findByRole('dialog', { name: 'Handoff offer' })
    // The name is a VISIBLE title, not an aria-label nobody can see.
    expect(within(sheet).getByText('Handoff offer')).toBeVisible()
    await screen.findByText('Deploy freeze needs an owner')

    // Tab reaches the response controls without a pointer.
    await user.tab()
    const reachable: string[] = []
    for (let i = 0; i < 12; i++) {
      const active = document.activeElement as HTMLElement | null
      if (active && active.tagName === 'BUTTON' && active.textContent)
        reachable.push(active.textContent.trim())
      await user.tab()
    }
    expect(reachable.join(' | ')).toMatch(/Accept responsibility/)
    expect(reachable.join(' | ')).toMatch(/Reject handoff/)

    await user.keyboard('{Escape}')
    expect(closes).toContain(false)
  })

  // The regression this guards: Radix's own mount focus takes the first TABBABLE
  // control, which is the footer's Re-read button. The body loads after mount, so
  // that button moves down as the detail renders and, on a narrow viewport, ends up
  // below the fold while still holding focus. The title cannot move that way.
  it('focuses the title while the body is still reading, and the read landing does not move it', async () => {
    const slow = deferred<HandoffDetail>()
    api.getHandoffDetail.mockReturnValueOnce(slow.promise)
    mount()
    const sheet = await screen.findByRole('dialog', { name: 'Handoff offer' })
    const title = within(sheet).getByText('Handoff offer')
    await waitFor(() => expect(document.activeElement).toBe(title))
    // The body really is still a skeleton at this point.
    expect(within(sheet).getByRole('status')).toHaveAttribute(
      'aria-busy',
      'true',
    )
    slow.resolve(handoffDetailOf())
    expect(
      await screen.findByText('Deploy freeze needs an owner'),
    ).toBeVisible()
    // Still the title, and never the footer action Radix would have chosen.
    expect(document.activeElement).toBe(title)
    expect(document.activeElement).not.toBe(
      within(sheet).getByRole('button', { name: 'Re-read' }),
    )
    // The title is reachable by script but stays out of the Tab order.
    expect(title).toHaveAttribute('tabindex', '-1')
  })

  it('returns focus to the control that opened it, rather than to the document body', async () => {
    api.getHandoffDetail.mockResolvedValue(handoffDetailOf())
    const user = userEvent.setup()
    // A real opener outside the overlay, exactly like an inbox row.
    const Harness = () => {
      const [open, setOpen] = openHolder
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>
            open the offer
          </button>
          <HandoffSheet
            open={open}
            onOpenChange={setOpen}
            deliveryId={HANDOFF_DELIVERY_ID}
            scope={scopeOf()}
            canDeliveryRead
            canRespond
            onRespond={() => {}}
          />
        </>
      )
    }
    const { rerender } = renderWithQuery(() => <Harness />)
    const opener = screen.getByRole('button', { name: 'open the offer' })
    opener.focus()
    expect(document.activeElement).toBe(opener)

    openHolder[0] = true
    rerender()
    await screen.findByRole('dialog')
    await screen.findByText('Deploy freeze needs an owner')

    await user.keyboard('{Escape}')
    openHolder[0] = false
    rerender()
    await waitFor(() =>
      expect(screen.queryByText('Deploy freeze needs an owner')).toBeNull(),
    )
    // The measured I2 defect was `document.activeElement === <body>` after Escape.
    await waitFor(() => expect(document.activeElement).toBe(opener))
  })
})

/** A tiny mutable open/setOpen pair, so a re-render can drive the overlay without a
 *  state hook this file would otherwise have to host. */
const openHolder: [boolean, (o: boolean) => void] = [
  false,
  (o: boolean) => {
    openHolder[0] = o
  },
]
