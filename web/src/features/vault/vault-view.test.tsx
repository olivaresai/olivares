// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    list: vi.fn(),
    put: vi.fn(),
    remove: vi.fn(),
    reveal: vi.fn(),
    code: vi.fn(),
    generate: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'viewer' as string | null,
    isSuperadmin: false,
    principal: { user_id: 'u1', aal: 1 } as { user_id: string; aal: number },
    can: (_p: string): boolean => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  vaultApi: api,
}))

import { VaultView } from './vault-view'

const stamp = '2026-10-02T10:00:00Z'
const row = (name: string, hint: string, description = '') => ({
  name,
  hint,
  description,
  created_at: stamp,
  updated_at: stamp,
})
const items = {
  sealer_available: true,
  secrets: [
    row('vault/u1/bank', 'login+totp', 'Main account'),
    row('vault/u1/safe', 'note'),
    row('vault/u1/github-2fa', 'totp'),
    row('mcp/github', 'a1b2c3d4e5f6'),
  ],
}
const login = JSON.stringify({
  type: 'login',
  username: 'ann',
  password: 'pw-bank-1',
  url: 'https://bank.example',
  totp: 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ',
})

function renderView() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <VaultView />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.activeTenant = 't1'
  authState.principal = { user_id: 'u1', aal: 1 }
  api.list.mockResolvedValue(items)
  api.reveal.mockResolvedValue({ name: 'vault/u1/bank', value: login })
  api.code.mockResolvedValue({
    code: '123456',
    seconds_left: 17,
    period: 30,
    digits: 6,
  })
  api.generate.mockResolvedValue({ password: 'Gen3rated-pw' })
  api.put.mockResolvedValue(row('vault/u1/forum', 'login'))
  api.remove.mockResolvedValue(undefined)
})

