// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Add profile continues to the tool's own sign-in in place: the person already chose to sign
// in, so the login starts without a second button. The device code is a large box with a
// Copy button, and the paste field shows when the engine says the tool waits for a code
// (state needs_code), whatever the tool.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: () => true, isSuperadmin: true }),
}))

import { ApiError, NetworkError } from '@/lib/api/errors'
import { useTenantStore } from '@/stores/tenant'
import { signInApi, type SignInFlow } from './api'
import { SignIn } from './first-hour'

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const device: SignInFlow = {
  id: 'f1',
  driver: 'codex',
  account_ref: 'ppf_b',
  state: 'waiting',
  url: 'https://auth.example.test/device',
  user_code: 'ABCD-1234',
}

beforeEach(() => {
  vi.restoreAllMocks()
  useTenantStore.setState({ activeTenant: 't1' })
  vi.spyOn(signInApi, 'get').mockResolvedValue(device)
})

describe('SignIn that starts on its own', () => {
  it('starts the login once, with no click, and shows the link and the code', async () => {
    const start = vi.spyOn(signInApi, 'start').mockResolvedValue(device)
    wrap(<SignIn driver="codex" accountRef="ppf_b" tenantId="t1" autoStart />)
    expect(
      await screen.findByRole('link', { name: 'Open the sign-in page' }),
    ).toHaveAttribute('href', device.url)
    expect(screen.getByTestId('device-code')).toHaveTextContent('ABCD-1234')
    expect(start).toHaveBeenCalledTimes(1)
    expect(start.mock.calls[0].slice(0, 3)).toEqual(['codex', 't1', 'ppf_b'])
  })

  it('waits for a click when it was not asked to start', async () => {
    const start = vi.spyOn(signInApi, 'start').mockResolvedValue(device)
    wrap(<SignIn driver="codex" accountRef="ppf_b" tenantId="t1" />)
    expect(
      await screen.findByRole('button', { name: 'Sign in with ChatGPT' }),
    ).toBeInTheDocument()
    expect(start).not.toHaveBeenCalled()
  })

  it('copies the device code from its box', async () => {
    vi.spyOn(signInApi, 'start').mockResolvedValue(device)
    const writeText = vi.fn().mockResolvedValue(undefined)
    const user = userEvent.setup()
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText },
      configurable: true,
    })
    wrap(<SignIn driver="codex" accountRef="ppf_b" tenantId="t1" autoStart />)
    await screen.findByTestId('device-code')
    await user.click(screen.getByRole('button', { name: 'Copy' }))
    expect(writeText).toHaveBeenCalledWith('ABCD-1234')
    // Said aloud too: a status region, not only a changed label.
    expect(await screen.findByRole('status')).toHaveTextContent('Copied')
  })

  it('asks for the pasted code of any tool whose flow says it waits for one', async () => {
    const needsCode: SignInFlow = {
      id: 'f2',
      driver: 'gemini-cli' as SignInFlow['driver'],
      account_ref: 'ppf_g',
      state: 'needs_code',
      url: 'https://auth.example.test/authorize',
    }
    vi.spyOn(signInApi, 'start').mockResolvedValue(needsCode)
    vi.mocked(signInApi.get).mockResolvedValue(needsCode)
    const code = vi.spyOn(signInApi, 'code').mockResolvedValue({
      ...needsCode,
      state: 'checking',
    })
    const user = userEvent.setup()
    wrap(
      <SignIn
        driver={'gemini-cli' as SignInFlow['driver']}
        accountRef="ppf_g"
        tenantId="t1"
        autoStart
      />,
    )
    const field = await screen.findByRole('textbox', {
      name: 'Code from the sign-in page',
    })
    // The code is the next thing to do: the field takes the focus when it appears.
    await waitFor(() => expect(field).toHaveFocus())
    await user.type(field, 'abc123')
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(code).toHaveBeenCalled())
    expect(code.mock.calls[0].slice(0, 2)).toEqual(['f2', 'abc123'])
    expect(screen.queryByTestId('device-code')).toBeNull()
  })

  it('says it is starting until the tool shows its page', async () => {
    const starting: SignInFlow = {
      id: 'f3',
      driver: 'codex',
      account_ref: 'ppf_b',
      state: 'starting',
    }
    vi.spyOn(signInApi, 'start').mockResolvedValue(starting)
    vi.mocked(signInApi.get).mockResolvedValue(starting)
    wrap(<SignIn driver="codex" accountRef="ppf_b" tenantId="t1" autoStart />)
    expect(await screen.findByText('Starting the sign-in…')).toBeInTheDocument()
  })

  it('does not start a second login over one that a reload resumed', async () => {
    vi.spyOn(signInApi, 'status').mockResolvedValue({
      driver: 'codex',
      installed: true,
      signed_in: false,
      pending: { ...device, account_ref: undefined },
    })
    const start = vi.spyOn(signInApi, 'start').mockResolvedValue(device)
    wrap(<SignIn driver="codex" autoStart />)
    expect(await screen.findByTestId('device-code')).toHaveTextContent(
      'ABCD-1234',
    )
    expect(start).not.toHaveBeenCalled()
  })

  it.each([403, 0])(
    'says in one quiet line that it lost touch with the sign-in (%i), instead of waiting for ever',
    async (status) => {
      vi.spyOn(signInApi, 'start').mockResolvedValue(device)
      vi.mocked(signInApi.get).mockRejectedValue(
        status === 0
          ? new NetworkError('offline')
          : new ApiError(status, 'forbidden', 'No.'),
      )
      wrap(<SignIn driver="codex" accountRef="ppf_b" tenantId="t1" autoStart />)
      expect(
        await screen.findByText(
          'Lost touch with the server. The sign-in may still be running.',
        ),
      ).toBeInTheDocument()
    },
  )
})
