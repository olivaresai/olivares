// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the regression: after an upgrade with a tab open, the load guard (index.html) reloads
// the document once when the old build's lazy chunk is gone. The fresh document
// says what happened — "Updated, reloaded" — exactly once, on the app-wide toast
// surface, and forgets the flag; an ordinary boot says nothing.
import { render } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const toast = vi.hoisted(() => ({ success: vi.fn() }))
const restoreOnPageLoad = vi.hoisted(() => vi.fn().mockResolvedValue(undefined))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))
vi.mock('@/lib/auth/context', () => ({
  AuthProvider: ({ children }: { children: ReactNode }) => children,
}))
vi.mock('@/lib/auth/browser-session', () => ({ restoreOnPageLoad }))
vi.mock('@/features/agentops/auth-boundary', () => ({
  AuthBoundaryCustody: () => null,
}))
vi.mock('@/components/layout/step-up-host', () => ({
  StepUpHost: () => null,
}))

import { Providers } from '@/app/providers'
import { ReloadNotice, RELOAD_NOTICE_KEY } from './reload-notice'

beforeEach(() => {
  vi.clearAllMocks()
  sessionStorage.clear()
})

describe('the after-upgrade notice', () => {
  it('says "Updated, reloaded" once where the guard flagged the reload it started', () => {
    sessionStorage.setItem(RELOAD_NOTICE_KEY, '1')
    render(<ReloadNotice />)
    expect(toast.success).toHaveBeenCalledTimes(1)
    expect(toast.success).toHaveBeenCalledWith('Updated, reloaded')
    expect(sessionStorage.getItem(RELOAD_NOTICE_KEY)).toBeNull()
  })

  it('stays quiet on an ordinary boot', () => {
    render(<ReloadNotice />)
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('is mounted by the app providers', () => {
    sessionStorage.setItem(RELOAD_NOTICE_KEY, '1')
    render(<Providers>{null}</Providers>)
    expect(toast.success).toHaveBeenCalledTimes(1)
    expect(toast.success).toHaveBeenCalledWith('Updated, reloaded')
  })
})