describe('VaultView', () => {
  it("lists the person's own items and finds one by typing", async () => {
    renderView()
    await screen.findByText('bank')
    expect(screen.getByText('safe')).toBeInTheDocument()
    expect(screen.getByText('github-2fa')).toBeInTheDocument()
    // The organization's own secrets are not the person's vault.
    expect(screen.queryByText('mcp/github')).not.toBeInTheDocument()
    expect(api.list).toHaveBeenCalledWith(
      expect.objectContaining({ tenant: 't1' }),
    )

    const user = userEvent.setup()
    await user.type(
      screen.getByRole('searchbox', { name: 'Find an item' }),
      'saf',
    )
    expect(screen.queryByText('bank')).not.toBeInTheDocument()
    expect(screen.getByText('safe')).toBeInTheDocument()
    await user.clear(screen.getByRole('searchbox'))
    await user.type(screen.getByRole('searchbox'), 'zzz')
    expect(await screen.findByText('No item matches “zzz”')).toBeInTheDocument()
  })

  it('copies a password in one click, through the audited reveal', async () => {
    renderView()
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Copy the password of bank' }),
    )
    await waitFor(async () =>
      expect(await navigator.clipboard.readText()).toBe('pw-bank-1'),
    )
    expect(api.reveal).toHaveBeenCalledWith(
      'vault/u1/bank',
      expect.objectContaining({ tenant: 't1' }),
    )
    expect(api.code).not.toHaveBeenCalled()
  })

  it('copies the current code from the engine, never computing it in the page', async () => {
    renderView()
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', {
        name: 'Copy the code of github-2fa',
      }),
    )
    await waitFor(async () =>
      expect(await navigator.clipboard.readText()).toBe('123456'),
    )
    expect(api.code).toHaveBeenCalledWith(
      'vault/u1/github-2fa',
      expect.objectContaining({ tenant: 't1' }),
    )
    expect(api.reveal).not.toHaveBeenCalled()
  })

  it('copies nothing when the reveal is refused', async () => {
    api.reveal.mockRejectedValue(new Error('refused'))
    renderView()
    const user = userEvent.setup()
    await navigator.clipboard.writeText('sentinel')
    await user.click(
      await screen.findByRole('button', { name: 'Copy the password of bank' }),
    )
    await waitFor(() => expect(api.reveal).toHaveBeenCalled())
    expect(await navigator.clipboard.readText()).toBe('sentinel')
  })

  it('opens an item locked, reveals it on demand, masks the password and shows the live code', async () => {
    renderView()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Open bank' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(/recorded in the audit ledger/),
    ).toBeVisible()
    expect(api.reveal).not.toHaveBeenCalled()

    await user.click(within(dialog).getByRole('button', { name: 'Reveal' }))
    expect(await within(dialog).findByText('ann')).toBeVisible()
    expect(within(dialog).queryByText('pw-bank-1')).not.toBeInTheDocument()
    // A masked value is announced as hidden, not read as an empty field.
    expect(within(dialog).getAllByText('Hidden')).not.toHaveLength(0)
    await user.click(
      within(dialog).getByRole('button', { name: 'Show Password' }),
    )
    expect(within(dialog).getByText('pw-bank-1')).toBeVisible()
    // A link is shown as one only for http(s).
    expect(
      within(dialog).getByRole('link', { name: 'https://bank.example' }),
    ).toHaveAttribute('href', 'https://bank.example/')

    expect(api.code).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: 'Show code' }))
    const shownCode = await within(dialog).findByText('123 456')
    expect(shownCode).toBeVisible()
    // Show code replaced its own button: focus is on the code, not lost to the page.
    expect(shownCode).toHaveFocus()
    await user.click(within(dialog).getByRole('button', { name: 'Copy Code' }))
    await waitFor(async () =>
      expect(await navigator.clipboard.readText()).toBe('123456'),
    )
  })

  it('adds an item with a generated password, and refuses a name already taken', async () => {
    renderView()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Add item' }))
    const dialog = await screen.findByRole('dialog')
    const save = within(dialog).getByRole('button', { name: 'Save' })

    await user.type(within(dialog).getByLabelText(/^Name/), 'bank')
    expect(
      await within(dialog).findByText('An item with this name already exists'),
    ).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText('Username'), 'ann')
    expect(save).toBeDisabled()

    await user.clear(within(dialog).getByLabelText(/^Name/))
    await user.type(within(dialog).getByLabelText(/^Name/), 'forum')
    await user.click(within(dialog).getByRole('button', { name: 'Generate' }))
    expect(
      await within(dialog).findByDisplayValue('Gen3rated-pw'),
    ).toBeVisible()
    expect(api.generate).toHaveBeenCalledWith(
      expect.objectContaining({ length: 20, symbols: true }),
      expect.objectContaining({ tenant: 't1' }),
    )

    await user.click(save)
    await waitFor(() => expect(api.put).toHaveBeenCalled())
    expect(api.put).toHaveBeenCalledWith(
      'vault/u1/forum',
      expect.objectContaining({
        type: 'login',
        username: 'ann',
        password: 'Gen3rated-pw',
      }),
      '',
      expect.objectContaining({ tenant: 't1' }),
    )
  })

  it('deletes an item after a confirmation that names it', async () => {
    renderView()
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Actions for safe' }),
    )
    await user.click(await screen.findByRole('menuitem', { name: 'Delete' }))
    const confirm = await screen.findByRole('dialog')
    expect(within(confirm).getByText('Delete safe?')).toBeInTheDocument()
    expect(api.remove).not.toHaveBeenCalled()
    await user.click(within(confirm).getByRole('button', { name: 'Delete' }))
    await waitFor(() =>
      expect(api.remove).toHaveBeenCalledWith(
        'vault/u1/safe',
        expect.objectContaining({ tenant: 't1' }),
      ),
    )
  })

  it('invites the first item in an empty vault, and says when the server cannot seal', async () => {
    api.list.mockResolvedValue({ secrets: [], sealer_available: false })
    renderView()
    expect(await screen.findByText('Your vault is empty')).toBeInTheDocument()
    expect(
      screen.getAllByRole('button', { name: 'Add item' }),
    ).not.toHaveLength(0)
    expect(screen.getByText(/cannot seal secrets yet/)).toBeInTheDocument()
    expect(screen.queryByRole('searchbox')).not.toBeInTheDocument()
  })

  it('edits an item in place: the kind is fixed and the other fields are kept', async () => {
    renderView()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Open bank' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Reveal' }))
    await user.click(
      await within(dialog).findByRole('button', { name: 'Edit' }),
    )

    expect(await within(dialog).findByText('Edit bank')).toBeVisible()
    expect(within(dialog).queryByRole('radio', { name: 'Note' })).toBeNull()
    expect(within(dialog).queryByLabelText(/^Name/)).toBeNull()
    const username = within(dialog).getByLabelText('Username')
    await user.clear(username)
    await user.type(username, 'ann2')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(api.put).toHaveBeenCalled())
    expect(api.put).toHaveBeenCalledWith(
      'vault/u1/bank',
      expect.objectContaining({
        type: 'login',
        username: 'ann2',
        password: 'pw-bank-1',
        totp: 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ',
      }),
      'Main account',
      expect.objectContaining({ tenant: 't1' }),
    )
  })

  it('shows a value that is not an item as text and does not offer to edit it', async () => {
    api.reveal.mockResolvedValue({
      name: 'vault/u1/bank',
      value: 'a-value-stored-before-items-had-a-shape',
    })
    renderView()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Open bank' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Reveal' }))
    await user.click(
      await within(dialog).findByRole('button', { name: 'Show Note' }),
    )
    expect(
      await within(dialog).findByText(
        'a-value-stored-before-items-had-a-shape',
      ),
    ).toBeVisible()
    expect(within(dialog).queryByRole('button', { name: 'Edit' })).toBeNull()
  })

  it('hides a revealed item again after a minute', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      renderView()
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
      await user.click(await screen.findByRole('button', { name: 'Open bank' }))
      const dialog = await screen.findByRole('dialog')
      await user.click(within(dialog).getByRole('button', { name: 'Reveal' }))
      expect(await within(dialog).findByText('ann')).toBeVisible()

      await act(async () => {
        await vi.advanceTimersByTimeAsync(60_000)
      })
      expect(within(dialog).queryByText('ann')).toBeNull()
      expect(
        within(dialog).getByRole('button', { name: 'Reveal' }),
      ).toBeVisible()
      // The fields that held focus are gone: focus goes to Reveal, not to the page.
      expect(
        within(dialog).getByRole('button', { name: 'Reveal' }),
      ).toHaveFocus()
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not replace an item that appeared since the list was read', async () => {
    api.list.mockResolvedValueOnce(items).mockResolvedValueOnce({
      ...items,
      secrets: [...items.secrets, row('vault/u1/forum', 'login')],
    })
    renderView()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Add item' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^Name/), 'forum')
    await user.type(within(dialog).getByLabelText('Username'), 'ann')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.list).toHaveBeenCalledTimes(2))
    expect(api.put).not.toHaveBeenCalled()
  })

  it('reads nothing without an active organization', async () => {
    authState.activeTenant = null
    renderView()
    await screen.findByText('Vault')
    expect(api.list).not.toHaveBeenCalled()
    // It says what to do, instead of calling an unreadable vault empty.
    expect(
      screen.getByText('Choose an organization to open your vault there.'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Your vault is empty')).toBeNull()
  })

  it('takes a copied password off the clipboard after 30 seconds', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      renderView()
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
      const write = vi.spyOn(navigator.clipboard, 'writeText')
      await user.click(
        await screen.findByRole('button', {
          name: 'Copy the password of bank',
        }),
      )
      await waitFor(async () =>
        expect(await navigator.clipboard.readText()).toBe('pw-bank-1'),
      )
      await act(async () => {
        await vi.advanceTimersByTimeAsync(29_000)
      })
      expect(write).not.toHaveBeenCalledWith('')
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_000)
      })
      expect(write).toHaveBeenLastCalledWith('')
    } finally {
      vi.useRealTimers()
    }
  })

  // The issue's journey: add, find, copy and delete are one step each.
  it('keeps a login in four steps: add, find, copy, delete', async () => {
    const stored = [...items.secrets]
    api.list.mockImplementation(async () => ({
      sealer_available: true,
      secrets: [...stored],
    }))
    api.put.mockImplementation(async (name: string) => {
      stored.push(row(name, 'login'))
      return row(name, 'login')
    })
    api.remove.mockImplementation(async (name: string) => {
      stored.splice(
        stored.findIndex((s) => s.name === name),
        1,
      )
    })
    renderView()
    const user = userEvent.setup()

    // 1. Add: one form, one Save.
    await user.click(await screen.findByRole('button', { name: 'Add item' }))
    const form = await screen.findByRole('dialog')
    await user.type(within(form).getByLabelText(/^Name/), 'forum')
    await user.type(within(form).getByLabelText('Username'), 'ann')
    const typed = within(form).getByLabelText('Password', { selector: 'input' })
    await user.type(typed, 'pw-forum-7')
    expect(typed).toHaveValue('pw-forum-7')
    await user.click(within(form).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        'vault/u1/forum',
        expect.objectContaining({ username: 'ann', password: 'pw-forum-7' }),
        '',
        expect.anything(),
      ),
    )
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    // 2. Find: typing narrows the list to it.
    await user.type(screen.getByRole('searchbox'), 'for')
    expect(await screen.findByText('forum')).toBeVisible()
    expect(screen.queryByText('bank')).toBeNull()

    // 3. Copy: one click on its row.
    api.reveal.mockResolvedValue({
      name: 'vault/u1/forum',
      value: JSON.stringify({
        type: 'login',
        username: 'ann',
        password: 'pw-forum-7',
      }),
    })
    await user.click(
      screen.getByRole('button', { name: 'Copy the password of forum' }),
    )
    await waitFor(async () =>
      expect(await navigator.clipboard.readText()).toBe('pw-forum-7'),
    )

    // 4. Delete: the row's menu, then the confirmation.
    await user.click(screen.getByRole('button', { name: 'Actions for forum' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Delete' }))
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: 'Delete',
      }),
    )
    await waitFor(() =>
      expect(api.remove).toHaveBeenCalledWith(
        'vault/u1/forum',
        expect.anything(),
      ),
    )
    expect(api.put).toHaveBeenCalledTimes(1)
    expect(api.reveal).toHaveBeenCalledTimes(1)
  })
})
