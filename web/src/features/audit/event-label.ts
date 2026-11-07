// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readableAction } from '@/components/layout/bell-events'

/** The verbs of a recorded change: the engine writes `<what>.<verb>` for every audited record. */
const CHANGES = new Set(['create', 'update', 'delete'])
/** A provider's tool-call decision: the engine writes `<driver>.approval.<allow|ask|deny>`. */
const TOOL_APPROVAL = /^[a-z0-9_]+\.approval\.(allow|ask|deny)$/

/** Labels describe only the action recorded by the engine; the original code stays in Details.
 *  An action without a label of its own that records a change reads as what changed and how
 *  ("Availability created"), and a provider's tool-call decision as that decision ("Tool call
 *  held for approval"), never as its record name ("Availability create", "Codex approval ask"). */
export function eventLabel(
  action: string,
  t: (key: string, options: { defaultValue: string; what?: string }) => string,
): string {
  const cut = action.lastIndexOf('.')
  const verb = action.slice(cut + 1)
  const fallback = readableAction(action)
  const decision = TOOL_APPROVAL.exec(action)?.[1]
  return t(`actions.${action.replaceAll('.', '_')}`, {
    defaultValue: decision
      ? t(`toolApproval.${decision}`, { defaultValue: fallback })
      : cut > 0 && CHANGES.has(verb)
        ? t(`verbs.${verb}`, {
            what: readableAction(action.slice(0, cut)),
            defaultValue: fallback,
          })
        : fallback,
  })
}
