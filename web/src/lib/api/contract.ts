// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// isContractPending — the shared seam helper. A DECLARED endpoint (a UI data contract
// a feature session EXPOSES for a backend session to implement) is "pending" when the
// engine answers 404 / 501 / 405. The admin dashboards use this to render an
// honest "backend pending" seam (SeamBadge + ContractPendingNotice) instead of faking
// a success or showing a red error (the claude-policy / identity
// precedent). New seam features import THIS one rather than copying it again.
import { ApiError } from './errors'

export function isContractPending(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false
  return error.status === 404 || error.status === 501 || error.status === 405
}
