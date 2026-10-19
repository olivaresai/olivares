// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import type { APIRoute } from 'astro'
import { getCollection } from 'astro:content'
import openapi from '../../../web/openapi/openapi.json'
import { corePathCount, renderLlmsFullTxt } from '../llms.mjs'

// /llms-full.txt: the full text of every page /llms.txt lists, as Markdown. See src/llms.mjs.
export const GET: APIRoute = async () =>
  new Response(renderLlmsFullTxt(await getCollection('docs'), corePathCount(openapi)), {
    headers: { 'Content-Type': 'text/plain; charset=utf-8' },
  })
