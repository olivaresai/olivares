// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// E4 — every feature's helpHref must name a page that exists in THIS REPOSITORY's docs tree:
// the canonical English set under docs-site/src/content/docs (locale copies and dated snapshots
// derive from it).
//
// ⛔ AND THAT IS THE WHOLE OF WHAT IT PROVES. Until 2026-08-19 the line above said the check meant
// "the topbar help icon can never 404", and that conclusion was false while every assertion in the
// file passed — because the tree it reads is not the tree that is served. `docs-site/` is not
// deployed. The icon's base was `docs.olivares.ai`, a name since withdrawn from DNS, and the live
// documentation at olivares.ai/docs uses a different information architecture; measured against
// that site's own sitemap, 0 of the 38 destinations existed there. Thirty-eight broken help links
// coexisted with a green suite for as long as the guarantee was read off the wrong tree.
//
// A test cannot reach the deployed site — gates here make no network request, deliberately — so
// the honest fix is to say what this one covers and to name what it does not. What closes the gap
// is `DEEP_LINKS_PUBLISHED` in components/layout/topbar.tsx: while it is false the icon links to
// the docs index and these paths are a MAPPING held ready, not live URLs. Before flipping it to
// true, confirm the pages are reachable at the base — this file cannot, and will still pass.
//
// ⇒ THE FLIP HAPPENED ON 2026-08-27, and this note records the confirmation the paragraph above
// demands, because "somebody checked once" is not a record. docs-site is deployed as the
// `olivares-docs` Worker at docs.olivares.ai, `DOCS_BASE` moved back to it, and the 37 non-root
// helpHref values were requested one by one against `https://docs.olivares.ai<path>/`:
// **37/37 = 200, zero non-200**. That measurement is dated on purpose — it is a fact about a
// SITE, and this file still cannot see one. The standing check against the live host is
// `bash scripts/check-docs-site-live.sh`, which does make the requests and answers 0/1/2.
//
// So the division of labour is now explicit, and neither half claims the other's ground:
//   · this file  — every helpHref names a page that EXISTS IN THE TREE (no network)
//   · the script — the deployed site keeps the promises the tree makes (network, dated)
import { existsSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { FEATURE_VIEWS } from './registry'

const DOCS_ROOT = resolve(__dirname, '../../../docs-site/src/content/docs')

/** A slug resolves if `<slug>.md(x)` or `<slug>/index.md(x)` exists. */
function slugExists(slug: string): boolean {
  const rel = slug === '/' ? 'index' : slug.replace(/^\//, '')
  return (
    existsSync(join(DOCS_ROOT, `${rel}.md`)) ||
    existsSync(join(DOCS_ROOT, `${rel}.mdx`)) ||
    existsSync(join(DOCS_ROOT, rel, 'index.md')) ||
    existsSync(join(DOCS_ROOT, rel, 'index.mdx'))
  )
}

describe('registry helpHref', () => {
  it('the docs tree this test pins against is present', () => {
    expect(existsSync(DOCS_ROOT)).toBe(true)
  })

  it.each(FEATURE_VIEWS.map((v) => [v.id, v.helpHref] as const))(
    '%s → %s exists in the docs tree (not proof it is served — see the header)',
    (_id, helpHref) => {
      expect(helpHref).toMatch(/^\/([a-z0-9/-]*[a-z0-9])?$/)
      expect(slugExists(helpHref), `no docs page for ${helpHref}`).toBe(true)
    },
  )
})
