// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHICH VERB, WHERE IT LIVES, AND WHAT AUTHORIZES IT (D21).
//
// Kept apart from the component that renders it for two reasons, and only the second
// one is about lint. (1) It is the part a test and a reviewer read: three rows that
// say what the front door offers. (2) A .tsx file that exports components AND
// constants trips `react-refresh/only-export-components`, and this console's warning
// count is a measured control — D15 proved its 42 rule violations were repaired and
// not demoted by showing the warning count identical before and after.
import { FEATURE_VIEWS } from '@/features/registry'

/**
 * The three verbs the brief names, each bound to the view that performs it and to the
 * permission that authorizes it. `id` is also the i18n key segment
 * (`home:next.<id>.title` / `.description`).
 *
 * ⛔ THE PERMISSION IS THE **WRITE** ONE, NOT THE ROUTE'S READ PERMISSION. A card that
 *    says "Add a provider" to a principal who may only read the provider plane is an
 *    invitation to a 403. specification04 §1 states the rule the palette already
 *    follows — "read permission for its page does not authorize it" — and this is that
 *    rule applied to an OFFER rather than to a command.
 */
export const NEXT_STEPS = [
  {
    id: 'provider',
    viewId: 'providerProfiles',
    permission: 'sessions:profile:write',
  },
  { id: 'agent', viewId: 'deploy', permission: 'deploy:deployment:write' },
  { id: 'session', viewId: 'agentops', permission: 'sessions:run:write' },
] as const

export type NextStepId = (typeof NEXT_STEPS)[number]['id']

/**
 * The registry entry a step points at, or undefined if the id no longer exists.
 *
 * ⛔ THE PATH IS NEVER TYPED HERE. `FEATURE_VIEWS` is the route table and
 *    `route-census.json` pins it; a path written in this file would be a second copy
 *    that goes stale the day a route moves, and the card would quietly point at a 404.
 *    `next-step.test.tsx` fails if an id stops resolving.
 */
export function stepView(viewId: string) {
  return FEATURE_VIEWS.find((v) => v.id === viewId)
}
