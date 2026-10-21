// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useClientSettings } from './preferences'
import { SessionNotificationSetting } from './session-notification-setting'

const requestPermission = vi.fn()
beforeEach(() => {
  useClientSettings.getState().restore()
  requestPermission.mockReset().mockResolvedValue('granted')
  vi.stubGlobal('Notification', { permission: 'default', requestPermission })
})
afterEach(() => vi.unstubAllGlobals())

it('is off by default and asks permission only on keyboard activation of Notify me', async () => {
  const user = userEvent.setup()
  render(<SessionNotificationSetting owner="person-tenant" />)
  const button = screen.getByRole('button', { name: 'Notify me' })
  expect(button).toHaveAttribute('aria-pressed', 'false')
  expect(button).toHaveAccessibleDescription(/background/)
  expect(requestPermission).not.toHaveBeenCalled()
  await user.tab()
  expect(button).toHaveFocus()
  await user.keyboard('{Enter}')
  expect(requestPermission).toHaveBeenCalledTimes(1)
  expect(button).toHaveAttribute('aria-pressed', 'true')
  await user.keyboard(' ')
  expect(button).toHaveAttribute('aria-pressed', 'false')
  expect(requestPermission).toHaveBeenCalledTimes(1)
})

it('shows one status line when permission is denied', async () => {
  requestPermission.mockResolvedValue('denied')
  render(<SessionNotificationSetting owner="person-tenant" />)
  await userEvent.click(screen.getByRole('button', { name: 'Notify me' }))
  expect(screen.getByRole('status')).toHaveTextContent(
    'Notifications are blocked in this browser.',
  )
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Notify me' })).toHaveAttribute(
    'aria-pressed',
    'false',
  )
})

it('does not enable the next person when an old permission request resolves', async () => {
  let finish!: (permission: NotificationPermission) => void
  requestPermission.mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve
      }),
  )
  const view = render(<SessionNotificationSetting owner="old-person" />)
  await userEvent.click(screen.getByRole('button', { name: 'Notify me' }))
  view.rerender(<SessionNotificationSetting owner="new-person" />)
  await act(async () => finish('granted'))
  expect(useClientSettings.getState().sessionNotificationsFor).toBeNull()
})

it('handles a failed request with a calm status and keeps notifications off', async () => {
  requestPermission.mockRejectedValue(new Error('Permission unavailable'))
  render(<SessionNotificationSetting owner="person-tenant" />)
  await userEvent.click(screen.getByRole('button', { name: 'Notify me' }))
  expect(screen.getByRole('status')).toHaveTextContent(
    'Browser notifications are unavailable here.',
  )
  expect(useClientSettings.getState().sessionNotificationsFor).toBeNull()
})

it('stays disabled with one status line when permission is already blocked', () => {
  vi.stubGlobal('Notification', { permission: 'denied', requestPermission })
  render(<SessionNotificationSetting owner="person-tenant" />)
  expect(screen.getByRole('button', { name: 'Notify me' })).toBeDisabled()
  expect(screen.getByRole('status')).toHaveTextContent(
    'Notifications are blocked in this browser.',
  )
})

it('stays disabled with one status line when the browser has no notifications', () => {
  vi.stubGlobal('Notification', undefined)
  render(<SessionNotificationSetting owner="person-tenant" />)
  expect(screen.getByRole('button', { name: 'Notify me' })).toBeDisabled()
  expect(screen.getByRole('status')).toHaveTextContent(
    'Browser notifications are unavailable here.',
  )
})

it('turns on without asking again when permission was already granted', async () => {
  vi.stubGlobal('Notification', { permission: 'granted', requestPermission })
  render(<SessionNotificationSetting owner="person-tenant" />)
  await userEvent.click(screen.getByRole('button', { name: 'Notify me' }))
  expect(requestPermission).not.toHaveBeenCalled()
  expect(screen.getByRole('button', { name: 'Notify me' })).toHaveAttribute(
    'aria-pressed',
    'true',
  )
})
