// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The READ half of the step-up story. `AsyncSection` and `DeclaredSection` are the
// two shared renderers every intel/system view routes its query states through, so
// their 403 branch decides what ~29 views say when the engine refuses a read for
// ASSURANCE. Both branched on the status alone and drew ForbiddenState — "you do
// not have permission" — for a session that only needed elevating.
//
// The panel is loaded lazily, so these assertions wait for the chunk: a test that
// asserted synchronously would pass against the Suspense fallback and see nothing.
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { renderIntel, screen, waitFor } from '@/test/intel'
import { AsyncSection } from './async'
import { DeclaredSection } from './declared'

// The ceremony itself is covered by the identity suite; here it only has to be
// DISTINGUISHABLE from the forbidden wall, so a light stand-in keeps this test
// about the branch and not about WebAuthn.
vi.mock('@/features/identity/assurance', () => ({
  StepUpPanel: () => <div>step-up ceremony</div>,
}))

function failing(error: Error) {
  return {
    data: undefined,
    isLoading: false,
    isError: true as const,
    error,
    refetch: vi.fn(),
  }
}

describe('shared read sections — assurance is not a missing permission', () => {
  it('AsyncSection offers the ceremony on step_up_required, not the forbidden wall', async () => {
    renderIntel(
      <AsyncSection
        query={failing(
          new ApiError(403, 'step_up_required', 'step-up required'),
        )}
      >
        {() => <div>gated content</div>}
      </AsyncSection>,
    )

    await waitFor(() =>
      expect(screen.getByText('step-up ceremony')).toBeInTheDocument(),
    )
    // The lie is the assertion: no "you do not have permission" copy, and the
    // gated content still does not leak.
    expect(screen.queryByText(/permission/i)).not.toBeInTheDocument()
    expect(screen.queryByText('gated content')).not.toBeInTheDocument()
  })

  // Non-firing direction: a section that showed the ceremony for EVERY 403 would
  // pass the case above while destroying the real forbidden state.
  it('AsyncSection keeps the forbidden wall for a plain forbidden 403', async () => {
    renderIntel(
      <AsyncSection query={failing(new ApiError(403, 'forbidden', 'nope'))}>
        {() => <div>gated content</div>}
      </AsyncSection>,
    )

    await waitFor(() =>
      expect(screen.queryByText('step-up ceremony')).not.toBeInTheDocument(),
    )
    expect(screen.getByText(/permission/i)).toBeInTheDocument()
  })

  it('DeclaredSection offers the ceremony on step_up_required', async () => {
    renderIntel(
      <DeclaredSection
        what="the thing"
        query={failing(
          new ApiError(403, 'step_up_required', 'step-up required'),
        )}
      >
        {() => <div>gated content</div>}
      </DeclaredSection>,
    )

    await waitFor(() =>
      expect(screen.getByText('step-up ceremony')).toBeInTheDocument(),
    )
    expect(screen.queryByText('gated content')).not.toBeInTheDocument()
  })
})
