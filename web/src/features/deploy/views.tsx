// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Rocket } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const DeployView = lazy(() => import('./deploy-view'))

export const VIEWS = [
  {
    order: 230,
    id: 'deploy',
    path: '/deploy',
    navigation: {
      kind: 'feature',
      areaId: 'deployment',
      sectionId: 'deployments',
    },
    helpHref: '/reference/modules/vii-deploy',
    icon: Rocket,
    permission: 'deploy:deployment:read',
    element: lazyView(DeployView),
  },
] satisfies ViewEntry[]
