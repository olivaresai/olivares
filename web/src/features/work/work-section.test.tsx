// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { WorkSection } from './work-section'
import './i18n'

/**
 * — THE WIRING, not just the predicate.
 *
 * This file exists because a mutation caught the gap it closes. api.test.ts already
 * proved isUnknownVerdict recognises a 503 read carrying the verdict — and that test
 * stayed GREEN when WorkSection's branch was replaced with `if (false)`, i.e. when the
 * console went back to rendering "server error, retry" over an unknown outcome.
 *
 * A predicate everyone agrees with, wired to nothing, is the exact shape of a fix that
 * was never verified. So these cases render the component and read the SCREEN.
 */

type Q = Parameters<typeof WorkSection<{ ok: boolean }>>[0]['query']

const erroring = (error: unknown): Q => ({
  data: undefined,
  isLoading: false,
  isError: true,
  error: error as Error,
  refetch: (() => Promise.resolve()) as unknown as Q['refetch'],
})

const unknownRead = new ApiError(
  503,
  'observation_unavailable',
  'observation_unavailable',
  undefined,
  {},
  {
    verdict: 'NO_HE_PODIDO_MIRAR',
    code: 'observation_unavailable',
    error: {
      code: 'observation_unavailable',
      message: 'observation_unavailable',
    },
  },
)

const brokenRead = new ApiError(
  400,
  'invalid_command',
  'invalid_command',
  undefined,
  {},
  {
    verdict: 'ROTO',
    code: 'invalid_command',
    error: { code: 'invalid_command', message: 'invalid_command' },
  },
)

describe('WorkSection keeps the third outcome on the read path', () => {
  // FIRES IF: the verdict branch is removed — the mutation that survived until this
  // file existed.
  it('renders "could not look", naming the engine code, for an unknown verdict', () => {
    render(
      <WorkSection query={erroring(unknownRead)}>
        {() => <p>rows</p>}
      </WorkSection>,
    )
    expect(screen.getByText('Could not look')).toBeInTheDocument()
    expect(screen.getByText(/observation_unavailable/)).toBeInTheDocument()
    // And it is emphatically NOT the data branch.
    expect(screen.queryByText('rows')).not.toBeInTheDocument()
  })

  // DOES NOT FIRE FOR: an ordinary broken read. Routing every failure to "could not
  // look" would be the mirror defect — it would claim uncertainty the engine did not
  // report, and it would drop the retry the operator needs.
  it('NON-FIRING: a ROTO read keeps the ordinary retryable error card', () => {
    render(
      <WorkSection query={erroring(brokenRead)}>
        {() => <p>rows</p>}
      </WorkSection>,
    )
    expect(screen.queryByText('Could not look')).not.toBeInTheDocument()
  })

  it('NON-FIRING: a transport failure is still a transport failure', () => {
    render(
      <WorkSection query={erroring(new NetworkError('down'))}>
        {() => <p>rows</p>}
      </WorkSection>,
    )
    expect(screen.queryByText('Could not look')).not.toBeInTheDocument()
  })

  it('NON-FIRING: a successful read renders its data', () => {
    render(
      <WorkSection
        query={{
          data: { ok: true },
          isLoading: false,
          isError: false,
          error: null,
          refetch: (() => Promise.resolve()) as unknown as Q['refetch'],
        }}
      >
        {() => <p>rows</p>}
      </WorkSection>,
    )
    expect(screen.getByText('rows')).toBeInTheDocument()
  })
})
