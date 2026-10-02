// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Root, 09b real use (WEB 040): a new user found no way from the console to the olivares
// CLI; the install line lived only in the administrator's setup wizard. The commands are
// the CLI's own syntax, checked by CLX on 09b.
import { describe, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import { renderIntel, screen, within } from '@/test/intel'

const auth = vi.hoisted(() => ({
  principal: {
    actor: 'user:editor-1',
    display_name: 'Second user',
    superadmin: false,
  },
  isSuperadmin: false,
  activeRole: 'editor',
  logout: vi.fn(),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...auth, activeTenant: 't1', can: () => false }),
}))
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => vi.fn() }))
const server = vi.hoisted(() => ({ data: {} as { tls_pin_sha256?: string } }))
vi.mock('@/lib/hooks/use-server-info', () => ({ useServerInfo: () => server }))

import { UserMenu } from './user-menu'
import { commandLineSteps, INSTALL_COMMAND } from './command-line-dialog'

const commands = () =>
  [
    ...screen
      .getByRole('dialog')
      .querySelectorAll('[data-slot="code-line"] code'),
  ].map((c) => c.textContent)

describe('Command line: how to get and use the olivares CLI, for every signed-in person', () => {
  it('an editor opens it from the Account menu: install, sign in to THIS engine, start', async () => {
    const user = userEvent.setup()
    renderIntel(<UserMenu />)
    await user.click(screen.getByRole('button', { name: 'Account' }))
    await user.click(screen.getByRole('menuitem', { name: 'Command line' }))
    const dialog = screen.getByRole('dialog', { name: 'Command line' })
    const origin = window.location.origin
    expect(commands()).toEqual([
      'curl -fsSL https://olivares.ai/olivares/install.sh | sh',
      `olivares login --server ${origin} --ca-cert tls.crt`,
      'olivares session start . "<first task>"',
    ])
    expect(
      within(dialog).getByText(
        'Already installed on this server. On another Linux computer, run:',
      ),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByText(/asks for your email and password/),
    ).toBeInTheDocument()
    // The engine's address is the one the browser is on, never a placeholder.
    expect(dialog.textContent).not.toContain('<this engine')
  })

  it('with the pin the engine publishes, sign-in trusts it by --pin-sha256 (CLX)', async () => {
    server.data = { tls_pin_sha256: 'sha256-pin-of-this-engine' }
    const user = userEvent.setup()
    renderIntel(<UserMenu />)
    await user.click(screen.getByRole('button', { name: 'Account' }))
    await user.click(screen.getByRole('menuitem', { name: 'Command line' }))
    expect(commands()[1]).toBe(
      `olivares login --server ${window.location.origin} --pin-sha256 sha256-pin-of-this-engine`,
    )
    expect(
      within(screen.getByRole('dialog')).getByText(/certificate fingerprint/),
    ).toBeInTheDocument()
    server.data = {}
  })

  it('never shows --email alone (it fails today) or a credential', () => {
    for (const pin of [undefined, 'pin']) {
      for (const step of commandLineSteps(
        'https://olv.example:8443',
        'first task',
        pin,
      )) {
        expect(step.command).not.toMatch(/--email|--password|--token/)
      }
    }
    expect(INSTALL_COMMAND).toBe(
      'curl -fsSL https://olivares.ai/olivares/install.sh | sh',
    )
  })

  it('fills the sign-in line with the address it is given', () => {
    const [, signIn] = commandLineSteps('https://olv.example:8443', 'x')
    expect(signIn.command).toBe(
      'olivares login --server https://olv.example:8443 --ca-cert tls.crt',
    )
  })
})
