// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
/**
 * Text nodes that look like an unresolved i18n key (`next.provider.title`) rather
 * than a sentence. Deliberately narrow: it wants dotted lowerCamel segments with no
 * spaces, which is what i18next echoes back when a key is missing, and which real
 * copy in seven languages never looks like. Identifiers a screen legitimately shows —
 * a host, a file name, a model reference — carry a slash, a digit or a capital, or
 * live inside `<code>`/`<pre>`, so they are excluded rather than whitelisted one by one.
 */
export function rawKeyProbe(): string[] {
  const bad: string[] = []
  // ⛔ THE WHOLE BODY, NOT `#main-content`. The first version of this probe walked
  //    the main landmark, which on /login is the CARD — so the statement column, the
  //    theme toggle and the deployment footer were all outside what
  //    it measured, and a missing `auth:shell.*` key would have rendered its raw key
  //    in seven languages under a green test. Found by re-reading my own instrument,
  //    which is the failure mode a review names: a check that looks like coverage and
  //    measures less than it claims.
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  const KEYISH = /^[a-z][a-zA-Z]*(\.[a-z][a-zA-Z]*){1,4}$/
  let node: Node | null
  while ((node = walker.nextNode())) {
    const text = (node.textContent ?? '').trim()
    if (!KEYISH.test(text)) continue
    const parent = node.parentElement
    if (!parent) continue
    if (parent.closest('code, pre, [data-allow-dotted]')) continue
    bad.push(text)
  }
  return bad
}
