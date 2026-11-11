// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Properties of the Cedar/OPA publish + activation lifecycle. Each test pins ONE
// named honesty property of the console — the ones a green backend suite cannot
// catch, because they are about what the screen CLAIMS, not about what the engine
// computed. The property is written above the test as a comment.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  pdpValidate: vi.fn(),
  pdpExplain: vi.fn(),
  pdpDryRun: vi.fn(),
  pdpVersions: vi.fn(),
  pdpGetVersion: vi.fn(),
  pdpActive: vi.fn(),
  pdpDisable: vi.fn(),
  pdpPublish: vi.fn(),
  pdpRollback: vi.fn(),
  pdpTestStatus: vi.fn(),
}))
vi.mock('@/features/claude-policy/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/claude-policy/api')>()
  return { ...actual, claudePolicyApi: api }
})

const ext = vi.hoisted(() => ({
  authoring: undefined as
    | { publish: ReturnType<typeof vi.fn>; rollback: ReturnType<typeof vi.fn> }
    | undefined,
}))
vi.mock('@/features/extensions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/extensions')>()
  return {
    ...actual,
    PANEL_EXTENSIONS: {
      ...actual.PANEL_EXTENSIONS,
      get cedarPolicyAuthoring() {
        return ext.authoring
      },
    },
  }
})

import { CedarOpaView } from '@/features/claude-policy/cedar-opa-view'
import '@/features/claude-policy/i18n'
import { ApiError } from '@/lib/api/errors'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const CEDAR_ACTIVE = {
  engine: 'cedar' as const,
  authored: {
    present: true,
    revision: 2,
    content: 'permit(principal, action, resource);',
    sha256: 'sha256:aaaa1111bbbb2222cccc',
  },
  managed: {
    present: true,
    revision: 5,
    sha256: 'sha256:dddd3333eeee4444ffff',
  },
  adopted: { present: false },
  union_sha256: 'sha256:9999888877776666',
}

const OPA_ACTIVE = {
  engine: 'opa' as const,
  authored: {
    present: true,
    revision: 1,
    content: 'package olivares.authz\n',
    sha256: 'sha256:1111222233334444',
  },
  managed: { present: false },
  adopted: { present: false },
}

const GATE_DETAIL =
  'stored result of the compile/validation gate run before this immutable revision was committed'

function gateOk(revision: number) {
  return {
    engine: 'cedar' as const,
    revision,
    available: true,
    passed: 1,
    failed: 0,
    total: 1,
    results: [
      { name: 'publish_compile_validate', passed: true, detail: GATE_DETAIL },
    ],
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.pdpActive.mockImplementation((engine: string) =>
    Promise.resolve(engine === 'opa' ? OPA_ACTIVE : CEDAR_ACTIVE),
  )
  api.pdpVersions.mockResolvedValue({ items: [], has_more: false })
  api.pdpTestStatus.mockResolvedValue(gateOk(2))
})

afterEach(() => {
  vi.restoreAllMocks()
})

/** Open the engine Select and pick OPA / Rego. */
async function selectOpa(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('combobox', { name: /^Engine$/i }))
  await user.click(await screen.findByRole('option', { name: /OPA/i }))
}

describe('Community stored policy management', () => {
  it('shows read-only Cedar without publish or activation controls', async () => {
    api.pdpVersions.mockResolvedValue({
      items: [{ surface: 'cedar', revision: 1, validated: true }],
      has_more: false,
    })
    wrap(<CedarOpaView active />)
    expect(
      await screen.findByText(
        'Editing custom policies, roles and grants requires Olivares AI Business.',
      ),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /^Publish/i }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /^Activate/i }),
    ).not.toBeInTheDocument()
    expect(
      await screen.findByRole('button', { name: 'Disable policy' }),
    ).toBeInTheDocument()
    expect(api.pdpPublish).not.toHaveBeenCalled()
    const editor = screen.getByRole('textbox', { name: /cedar/i })
    expect(editor).toHaveTextContent(CEDAR_ACTIVE.authored.content)
    expect(editor).toHaveAttribute('contenteditable', 'false')
    expect(
      await screen.findByText(
        /The enforced Cedar policy is the UNION of three surfaces/i,
      ),
    ).toBeInTheDocument()
    expect(screen.getByText(/r5 · sha256 dddd3333eeee/)).toBeInTheDocument()
  })
  it('disables the stored Cedar policy only after confirmation', async () => {
    api.pdpDisable.mockResolvedValue({
      engine: 'cedar',
      revision: 3,
      active: true,
      live_activation: 'applied',
    })
    const user = userEvent.setup()
    wrap(<CedarOpaView active />)
    await user.click(
      await screen.findByRole('button', { name: 'Disable policy' }),
    )
    expect(api.pdpDisable).not.toHaveBeenCalled()
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: 'Disable policy',
      }),
    )
    await waitFor(() => expect(api.pdpDisable).toHaveBeenCalledOnce())
  })
  it('retains OPA versioning', async () => {
    const user = userEvent.setup()
    wrap(<CedarOpaView active />)
    await selectOpa(user)
    expect(
      await screen.findByRole('button', { name: /^Version/i }),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(
        'Editing custom policies, roles and grants requires Olivares AI Business.',
      ),
    ).not.toBeInTheDocument()
  })
})

it('reads stored Cedar revision source without offering activation', async () => {
  api.pdpVersions.mockResolvedValue({
    items: [{ surface: 'cedar', revision: 1, validated: true }],
    has_more: false,
  })
  api.pdpGetVersion.mockResolvedValue({
    surface: 'cedar',
    revision: 1,
    content: 'forbid(principal, action, resource);',
  })
  const user = userEvent.setup()
  wrap(<CedarOpaView active />)
  await user.click(await screen.findByRole('button', { name: 'View policy' }))
  expect(
    await within(screen.getByRole('dialog')).findByRole('textbox'),
  ).toHaveTextContent('forbid(principal, action, resource);')
  expect(api.pdpGetVersion).toHaveBeenCalledWith('cedar', 1)
  expect(
    screen.queryByRole('button', { name: /^Activate/i }),
  ).not.toBeInTheDocument()
})

it('publishes a test written from the example and keeps a refusal on screen', async () => {
  const refusal =
    'not published, policy tests failed: "viewers locked out" expected deny, the policy answers allow'
  ext.authoring = {
    publish: vi
      .fn()
      .mockRejectedValue(new ApiError(400, 'bad_request', refusal)),
    rollback: vi.fn(),
  }
  const user = userEvent.setup()
  wrap(<CedarOpaView active />)

  await user.type(
    await screen.findByLabelText('Test name'),
    'viewers locked out',
  )
  await user.click(screen.getByRole('button', { name: 'Add as a test' }))
  await user.click(screen.getByRole('button', { name: 'Publish' }))
  await user.click(
    within(screen.getByRole('dialog')).getByRole('button', {
      name: 'Publish',
    }),
  )

  await waitFor(() =>
    expect(ext.authoring?.publish).toHaveBeenCalledWith(
      expect.any(String),
      undefined,
      [expect.objectContaining({ name: 'viewers locked out', expect: 'deny' })],
    ),
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(refusal)
  ext.authoring = undefined
})
