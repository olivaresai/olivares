// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// While the tool reports a login in progress, a profile row offers no second sign-in
// (#3388): it says "Signing in…" and its button is disabled, and it never reads
// as signed in before the tool's own status says so.
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { SignInFlow, SignInStatus } from '@/features/first-hour/api'
import {
  isSigningIn,
  ProfileRowView,
  type ProfileRowModel,
} from './profile-row'
import './i18n'

const flow = (state: SignInFlow['state']): SignInFlow => ({
  id: 'f1',
  driver: 'codex',
  state,
})
const status = (pending?: SignInFlow): SignInStatus => ({
  driver: 'codex',
  installed: true,
  signed_in: false,
  pending,
})

describe('a login in progress', () => {
  it('is live until the tool signs in or fails', () => {
    for (const state of [
      'starting',
      'needs_code',
      'needs_key',
      'waiting',
      'checking',
    ] as const)
      expect(isSigningIn(status(flow(state))), state).toBe(true)
    expect(isSigningIn(status(flow('signed_in')))).toBe(false)
    expect(isSigningIn(status(flow('failed')))).toBe(false)
    expect(isSigningIn(status())).toBe(false)
    expect(isSigningIn(undefined)).toBe(false)
  })

  it('disables the row sign-in and says so', () => {
    const model: ProfileRowModel = {
      id: 'work',
      name: 'work',
      state: { kind: 'account', signedIn: false, signingIn: true },
      ready: false,
    }
    const onSignIn = vi.fn()
    render(
      <ul>
        <ProfileRowView model={model} onOpen={vi.fn()} onSignIn={onSignIn} />
      </ul>,
    )
    expect(
      screen.queryByRole('button', { name: /^Sign in/ }),
    ).not.toBeInTheDocument()
    const busy = screen.getByRole('button', { name: /^Signing in/ })
    expect(busy).toBeDisabled()
    expect(screen.getByText('Account · signing in…')).toBeInTheDocument()
  })
})
