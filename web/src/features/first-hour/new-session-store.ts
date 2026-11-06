// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'

/** What the launch dialog opens on: a template from the catalog, the commit or branch a
 * handoff names for a new worktree, or the profile, model and effort a session's message
 * bar hands over for a new session. `driver` is the tool the New session form had chosen:
 * the default profile is read for it, not for another tool. */
export interface AdvancedLaunch {
  message?: string
  workspaceRef?: string
  templateId?: string
  worktreeFrom?: string
  driver?: string
  profileRef?: string
  model?: string
  effort?: string
  fastMode?: boolean
  serviceTier?: string
  runOn?: string
}

export interface SessionDraft {
  tool?: string
  workspace?: string
  prompt?: string
}

const focused = () =>
  document.activeElement instanceof HTMLElement ? document.activeElement : null

/** The two New session dialogs, mounted once in the authenticated shell (NewSessionHost),
 * and the start composer of the work surface. The sidebar button, the phone bar, the N key
 * and the list's + bring the composer forward (`compose`); Home, Estate and Providers open
 * the New session form; its Advanced options, the composer's More options, Templates and a
 * handoff open the advanced launch. */
export const useNewSessionDialog = create<{
  open: boolean
  draft: SessionDraft | null
  /** Where focus returns when the advanced launch closes: the door that opened it, or,
   * when the form handed over to it, the form's door, since the form's button is gone by
   * then. The dialogs have no trigger of their own to return to. */
  opener: HTMLElement | null
  /** The advanced launch while it is shown, kept after it closes so the same request
   * opens on the same draft. */
  advanced: (AdvancedLaunch & { open: boolean }) | null
  /** The work surface shows the start composer instead of a session. */
  composing: boolean
  /** Counts the requests to put the keyboard in the start composer. */
  composeRequest: number
  compose: () => void
  stopComposing: () => void
  setOpen: (open: boolean, opener?: HTMLElement | null) => void
  /** The form hands over to the advanced launch, keeping the form's opener and the
   * sign-in, model and effort it shows. */
  advance: (launch?: AdvancedLaunch) => void
  /** `opener` is where focus returns when the door itself goes away with the open (a
   * menu item). */
  openAdvanced: (launch?: AdvancedLaunch, opener?: HTMLElement | null) => void
  closeAdvanced: () => void
}>((set) => ({
  open: false,
  draft: null,
  opener: null,
  advanced: null,
  composing: false,
  composeRequest: 0,
  compose: () =>
    set((s) => ({ composing: true, composeRequest: s.composeRequest + 1 })),
  stopComposing: () => set({ composing: false }),
  setOpen: (open, opener) =>
    set(open ? { open, draft: null, opener: opener ?? focused() } : { open }),
  advance: (launch = {}) =>
    set({ open: false, advanced: { ...launch, open: true } }),
  openAdvanced: (launch = {}, opener) =>
    set({ opener: opener ?? focused(), advanced: { ...launch, open: true } }),
  closeAdvanced: () =>
    set((s) => (s.advanced ? { advanced: { ...s.advanced, open: false } } : s)),
}))

// Both close, and the advanced launch's draft is dropped, when the credential or the
// organization changes: no draft, template or handoff passes to the next sign-in or
// organization (as the sent first messages, an internal design note (not shipped)).
const forget = () =>
  useNewSessionDialog.setState({
    open: false,
    draft: null,
    opener: null,
    advanced: null,
    composing: false,
  })
useSessionStore.subscribe((s, prev) => {
  if (s.credentialGeneration !== prev.credentialGeneration) forget()
})
useTenantStore.subscribe((s, prev) => {
  if (s.activeTenant !== prev.activeTenant) forget()
})
