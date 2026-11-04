// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { RECORDING_NOTICE_EXTENSION } from './extensions'

/**
 * Where a view that recording can monitor shows the monitoring notice. Recording is a Business
 * capability, so only a build whose extensions module provides the notice fills the slot;
 * Community shows the view's own `fallback`, or nothing.
 */
export function RecordingNoticeSlot({
  namespace,
  fallback = null,
}: {
  /** The mounting view's module namespace. */
  namespace: string
  fallback?: ReactNode
}) {
  const Notice = RECORDING_NOTICE_EXTENSION
  return Notice ? (
    <Notice namespace={namespace} fallback={fallback} />
  ) : (
    fallback
  )
}
