// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT A MODULE'S ACTIVATION STATE SAYS TO THE OPERATOR (EU-07).
//
// `pending` used to read "pending restart" for every staged module, including the ones
// that wait for a secret or a policy review: three restarts later they still said it.
// A restart supplies neither. So a pending module is labelled from what the engine says
// it waits for — its `needs_secret` flag and the lead clause of its `reason`
// ("needs a secret — fill …", "needs review — …", "blocked on X — …") — and says
// "pending restart" only when the status reports a restart is really required.
//
// Operational state comes from `state` alone. The coverage facts (`in_build`,
// `license_covered`) never make a module read as on, ready or active.

/** The reason's lead clause and its detail, split at the engine's em dash. */
export function splitReason(reason: string | undefined): {
  head?: string
  detail?: string
} {
  const text = reason?.trim()
  if (!text) return {}
  const at = text.indexOf(' — ')
  if (at <= 0) return { detail: text }
  return { head: text.slice(0, at).trim(), detail: text.slice(at + 3).trim() }
}

export type ActivationLabel =
  | { kind: 'active' | 'available' | 'console' }
  | { kind: 'restart' }
  | { kind: 'staged' }
  | { kind: 'secret'; detail?: string }
  | { kind: 'review'; detail?: string }
  | { kind: 'input'; detail?: string }
  | { kind: 'reason'; head: string; detail?: string }
  | { kind: 'other'; state: string }

export function activationLabel(
  addon: { state: string; reason?: string; needs_secret?: boolean },
  restartRequired: boolean,
): ActivationLabel {
  switch (addon.state) {
    case 'active':
    case 'available':
    case 'console':
      return { kind: addon.state }
    case 'pending':
      break
    default:
      return { kind: 'other', state: addon.state }
  }
  const { head, detail } = splitReason(addon.reason)
  const lead = head?.toLowerCase()
  if (addon.needs_secret || lead === 'needs a secret')
    return { kind: 'secret', detail }
  if (lead === 'needs review') return { kind: 'review', detail }
  if (head) return { kind: 'reason', head, detail }
  if (detail) return { kind: 'input', detail }
  return restartRequired ? { kind: 'restart' } : { kind: 'staged' }
}

export function activationTone(
  label: ActivationLabel,
): 'success' | 'warning' | 'neutral' | 'accent' {
  switch (label.kind) {
    case 'active':
      return 'success'
    case 'console':
      return 'accent'
    case 'available':
    case 'other':
      return 'neutral'
    default:
      return 'warning'
  }
}

/** The badge text: localized for the states the console knows, the engine's own
 * words for any other reason it gives. */
export function activationText(
  label: ActivationLabel,
  t: (key: string, opts?: Record<string, unknown>) => string,
): string {
  switch (label.kind) {
    case 'active':
      return t('console:entitlement.state.active')
    case 'available':
      return t('console:entitlement.state.available')
    case 'console':
      return t('console:entitlement.state.console')
    case 'restart':
      return t('console:entitlement.state.pending')
    case 'staged':
      return t('console:entitlement.state.staged')
    case 'secret':
      return t('console:entitlement.state.needsSecret')
    case 'review':
      return t('console:entitlement.state.needsReview')
    case 'input':
      return t('console:entitlement.state.needsInput')
    case 'reason':
      return label.head
    case 'other':
      return label.state
  }
}

/** The detail line under the badge, when the engine gave one. */
export function activationDetail(label: ActivationLabel): string | undefined {
  return 'detail' in label ? label.detail : undefined
}
