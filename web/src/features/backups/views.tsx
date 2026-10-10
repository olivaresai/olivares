// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { DatabaseBackup } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

//Backup/Restore — DR management console (backup trigger, list, restore,
// schedule). Superadmin-only system view.
const BackupsView = lazy(() =>
  import('./backups-view').then((m) => ({
    default: m.BackupsView,
  })),
)

export const VIEWS = [
  //Backup/Restore — DR management console: list, trigger, download,
  // restore with dual-confirmation, scheduling. Superadmin-only.
  {
    order: 650,
    id: 'backups',
    path: '/backups',
    navigation: {
      kind: 'feature',
      areaId: 'system',
      sectionId: 'maintenance',
    },
    helpHref: '/how-to/backup-and-restore',
    icon: DatabaseBackup,
    permission: 'system:admin',
    element: lazyView(BackupsView),
  },
] satisfies ViewEntry[]
