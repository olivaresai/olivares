// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console gates exactly where the engine does: a hardware floor is the
// administrative step-up, and whoami's step_up_satisfied (computed by the
// engine's own predicate) decides it. A fresh install asks for nothing beyond
// the sign-in, so a password session sees the form, not a passkey panel.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

const authState = vi.hoisted(() => ({
  principal: null as Record<string, unknown> | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

import { AAL, RequireAssurance } from './assurance'

function gate() {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <RequireAssurance minAal={AAL.HARDWARE} action="identity">
        <p>the form</p>
      </RequireAssurance>
    </QueryClientProvider>,
  )
}

describe('RequireAssurance follows the deployment step-up policy', () => {
  it('shows the form to a password session when the engine says the step-up is met', () => {
    authState.principal = {
      aal: 1,
      amr: ['pwd'],
      admin_step_up: 'none',
      step_up_satisfied: true,
    }
    gate()
    expect(screen.getByText('the form')).toBeInTheDocument()
  })

  it('withholds the form when the engine says the step-up is not met', () => {
    authState.principal = {
      aal: 1,
      amr: ['pwd'],
      admin_step_up: 'passkey',
      step_up_satisfied: false,
    }
    gate()
    expect(screen.queryByText('the form')).not.toBeInTheDocument()
  })

  it('falls back to the hardware floor against an engine that does not say', () => {
    authState.principal = { aal: 1, amr: ['pwd'] }
    gate()
    expect(screen.queryByText('the form')).not.toBeInTheDocument()
    authState.principal = { aal: 3, amr: ['pwd', 'webauthn'] }
    gate()
    expect(screen.getByText('the form')).toBeInTheDocument()
  })
})
