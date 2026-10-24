// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { KeySquare } from 'lucide-react'
import { toolsRoom } from '@/features/agent-tools/views'
import type { ViewEntry } from '@/features/registry'

export const VIEWS = [
  {
    order: 330,
    // The credential a session launches with. It sits FIRST in the environments
    // section because it is the first thing a new operator needs and the last thing
    // the product used to offer: a profile with no credential launches nothing, and
    // the answer to "where does my API key go" used to be a variable in the server's
    // shell.
    id: 'providers',
    path: '/providers',
    navigation: {
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'environments',
    },
    helpHref: '/how-to/add-a-provider',
    // KeySquare and not KeyRound: the icon guard requires one lucide glyph per view,
    // and KeyRound is the channel-administration view's. Two screens sharing a glyph
    // is how a sidebar stops being scannable.
    icon: KeySquare,
    permission: 'sessions:provider:read',
    // A door into Tools (#2552), where the API keys are listed beside the accounts.
    // `?add=<kind>&returnTo=` (the setup wizard's "Add an API key") still opens the form.
    doorTo: 'agent-tools',
    element: toolsRoom,
  },
] satisfies ViewEntry[]
