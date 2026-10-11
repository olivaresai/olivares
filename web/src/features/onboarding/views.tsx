// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Compass } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Onboarding wizard — first-time deployment setup (overview group).
const OnboardingView = lazy(() =>
  import('./index').then((m) => ({ default: m.OnboardingView })),
)

export const VIEWS = [
  // Onboarding wizard: first-time deployment setup. Superadmin-gated so only
  // the operator who configures the deployment sees it. Not hidden — it surfaces
  // in the nav as a persistent reminder until the operator completes or dismisses.
  {
    order: 20,
    id: 'onboarding',
    path: '/onboarding',
    navigation: {
      kind: 'feature',
      areaId: 'system',
      sectionId: 'maintenance',
    },
    helpHref: '/start/quickstart',
    // Compass, not Rocket: Rocket is Deploy's icon — a guided first-run
    // is wayfinding, and every registered view must carry a unique glyph.
    icon: Compass,
    permission: 'system:admin',
    element: lazyView(OnboardingView),
  },
] satisfies ViewEntry[]
