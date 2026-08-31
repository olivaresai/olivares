// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Slice A: the enterprise-activation table's Promote action is a privileged
// write on a PAID add-on, and until it was the one privileged control on
// this screen that offered no step-up ceremony.
//
// WHAT THIS DOES AND DOES NOT CLAIM. The engine was never open: handleActivationApply
// calls requireAAL3 BEFORE dispatching on the action (core/api/handlers_activation.go
// :75-82), so `promote` was refused below AAL3 exactly like `enable`. What was missing
// is the operator's way THROUGH that refusal ON THIS SCREEN: the 403 code the engine
// returns has no runtime consumer in the console (lib/api/client.ts rethrows it), so
// the operator got a red toast and no inline path to elevate.
//
// ⚠ NOT claimed, after the contrast refuted it: that RequireAssurance is the only
// access to the ceremony. StepUpPanel is rendered directly by the privileged-login tab
// and by the inference-proxy view; an operator could elevate elsewhere and return. This
// closes a dead end in context, which is smaller than "the only door" and still real.
//
// WHY THIS FILE EXISTS SEPARATELY FROM license-tab.test.tsx: that file mocks
// `@/features/identity/assurance` with a PASS-THROUGH (:26-30). A witness written
// there could never go red, because the double renders the gated content whether the
// gate is present or not — the same reason onboarding-i18n.test.tsx had to be written
// beside onboarding.test.tsx. This file runs the REAL gate at a real AAL.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    getLicense: vi.fn(),
    getActivation: vi.fn(),
    previewActivation: vi.fn(),
    applyActivation: vi.fn(),
  },
  authState: {
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number },
    can: (_p: string): boolean => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
// NOTE: `@/features/identity/assurance` is deliberately NOT mocked — the whole
// point of this file is the gate's real behaviour at a real AAL.
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { LicenseTab } from './license-tab'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const enterpriseLicense = {
  edition: 'enterprise',
  status: 'valid',
  source: 'data-dir',
  managed_externally: false,
  licensee: 'Acme Corp',
  active_users: 12,
}

const stagedActivation = {
  edition: 'enterprise',
  preset: 'regulated-operations',
  restart_required: true,
  presets: [{ name: 'regulated-operations', addons: ['audit-worm-archive'] }],
  addons: [
    {
      key: 'audit-worm-archive',
      title: 'WORM audit archive',
      summary: 'Write-once retention for the audit ledger.',
      env: '',
      preset: 'regulated-operations',
      state: 'pending',
      reason: 'needs a bucket credential',
    },
  ],
}

/** Open the promote dialog for the single staged add-on. */
async function openPromoteDialog(user: ReturnType<typeof userEvent.setup>) {
  const row = (await screen.findByText('WORM audit archive')).closest('tr')
  if (!row) throw new Error('the staged add-on row did not render')
  await user.click(within(row as HTMLElement).getByRole('button'))
  return screen.findByRole('dialog')
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.isSuperadmin = true
  authState.principal = { aal: 3 }
  api.getLicense.mockResolvedValue(enterpriseLicense)
  api.getActivation.mockResolvedValue(stagedActivation)
  api.applyActivation.mockResolvedValue(stagedActivation)
})

describe('LicenseTab activation promote — assurance gate', () => {
  it('offers the step-up ceremony instead of the promote confirmation below AAL3', async () => {
    // A password session: the state of any operator who has not stepped up yet.
    authState.principal = { aal: 1 }
    const user = userEvent.setup()
    wrap(<LicenseTab />)

    const dialog = await openPromoteDialog(user)

    // The REAL gate replaced the confirmation with the ceremony…
    expect(
      within(dialog).getByText('Step-up authentication required'),
    ).toBeInTheDocument()
    // …and the write it guards is unreachable: no confirm button, nothing sent.
    expect(
      within(dialog).queryByRole('button', { name: /^activate$/i }),
    ).not.toBeInTheDocument()
    expect(api.applyActivation).not.toHaveBeenCalled()
  })

  it('keeps an accessible name while the step-up panel is showing', async () => {
    // The contrast's a11y finding: RequireAssurance REPLACES its children,
    // so a DialogTitle rendered inside the gate disappears exactly when the
    // operator is below AAL3 — leaving a screen reader with an unnamed modal.
    // dialog.test.tsx treats "accessible name from the title" as an invariant,
    // and the first cut of this file passed while violating it because it only
    // ever asked for `getByRole('dialog')` with no name matcher.
    authState.principal = { aal: 1 }
    const user = userEvent.setup()
    wrap(<LicenseTab />)

    await openPromoteDialog(user)

    expect(
      screen.getByRole('dialog', { name: /activate worm audit archive/i }),
    ).toBeInTheDocument()
  })

  it('promotes the staged add-on once the session is AAL3', async () => {
    // The NON-FIRING direction. Without this cell, a gate that blocked every
    // session — or a promote button that never worked at all — would satisfy the
    // test above and look like a passing guard.
    const user = userEvent.setup()
    wrap(<LicenseTab />)

    const dialog = await openPromoteDialog(user)

    expect(
      within(dialog).queryByText('Step-up authentication required'),
    ).not.toBeInTheDocument()
    await user.click(
      within(dialog).getByRole('button', { name: /^activate$/i }),
    )
    await waitFor(() =>
      expect(api.applyActivation).toHaveBeenCalledWith({
        action: 'promote',
        addon: 'audit-worm-archive',
      }),
    )
  })

  it('states which add-on is being activated, in real copy', async () => {
    // The dialog is new surface, so it is also new copy: pin that it resolves
    // rather than rendering a raw `console:activation.promoteTitle` key at the
    // operator (the defect onboarding-i18n.test.tsx was written for).
    const user = userEvent.setup()
    wrap(<LicenseTab />)

    const dialog = await openPromoteDialog(user)

    expect(
      within(dialog).getByText('Activate WORM audit archive'),
    ).toBeInTheDocument()
    expect(dialog.textContent).not.toMatch(/activation\.promote(Title|Hint)/)
  })
})
