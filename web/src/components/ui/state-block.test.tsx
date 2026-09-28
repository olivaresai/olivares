// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE EIGHT STATES EVERY LIST AND ACTION SHOWS, one case each, with the content the design's
// state set lists for it: Loading, Empty, Filtered, Error, Stale, Disabled with reason,
// No access and Outcome not known.
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { Button } from './button'
import { StateBlock } from './state-block'

const block = (container: HTMLElement, state: string) => {
  const el = container.querySelector('[data-slot="state-block"]')
  expect(el).not.toBeNull()
  expect(el).toHaveAttribute('data-state', state)
  return el as HTMLElement
}

describe('StateBlock', () => {
  it('Loading: busy, says what is loading, and never shows a count', () => {
    const { container } = render(
      <StateBlock state="loading" label="Loading sessions…" />,
    )
    const el = block(container, 'loading')
    expect(el).toHaveAttribute('aria-busy', 'true')
    expect(el).toHaveAttribute('role', 'status')
    expect(el).toHaveTextContent('Loading sessions…')
    // Never "0 sessions" while loading: no number in what the operator reads.
    expect(el.textContent).not.toMatch(/\d/)
    // The skeleton rows are shape only.
    const rows = el.querySelectorAll('[data-slot="state-block-skeleton"]')
    expect(rows.length).toBeGreaterThanOrEqual(3)
    rows.forEach((row) => expect(row).toHaveAttribute('aria-hidden', 'true'))
  })

  it('Loading: the default label is the localized one', () => {
    const { container } = render(<StateBlock state="loading" />)
    expect(block(container, 'loading')).toHaveTextContent('Loading…')
  })

  it('Empty: complete with zero rows, one next action and the CLI line', async () => {
    const start = vi.fn()
    const { container } = render(
      <StateBlock
        state="empty"
        title="No sessions in telescopes yet"
        description="Start one here, or from a terminal."
        action={<Button onClick={start}>New session</Button>}
        command="olivares session start"
      />,
    )
    const el = block(container, 'empty')
    expect(el).toHaveAttribute('role', 'status')
    expect(within(el).getByText('No sessions in telescopes yet')).toBeVisible()
    const code = el.querySelector('code')!
    expect(code.textContent).toBe('olivares session start')
    await userEvent
      .setup()
      .click(within(el).getByRole('button', { name: 'New session' }))
    expect(start).toHaveBeenCalledTimes(1)
  })

  it('Filtered: the filters, the hidden count and Clear filters', async () => {
    const clear = vi.fn()
    const { container } = render(
      <StateBlock
        state="filtered"
        title="No session matches these filters"
        filters="State: Failed · AI tool: Grok Build"
        hiddenCount={12}
        onClear={clear}
      />,
    )
    const el = block(container, 'filtered')
    expect(el).toHaveTextContent('State: Failed · AI tool: Grok Build')
    expect(el).toHaveTextContent('12 items are hidden by the filters.')
    await userEvent
      .setup()
      .click(within(el).getByRole('button', { name: 'Clear filters' }))
    expect(clear).toHaveBeenCalledTimes(1)
  })

  it('Error: says the list could not be read, with the request, time, request id, Try again and Open diagnostics', async () => {
    const retry = vi.fn()
    const diagnose = vi.fn()
    const { container } = render(
      <StateBlock
        state="error"
        request="GET /v1/sessions · timeout after 10 s"
        requestId="01J8ZK4Q7M"
        time="18:06:41 UTC"
        onRetry={retry}
        onOpenDiagnostics={diagnose}
      />,
    )
    const el = block(container, 'error')
    expect(el).toHaveAttribute('role', 'alert')
    expect(el).toHaveTextContent('Could not reach the engine.')
    expect(el).toHaveTextContent('The list is not empty. It could not be read.')
    expect(el).toHaveTextContent('GET /v1/sessions · timeout after 10 s')
    expect(el).toHaveTextContent('Request 01J8ZK4Q7M · 18:06:41 UTC')
    const user = userEvent.setup()
    await user.click(within(el).getByRole('button', { name: 'Try again' }))
    await user.click(
      within(el).getByRole('button', { name: 'Open diagnostics' }),
    )
    expect(retry).toHaveBeenCalledTimes(1)
    expect(diagnose).toHaveBeenCalledTimes(1)
  })

  it('Error: draws no empty request line when the failure carried no id', () => {
    const { container } = render(
      <StateBlock state="error" onRetry={() => {}} />,
    )
    const el = block(container, 'error')
    expect(el).not.toHaveTextContent('Request')
    expect(
      el.querySelector('code, [data-slot="state-block-request"]'),
    ).toBeNull()
  })

  it('Stale: the last good time, the failed refresh time, Retry, and the rows kept', async () => {
    const retry = vi.fn()
    const { container } = render(
      <StateBlock
        state="stale"
        lastGood="18:04 UTC"
        failedAt="18:06"
        onRetry={retry}
      >
        <ul>
          <li>Move stream ingest to async</li>
          <li>Publish camera calibration</li>
        </ul>
      </StateBlock>,
    )
    const el = block(container, 'stale')
    expect(el).toHaveTextContent(
      'Showing results from 18:04 UTC. The refresh at 18:06 failed.',
    )
    const rows = el.querySelector('[data-slot="state-block-stale-rows"]')!
    expect(rows).toHaveTextContent('Move stream ingest to async')
    // Full contrast: the kept rows are not faded.
    expect(rows.getAttribute('class')).not.toMatch(/opacity-/)
    await userEvent
      .setup()
      .click(within(el).getByRole('button', { name: 'Retry' }))
    expect(retry).toHaveBeenCalledTimes(1)
  })

  it('Disabled with reason: the control stays, cannot act and points at its visible reason', async () => {
    const deploy = vi.fn()
    const { container } = render(
      <StateBlock
        state="disabled"
        reason="The plan changed after it was approved. Approve the new plan first."
      >
        <Button onClick={deploy}>Deploy to production</Button>
      </StateBlock>,
    )
    block(container, 'disabled')
    const button = screen.getByRole('button', { name: 'Deploy to production' })
    expect(button).toHaveAttribute('aria-disabled', 'true')
    expect(button).toHaveAccessibleDescription(
      'The plan changed after it was approved. Approve the new plan first.',
    )
    await userEvent.setup().click(button)
    expect(deploy).not.toHaveBeenCalled()
  })

  it('No access: refused, calm, and nothing about the object', () => {
    const { container } = render(
      <StateBlock
        state="no-access"
        action={<Button>Ask the owner for access</Button>}
      />,
    )
    const el = block(container, 'no-access')
    expect(el).toHaveAttribute('role', 'status')
    expect(el).toHaveTextContent('You cannot open this')
    expect(el).toHaveTextContent(
      'Your role does not include it. Nothing else is shown about it.',
    )
    expect(
      within(el).getByRole('button', { name: 'Ask the owner for access' }),
    ).toBeVisible()
  })

  it('Outcome not known: checks the same request and keeps the retry disabled with its reason until the engine confirms nothing started', async () => {
    const again = vi.fn()
    const { container, rerender } = render(
      <StateBlock
        state="unknown-outcome"
        requestId="01J8ZK9V2C"
        elapsed="8 s"
        confirmedNothingStarted={false}
        retryLabel="Start again"
        onRetry={again}
      />,
    )
    const el = block(container, 'unknown-outcome')
    expect(el).toHaveTextContent(
      'The request was sent, and the answer was lost.',
    )
    expect(el).toHaveTextContent('Request 01J8ZK9V2C · checking for 8 s')
    const user = userEvent.setup()
    const retry = within(el).getByRole('button', { name: 'Start again' })
    expect(retry).toHaveAttribute('aria-disabled', 'true')
    expect(retry).toHaveAccessibleDescription(
      'Available when the engine confirms that nothing started.',
    )
    await user.click(retry)
    expect(again).not.toHaveBeenCalled()

    rerender(
      <StateBlock
        state="unknown-outcome"
        requestId="01J8ZK9V2C"
        elapsed="9 s"
        confirmedNothingStarted
        retryLabel="Start again"
        onRetry={again}
      />,
    )
    const ready = screen.getByRole('button', { name: 'Start again' })
    expect(ready).not.toHaveAttribute('aria-disabled')
    await user.click(ready)
    expect(again).toHaveBeenCalledTimes(1)
  })
})
