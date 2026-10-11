// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ApiError } from '@/lib/api/errors'

/**
 * Whether a failed read of HEAD's text is a failure worth saying so. A 404 is an answer
 * (no readable repository, or a file never committed); a refusal, a file too large or a
 * server error is not, and the person is told the comparison could not be made.
 */
export function headUnavailable(error: unknown): boolean {
  return (
    error !== null &&
    error !== undefined &&
    !(error instanceof ApiError && error.status === 404)
  )
}
