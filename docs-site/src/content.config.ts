// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import { defineCollection } from 'astro:content'
import { docsLoader } from '@astrojs/starlight/loaders'
import { docsSchema } from '@astrojs/starlight/schema'
import { docsVersionsLoader } from 'starlight-versions/loader'

export const collections = {
  docs: defineCollection({ loader: docsLoader(), schema: docsSchema() }),
  versions: defineCollection({ loader: docsVersionsLoader() }),
}
