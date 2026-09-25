// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'

import { ApiError } from '@/lib/api/errors'

import { classifyFailure } from './errors'

/**
 * The served 503 body, byte for byte as the engine writes it: `writeJSON`
 * encodes a map, so the three top-level keys come out in sorted order and the
 * nested error object carries the same code as its message. The console parses
 * this envelope, so the fixture has to be it and not a convenient shortening.
 */
const COMMIT_OUTCOME_UNKNOWN_BODY = {
  code: 'commit_outcome_unknown',
  error: { code: 'commit_outcome_unknown', message: 'commit_outcome_unknown' },
  verdict: 'NO_HE_PODIDO_MIRAR',
}

const EVIDENCE_UNAVAILABLE_BODY = {
  code: 'evidence_unavailable',
  error: { code: 'evidence_unavailable', message: 'evidence_unavailable' },
  verdict: 'NO_HE_PODIDO_MIRAR',
}

function apiErrorOf(body: { code: string }): ApiError {
  return new ApiError(503, body.code, body.code, 'req-c32', {}, body)
}

describe('classifyFailure and the commit outcome', () => {
  /**
   * WHY THIS IS NOT `unavailable`. Both answers are 503 and both carry the
   * UNKNOWN verdict, so without this arm the console would show an undetermined
   * write with the same screen it shows for "the engine could not look" — and
   * that screen tells the operator the act did not happen and discards the
   * intent they were holding. `ambiguous` is the kind the console already has
   * for "whether the write happened is UNDETERMINED", and it is the one that
   * keeps the intent on screen.
   */
  it('maps commit_outcome_unknown to ambiguous, not unavailable', () => {
    const f = classifyFailure(apiErrorOf(COMMIT_OUTCOME_UNKNOWN_BODY))
    expect(f.kind).toBe('ambiguous')
    // The code and verdict survive, so the notice can name what happened.
    expect(f.code).toBe('commit_outcome_unknown')
    expect(f.verdict).toBe('NO_HE_PODIDO_MIRAR')
    expect(f.status).toBe(503)
  })

  /** The preservation positive: every other 503 is still `unavailable`. */
  it('leaves another 503 as unavailable', () => {
    const f = classifyFailure(apiErrorOf(EVIDENCE_UNAVAILABLE_BODY))
    expect(f.kind).toBe('unavailable')
    expect(f.code).toBe('evidence_unavailable')
  })

  /**
   * The arm is keyed on the CODE, not on the status: a future 503 code must not
   * inherit the ambiguous screen, and a bare 503 with no code at all is still
   * "could not look" rather than "may have happened".
   */
  it('does not treat a bare 503 as ambiguous', () => {
    const f = classifyFailure(
      new ApiError(
        503,
        'internal',
        'unavailable',
        'req-c32',
        {},
        {
          error: { message: 'unavailable' },
        },
      ),
    )
    expect(f.kind).toBe('unavailable')
  })
})
