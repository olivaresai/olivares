// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// NEW SESSION, ONE IMPLEMENTATION for the sidebar button, the phone bar, the `N` key and the
// Sessions list's +: Sessions with no session in front, its start composer focused
// (features/first-hour WorkStart). Its More options opens the advanced launch.
import { useNavigate } from '@tanstack/react-router'
import { useNewSessionDialog } from '@/features/first-hour/new-session-store'

export function useNewSession(): () => void {
  const navigate = useNavigate()
  const compose = useNewSessionDialog((s) => s.compose)
  return () => {
    compose()
    void navigate({ to: '/sessions' as '/' })
  }
}
