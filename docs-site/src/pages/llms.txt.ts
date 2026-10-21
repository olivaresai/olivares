// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import type { APIRoute } from 'astro'
import { getCollection } from 'astro:content'
import { renderLlmsTxt } from '../llms.mjs'

// /llms.txt (llmstxt.org): the index of the English docs for AI assistants. See src/llms.mjs.
export const GET: APIRoute = async () =>
  new Response(renderLlmsTxt(await getCollection('docs')), {
    headers: { 'Content-Type': 'text/plain; charset=utf-8' },
  })
