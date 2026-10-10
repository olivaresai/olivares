// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readableAction } from '@/components/layout/bell-events'

/** Labels describe only the action recorded by the engine; the original code stays in Details. */
export function eventLabel(
  action: string,
  t: (key: string, options: { defaultValue: string }) => string,
): string {
  return t(`actions.${action.replaceAll('.', '_')}`, {
    defaultValue: readableAction(action),
  })
}
