// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useClientSettings } from './preferences'
import { SessionNotificationSetting } from './session-notification-setting'
import { NotificationEventSettings } from './notification-event-settings'
import * as webPush from './web-push'

vi.mock('./web-push', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./web-push')>()),
  pushSupported: vi.fn(() => false),
  needsInstallForPush: vi.fn(() => false),
  enablePush: vi.fn(),
  disablePush: vi.fn(),
  currentPush: vi.fn(),
}))

const requestPermission = vi.fn()
beforeEach(() => {
  vi.mocked(webPush.pushSupported).mockReturnValue(false)
  vi.mocked(webPush.needsInstallForPush).mockReturnValue(false)
  vi.mocked(webPush.enablePush).mockReset().mockResolvedValue(undefined)
  vi.mocked(webPush.disablePush).mockReset().mockResolvedValue(undefined)
  vi.mocked(webPush.currentPush).mockReset().mockResolvedValue(true)
  useClientSettings.getState().restore()
  requestPermission.mockReset().mockResolvedValue('granted')
  vi.stubGlobal('Notification', { permission: 'default', requestPermission })
})

it('saves each event choice independently and offers its reset', async () => {
  render(<NotificationEventSettings />)
  await userEvent.click(screen.getByRole('switch', { name: 'Finished' }))
  expect(useClientSettings.getState().notificationEvents).toEqual({
    needsYou: true,
    finished: false,
    failed: true,
  })
  await userEvent.click(screen.getByRole('button', { name: 'Reset Finished' }))
  expect(useClientSettings.getState().notificationEvents.finished).toBe(true)
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

it('also registers this browser for push when it can, and says so', async () => {
  vi.mocked(webPush.pushSupported).mockReturnValue(true)
  render(<SessionNotificationSetting owner="person-tenant" />)
  await userEvent.click(screen.getByRole('button', { name: 'Notify me' }))
  expect(webPush.enablePush).toHaveBeenCalledTimes(1)
  // The words the closed tab will show are the person's own, in this console's language.
  expect(webPush.enablePush).toHaveBeenCalledWith({
    app: 'Olivares',
    approval: 'This session needs you.',
    finished: 'The session ended.',
    failed: 'The session stopped with an error.',
  })
  expect(await screen.findByText(/with the tab closed/)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Notify me' })).toHaveAttribute(
    'aria-pressed',
    'true',
  )
})

it('keeps the in-page notices when the engine cannot register the device, and says what is missing', async () => {
  vi.mocked(webPush.pushSupported).mockReturnValue(true)
  vi.mocked(webPush.enablePush).mockRejectedValue(new Error('503'))
  render(<SessionNotificationSetting owner="person-tenant" />)
  await userEvent.click(screen.getByRole('button', { name: 'Notify me' }))
  expect(
    await screen.findByText(/could not be registered for push/),
  ).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Notify me' })).toHaveAttribute(
    'aria-pressed',
    'true',
  )
  expect(useClientSettings.getState().sessionNotificationsFor).toBe(
    'person-tenant',
  )
})

it('turning it off unsubscribes this browser', async () => {
  vi.mocked(webPush.pushSupported).mockReturnValue(true)
  render(<SessionNotificationSetting owner="person-tenant" />)
  const button = screen.getByRole('button', { name: 'Notify me' })
  await userEvent.click(button)
  await screen.findByText(/with the tab closed/)
  await userEvent.click(button)
  expect(webPush.disablePush).toHaveBeenCalledTimes(1)
  expect(button).toHaveAttribute('aria-pressed', 'false')
  expect(screen.queryByText(/with the tab closed/)).not.toBeInTheDocument()
})

it('shows the registration it finds when the page opens with the switch already on', async () => {
  vi.mocked(webPush.pushSupported).mockReturnValue(true)
  useClientSettings.getState().setSessionNotificationsFor('person-tenant')
  vi.mocked(webPush.currentPush).mockResolvedValue(false)
  render(<SessionNotificationSetting owner="person-tenant" />)
  expect(
    await screen.findByText(/could not be registered for push/),
  ).toBeInTheDocument()
  expect(webPush.enablePush).not.toHaveBeenCalled()
})

it('on an iPhone that has not installed the app, says how to get notifications instead of "unavailable"', () => {
  vi.stubGlobal('Notification', undefined)
  vi.mocked(webPush.needsInstallForPush).mockReturnValue(true)
  render(<SessionNotificationSetting owner="person-tenant" />)
  expect(screen.getByRole('status')).toHaveTextContent(/Add to Home Screen/)
  expect(screen.getByRole('button', { name: 'Notify me' })).toBeDisabled()
})
