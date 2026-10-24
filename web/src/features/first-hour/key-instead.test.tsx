// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// "Use an API key instead" turns the sign-in sheet into the key field in place
// (#2551): the page and the dialog stay, the provider is the tool's own, the field
// takes the focus, and Cancel clears the typed key.
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { providersApi } from '@/features/providers/api'
import type { ProviderRecordDTO } from '@/features/providers/types'
import { renderIntel } from '@/test/intel'
import { useTenantStore } from '@/stores/tenant'
import { signInApi, type SignInFlow } from './api'
import { ProfileSignInDialog } from '@/features/agent-tools/profile-sign-in'
import { SignIn } from './first-hour'

const perms = vi.hoisted(() => ({ providerWrite: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    isSuperadmin: true,
    can: (p: string) => p !== 'sessions:provider:write' || perms.providerWrite,
    principal: { user_id: 'u1', aal: 1 },
    activeTenant: 'tnt-a',
  }),
}))

beforeEach(() => {
  vi.restoreAllMocks()
  perms.providerWrite = true
  useTenantStore.setState({ activeTenant: 'tnt-a' })
  vi.spyOn(signInApi, 'status').mockResolvedValue({
    driver: 'claude',
    installed: true,
    signed_in: false,
  })
})

afterEach(() => vi.restoreAllMocks())

