// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The step-up gate must carry its OWN strings. `RequireAssurance` / `StepUpPanel` are
// rendered from chunks that never import the identity view — the first-boot wizard,
// the console tabs, residency, the support bundle, the inference proxy — and until
// this was fixed those screens printed `assurance.stepUpTitle`, `assurance.stepUpBody`
// and a button labelled `assurance.authenticate` at the exact moment the operator was
// told to authenticate.
//
// THIS FILE MUST NOT IMPORT './i18n'. That is the whole experiment: the only thing
// that can register the `identity` namespace here is the module under test. Vitest
// isolates modules per file, so no other test's imports leak in. Delete
// `import './i18n'` from assurance.tsx and every assertion below fails.
import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactElement } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { createTestQueryClient } from '@/test/intel'
import { expectNoRawI18nKeys } from '@/test/i18n-keys'

const { authState } = vi.hoisted(() => ({
  authState: {
    principal: { aal: 1, amr: ['pwd'] } as { aal: number; amr: string[] },
  },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

import { AAL, RequireAssurance, StepUpPanel } from './assurance'

function wrap(ui: ReactElement) {
  return render(
    <QueryClientProvider client={createTestQueryClient()}>
      {ui}
    </QueryClientProvider>,
  )
}

describe('assurance step-up i18n', () => {
  it('renders real copy, not the key, with only ./assurance imported', () => {
    const { container } = wrap(
      <StepUpPanel
        minAal={AAL.HARDWARE}
        currentAal={AAL.PASSWORD}
        action="console"
      />,
    )

    expect(
      screen.getByText('Step-up authentication required'),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Authenticate with security key' }),
    ).toBeInTheDocument()
    // The body interpolates three translated fragments; each must be real copy.
    expect(
      screen.getByText(/requires AAL3 \(hardware, phishing-resistant\)/),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/Your session is AAL1 \(password\)/),
    ).toBeInTheDocument()
    expectNoRawI18nKeys(container)
  })

  it('gates a subtree below the minimum AAL and still speaks English', () => {
    const { container } = wrap(
      <RequireAssurance minAal={AAL.HARDWARE} action="identity">
        <p>the privileged form</p>
      </RequireAssurance>,
    )

    expect(screen.queryByText('the privileged form')).not.toBeInTheDocument()
    expect(
      screen.getByText('Step-up authentication required'),
    ).toBeInTheDocument()
    // `action` selects a translated fragment too — a missing namespace loses it.
    expect(screen.getByText(/Managing identity requires/)).toBeInTheDocument()
    expectNoRawI18nKeys(container)
  })

  it('passes the gated subtree through once the session is elevated', () => {
    authState.principal = { aal: AAL.HARDWARE, amr: ['webauthn'] }
    try {
      wrap(
        <RequireAssurance minAal={AAL.HARDWARE} action="identity">
          <p>the privileged form</p>
        </RequireAssurance>,
      )
      expect(screen.getByText('the privileged form')).toBeInTheDocument()
      expect(
        screen.queryByText('Step-up authentication required'),
      ).not.toBeInTheDocument()
    } finally {
      authState.principal = { aal: 1, amr: ['pwd'] }
    }
  })
})
