// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A DISABLED CONTROL ALWAYS SAYS WHY. `DisabledReason` owns the visible reason node of the
// control it wraps: the control points at it with `aria-describedby`, and the component
// refuses to exist without the text.
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Button } from './button'
import { DisabledReason } from './disabled-reason'

const REASON =
  'The plan changed after it was approved. Approve the new plan first.'

describe('DisabledReason refuses to render without reason text', () => {
  const quiet = vi.spyOn(console, 'error').mockImplementation(() => undefined)
  afterEach(() => quiet.mockClear())

  it.each([
    ['an empty reason', ''],
    ['a blank reason', '   '],
  ])('throws on %s, disabled or not', (_, reason) => {
    for (const disabled of [true, false]) {
      expect(() =>
        render(
          <DisabledReason disabled={disabled} reason={reason}>
            <Button>Deploy to production</Button>
          </DisabledReason>,
        ),
      ).toThrow(/reason text/)
    }
  })
})

describe('DisabledReason while the control is disabled', () => {
  it('keeps the control focusable, marks it aria-disabled and points it at the visible reason', () => {
    render(
      <DisabledReason disabled reason={REASON}>
        <Button>Deploy to production</Button>
      </DisabledReason>,
    )
    const button = screen.getByRole('button', { name: 'Deploy to production' })
    expect(button).toHaveAttribute('aria-disabled', 'true')
    expect(button).not.toHaveAttribute('disabled')
    const id = button.getAttribute('aria-describedby')
    expect(id).toBeTruthy()
    const reason = document.getElementById(id!)
    expect(reason).not.toBeNull()
    expect(reason).toHaveTextContent(REASON)
    expect(reason).toBeVisible()
    expect(button).toHaveAccessibleDescription(REASON)
  })

  it('refuses the activation by pointer and by keyboard', async () => {
    const act = vi.fn()
    const user = userEvent.setup()
    render(
      <DisabledReason disabled reason={REASON}>
        <Button onClick={act}>Deploy to production</Button>
      </DisabledReason>,
    )
    const button = screen.getByRole('button', { name: 'Deploy to production' })
    await user.click(button)
    button.focus()
    expect(button).toHaveFocus()
    await user.keyboard('{Enter}')
    await user.keyboard(' ')
    fireEvent.click(button)
    expect(act).not.toHaveBeenCalled()
  })

  it('keeps a description the control already had, after it', () => {
    render(
      <>
        <p id="own-hint">Deploys the approved plan.</p>
        <DisabledReason disabled reason={REASON}>
          <Button aria-describedby="own-hint">Deploy to production</Button>
        </DisabledReason>
      </>,
    )
    const ids = screen
      .getByRole('button', { name: 'Deploy to production' })
      .getAttribute('aria-describedby')!
      .split(' ')
    expect(ids[0]).toBe('own-hint')
    expect(ids).toHaveLength(2)
  })

  it('shows the next step beside the reason', () => {
    render(
      <DisabledReason
        disabled
        reason="grok-b is signed out."
        action={<a href="#sign-in">Sign in</a>}
      >
        <Button>Start session</Button>
      </DisabledReason>,
    )
    const button = screen.getByRole('button', { name: 'Start session' })
    expect(button).toHaveAccessibleDescription(/grok-b is signed out\./)
    expect(screen.getByRole('link', { name: 'Sign in' })).toBeVisible()
  })

  it('offers the native form for a control whose tests read the disabled attribute', () => {
    render(
      <DisabledReason
        disabled
        mode="native"
        reason="That act needs sessions:message-send:write."
      >
        <Button>Offer handoff</Button>
      </DisabledReason>,
    )
    const button = screen.getByRole('button', { name: 'Offer handoff' })
    expect(button).toBeDisabled()
    expect(button).toHaveAccessibleDescription(
      'That act needs sessions:message-send:write.',
    )
  })
})

describe('DisabledReason while the control can act', () => {
  it('adds nothing: no reason node, no aria-disabled, the click goes through', async () => {
    const act = vi.fn()
    render(
      <DisabledReason disabled={false} reason={REASON}>
        <Button onClick={act}>Deploy to production</Button>
      </DisabledReason>,
    )
    const button = screen.getByRole('button', { name: 'Deploy to production' })
    expect(button).not.toHaveAttribute('aria-disabled')
    expect(button).not.toHaveAttribute('aria-describedby')
    expect(screen.queryByText(REASON)).toBeNull()
    await userEvent.setup().click(button)
    expect(act).toHaveBeenCalledTimes(1)
  })
})