describe('"Use an API key instead" in the sign-in sheet', () => {
  it("keeps the sheet and opens the key field on the tool's provider, without leaving the page", async () => {
    const start = vi.spyOn(signInApi, 'start').mockResolvedValue({
      id: 'f1',
      driver: 'claude',
      state: 'waiting',
    })
    const user = userEvent.setup()
    renderIntel(<SignIn driver="claude" />)
    // The control is a button in the sheet, not a link out of the page (#2551).
    expect(
      screen.queryByRole('link', { name: 'Use an API key instead' }),
    ).not.toBeInTheDocument()
    await user.click(
      await screen.findByRole('button', { name: 'Use an API key instead' }),
    )
    // The sheet itself turned into the key field, on the provider this tool runs on.
    expect(screen.getByText('Anthropic')).toBeInTheDocument()
    const key = screen.getByLabelText(/^API key/)
    await waitFor(() => expect(key).toHaveFocus())
    // No navigation: the login never started and the sign-in sheet is gone only
    // because it became the key field.
    expect(start).not.toHaveBeenCalled()
    expect(
      screen.queryByRole('button', { name: 'Sign in with Claude' }),
    ).not.toBeInTheDocument()
  })

  it('Cancel clears the typed key and brings the sign-in back', async () => {
    const user = userEvent.setup()
    renderIntel(<SignIn driver="claude" />)
    await user.click(
      await screen.findByRole('button', { name: 'Use an API key instead' }),
    )
    await user.type(screen.getByLabelText(/^API key/), 'sk-ant-typed')
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(
      screen.getByRole('button', { name: 'Sign in with Claude' }),
    ).toBeInTheDocument()
    // Focus returns to the control Cancel came from, not to the page body.
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Use an API key instead' }),
      ).toHaveFocus(),
    )
    // Opening the form again starts empty: the typed key did not survive the Cancel.
    await user.click(
      screen.getByRole('button', { name: 'Use an API key instead' }),
    )
    expect(screen.getByLabelText(/^API key/)).toHaveValue('')
  })

  it('registers the key for the preselected provider and clears the field', async () => {
    const create = vi.spyOn(providersApi, 'create').mockResolvedValue({
      provider_ref: 'prv-openai',
      kind: 'openai',
      display_name: 'OpenAI',
      state: 'active',
    } as ProviderRecordDTO)
    const user = userEvent.setup()
    renderIntel(<SignIn driver="codex" />)
    await user.click(
      await screen.findByRole('button', { name: 'Use an API key instead' }),
    )
    expect(screen.getByText('OpenAI')).toBeInTheDocument()
    await user.type(screen.getByLabelText(/^API key/), 'sk-openai-test')
    await user.click(screen.getByRole('button', { name: 'Add provider' }))
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith({
        kind: 'openai',
        display_name: 'OpenAI',
        api_key: 'sk-openai-test',
      }),
    )
    // The credential does not linger in the sheet after the write: the form is gone
    // and the sign-in is back, to re-read what the key made ready.
    await waitFor(() =>
      expect(screen.queryByLabelText(/^API key/)).not.toBeInTheDocument(),
    )
    expect(
      await screen.findByRole('button', { name: 'Sign in with ChatGPT' }),
    ).toBeInTheDocument()
  })

  it('a sign-in that completes under the key form cannot close the sheet', async () => {
    let resolveStart!: (flow: SignInFlow) => void
    const start = vi
      .spyOn(signInApi, 'start')
      .mockImplementation(
        () => new Promise<SignInFlow>((resolve) => (resolveStart = resolve)),
      )
    const onSignedIn = vi.fn()
    const user = userEvent.setup()
    renderIntel(<SignIn driver="claude" autoStart onSignedIn={onSignedIn} />)
    // The login the person chose starts on its own…
    await waitFor(() => expect(start).toHaveBeenCalled())
    // …and while it waits, the sheet is turned into the key form.
    await user.click(
      await screen.findByRole('button', { name: 'Use an API key instead' }),
    )
    expect(screen.getByText('Anthropic')).toBeInTheDocument()
    // The login then completes: the sheet must not close over the key being typed,
    // and the signed-in callback must not run under the form.
    await act(async () => {
      resolveStart({ id: 'f1', driver: 'claude', state: 'signed_in' })
    })
    expect(screen.getByText('Anthropic')).toBeInTheDocument()
    expect(screen.getByLabelText(/^API key/)).toBeInTheDocument()
    expect(onSignedIn).not.toHaveBeenCalled()
    // Leaving the form hands back to the sign-in, which reports the signed-in login.
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(onSignedIn).toHaveBeenCalled())
  })

  it('is not offered to a role that may not add a provider', async () => {
    // The Providers page gates the same form on sessions:provider:write.
    perms.providerWrite = false
    renderIntel(<SignIn driver="claude" />)
    expect(
      await screen.findByRole('button', { name: 'Sign in with Claude' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Use an API key instead' }),
    ).not.toBeInTheDocument()
  })

  it("a sign-in that completes under the profile sheet's key form cannot close it", async () => {
    let resolveStart!: (flow: SignInFlow) => void
    vi.spyOn(signInApi, 'start').mockImplementation(
      () => new Promise<SignInFlow>((resolve) => (resolveStart = resolve)),
    )
    const onOpenChange = vi.fn()
    const user = userEvent.setup()
    renderIntel(
      <ProfileSignInDialog
        driver="claude"
        name="claude"
        tenant="tnt-a"
        onOpenChange={onOpenChange}
      />,
    )
    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect(resolveStart).toBeDefined())
    // The sheet offers the key form once, under Other ways.
    await user.click(screen.getByText('Other ways'))
    const triggers = screen.getAllByRole('button', {
      name: 'Use an API key instead',
    })
    expect(triggers).toHaveLength(1)
    await user.click(triggers[0])
    await user.type(screen.getByLabelText(/^API key/), 'sk-typed')
    // The auto-started login completes under the form: the sheet stays.
    await act(async () => {
      resolveStart({ id: 'f1', driver: 'claude', state: 'signed_in' })
    })
    expect(dialog).toBeInTheDocument()
    expect(screen.getByLabelText(/^API key/)).toHaveValue('sk-typed')
    expect(onOpenChange).not.toHaveBeenCalled()
    // Leaving the form lets the signed-in login close the sheet.
    const form = screen.getByLabelText(/^API key/).closest('form')!
    await user.click(within(form).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  })

  it('holds the profile sheet open while the key is being stored', async () => {
    vi.spyOn(signInApi, 'start').mockReturnValue(new Promise(() => {}))
    let finish!: (record: ProviderRecordDTO) => void
    vi.spyOn(providersApi, 'create').mockImplementation(
      () => new Promise<ProviderRecordDTO>((resolve) => (finish = resolve)),
    )
    const onOpenChange = vi.fn()
    const user = userEvent.setup()
    renderIntel(
      <ProfileSignInDialog
        driver="claude"
        name="claude"
        tenant="tnt-a"
        onOpenChange={onOpenChange}
      />,
    )
    await screen.findByRole('dialog')
    await user.click(screen.getByText('Other ways'))
    await user.click(
      screen.getByRole('button', { name: 'Use an API key instead' }),
    )
    await user.type(screen.getByLabelText(/^API key/), 'sk-pending')
    await user.click(screen.getByRole('button', { name: 'Add provider' }))
    await waitFor(() => expect(finish).toBeDefined())
    // Escape while the create is in flight: the sheet stays.
    await user.keyboard('{Escape}')
    expect(onOpenChange).not.toHaveBeenCalled()
    // The stored key closes the sheet on its own.
    await act(async () => {
      finish({
        provider_ref: 'prv-a',
        kind: 'anthropic',
        display_name: 'Anthropic',
        state: 'active',
      } as ProviderRecordDTO)
    })
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  })

  // The sheet is a modal: it hides the toast from screen readers (#2561), so a
  // refused key is announced in the form, inside the sheet.
  it('announces a refused key inside the profile sheet', async () => {
    vi.spyOn(signInApi, 'start').mockReturnValue(new Promise(() => {}))
    vi.spyOn(providersApi, 'create').mockRejectedValue(
      new Error('the engine refused the key'),
    )
    const user = userEvent.setup()
    renderIntel(
      <ProfileSignInDialog
        driver="claude"
        name="claude"
        tenant="tnt-a"
        onOpenChange={() => {}}
      />,
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(screen.getByText('Other ways'))
    await user.click(
      screen.getByRole('button', { name: 'Use an API key instead' }),
    )
    await user.type(screen.getByLabelText(/^API key/), 'sk-refused')
    await user.click(screen.getByRole('button', { name: 'Add provider' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'the engine refused the key',
    )
  })
})
