// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// NEW SESSION, ONE IMPLEMENTATION for the sidebar button, the phone bar and the `N` key.
// It focuses the composer where one is mounted; elsewhere it opens Home, whose centre is
// the composer, and focuses it once it is there.
import { useNavigate } from '@tanstack/react-router'
import { COMPOSER_INPUT_ID } from './work-composer-id'

/** How long the composer may take to mount after the navigation, in 50 ms steps. */
const FOCUS_ATTEMPTS = 20

function focusComposer(): boolean {
  const field = document.getElementById(COMPOSER_INPUT_ID)
  if (!(field instanceof HTMLElement)) return false
  field.focus()
  return true
}

export function useNewSession(): () => void {
  const navigate = useNavigate()
  return () => {
    if (focusComposer()) return
    void Promise.resolve(navigate({ to: '/' as never })).then(() => {
      let attempts = 0
      const retry = () => {
        if (focusComposer() || ++attempts >= FOCUS_ATTEMPTS) return
        window.setTimeout(retry, 50)
      }
      retry()
    })
  }
}
