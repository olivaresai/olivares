// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from '@/lib/i18n'
import { compareHref } from '@/features/source-diff/source-diff-model'
import { SourceCompareControl } from '@/features/source-diff/source-diff-view'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    listConnectors: vi.fn(),
    listSources: vi.fn(),
    putConnector: vi.fn(),
    testConnector: vi.fn(),
    deleteConnector: vi.fn(),
    reloadRuntime: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { user_id: 'u1', aal: 3 } as {
      user_id?: string
      aal?: number
    } | null,
    can: (_permission: string) => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
  StepUpPanel: () => null,
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  Toaster: () => null,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { useSessionStore } from '@/stores/session'
import { ConnectorsTab } from './connectors-tab'

function wrap(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.isSuperadmin = true
  useSessionStore.setState({ credentialGeneration: 0 })
  i18n.addResourceBundle(
    'en',
    'source-diff',
    {
      compare: 'Compare',
      forbidden:
        'This comparison needs system:admin. Only a superadmin holds it. An owner or an admin cannot grant it.',
    },
    true,
    true,
  )
  api.listConnectors.mockResolvedValue({ connectors: [] })
  api.listSources.mockResolvedValue({
    sources: [
      {
        name: 'gh-main',
        kind: 'github',
        tenant: '',
        enabled: true,
        status: 'running',
      },
      {
        name: 'gl-main',
        kind: 'gitlab',
        tenant: '',
        enabled: true,
        status: 'running',
      },
      {
        name: 'vault-prod',
        kind: 'vault',
        tenant: '',
        enabled: true,
        status: 'running',
      },
    ],
  })
})

describe('compare on a source row', () => {
  it('builds a shareable address from the roster name and kind', () => {
    expect(compareHref({ name: 'gh-main', kind: 'github' })).toBe(
      '/console/sources/diff?source=gh-main&host=github',
    )
    expect(compareHref({ name: 'gl-main', kind: 'GitLab' })).toBe(
      '/console/sources/diff?source=gl-main&host=gitlab',
    )
    expect(compareHref({ name: 'vault-prod', kind: 'vault' })).toBeNull()
  })

  it('hides Compare for any other kind', () => {
    wrap(
      <SourceCompareControl
        source={{ name: 'vault-prod', kind: 'vault' }}
        allowed
      />,
    )
    expect(
      screen.queryByRole('link', { name: 'Compare' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Compare' }),
    ).not.toBeInTheDocument()
  })

  it('disables Compare and names system:admin when that permission is absent', () => {
    wrap(
      <SourceCompareControl
        source={{ name: 'gh-main', kind: 'github' }}
        allowed={false}
      />,
    )
    expect(screen.getByRole('button', { name: 'Compare' })).toBeDisabled()
    expect(screen.getByText(/Only a superadmin holds it/)).toHaveTextContent(
      'cannot grant',
    )
    expect(screen.queryByText(/ask an administrator/)).not.toBeInTheDocument()
  })

  it('offers Compare on GitHub and GitLab rows and fills source from the row', async () => {
    wrap(<ConnectorsTab />)
    const links = await screen.findAllByRole('link', { name: 'Compare' })
    expect(links).toHaveLength(2)
    expect(links[0]).toHaveAttribute(
      'href',
      '/console/sources/diff?source=gh-main&host=github',
    )
    expect(links[1]).toHaveAttribute(
      'href',
      '/console/sources/diff?source=gl-main&host=gitlab',
    )
  })
})
