// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'

/**
 * The pending step-up demand, shared so the ONE global ceremony host can be
 * opened from anywhere a 403 `step_up_required` lands — a mutation's onError, a
 * hand-rolled catch — without every one of those call sites having to render a
 * panel of its own.
 *
 * WHY a store and not a returned render prop: `usePrivilegedMutation` has 76
 * callers. Handing each of them a panel to place means the fix ships only where
 * someone remembered it, and the failure mode of forgetting is the exact defect
 * this replaces — a toast that blames the operator's role for an assurance
 * problem. One host, mounted once in Providers, cannot be forgotten.
 *
 * The store carries no assurance of its own and grants nothing: it is a request
 * to show a ceremony the BACKEND will adjudicate (assurance.tsx — the panel
 * never fabricates an AAL the engine did not set).
 */
export interface StepUpRequest {
  /** i18n key fragment naming the gated action, e.g. "console", "wif" — the same
   *  vocabulary `RequireAssurance` uses (identity:assurance.actions.*). */
  action: string
  /** Re-run the call the engine refused, once the session is elevated. Optional:
   *  a caller that cannot safely repeat itself simply omits it and the operator
   *  retries by hand. */
  retry?: () => void
}

interface StepUpState {
  request: StepUpRequest | null
  /**
   * Demand a step-up. A demand already on screen is NOT replaced: the operator is
   * mid-ceremony, and swapping the panel under them would strand the retry of the
   * first call.
   *
   * Returns FALSE when it was refused for that reason, and the caller must say so.
   * It used to return nothing: two mutations refused before the operator finished
   * the first ceremony left the second with no panel, no toast and no retry — the
   * action simply vanished, which is a quieter version of the defect this whole
   * change exists to remove.
   */
  require: (request: StepUpRequest) => boolean
  clear: () => void
}

export const useStepUpStore = create<StepUpState>((set, get) => ({
  request: null,
  require: (request) => {
    if (get().request) return false
    set({ request })
    return true
  },
  clear: () => set({ request: null }),
}))
