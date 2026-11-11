// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// What a line of a terminal links to: a web address, or a file of the session's folder at
// a line, opened in the editor the person chose in Settings, as a path in a reply is.
import { useClientSettings } from '@/features/settings/preferences'
import { editorLink, sshTarget } from '@/lib/editor-links'
import { pathInFolder, pathRef } from './markdown'

export interface TerminalLink {
  text: string
  /** Where the link starts and ends in the line. */
  start: number
  end: number
  href: string
  /** A web address opens a new tab; a file opens the person's editor. */
  external: boolean
}

const WRAPPED_START = /^[([{<'"`]+/
const WRAPPED_END = /[)\]}>'"`,;:.!?]+$/
const URL_START = /^https?:\/\//i

/**
 * The address a browser reads in `text` when it is http or https, else null. The parsed
 * address, not the text: `window.open` would resolve `https:foo` against this page. A
 * user name or password it carries is left out: it is not sent, and what the pointer
 * names is the address that opens.
 */
export function webAddress(text: string): string | null {
  try {
    const url = new URL(text)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return null
    url.username = ''
    url.password = ''
    return url.href
  } catch {
    return null // Not an address: it stays text.
  }
}

/**
 * What the pointer on a web address shows: the host first, as a person checks where a
 * link goes, then the address that opens. Null when the address would not open.
 */
export function webAddressTitle(text: string): string | null {
  const href = webAddress(text)
  return href ? `${new URL(href).host} · ${href}` : null
}

/** Opens an http or https address in a new tab that cannot reach this page. */
export function openWebAddress(text: string) {
  const href = webAddress(text)
  if (href) window.open(href, '_blank', 'noopener,noreferrer')
}

// A compiler says file:line:column; the editor is taken to the line.
const withoutColumn = (token: string) => token.replace(/^(.*:\d+):\d+$/, '$1')

function hrefOf(token: string, root: string | null): string | null {
  if (!root) return null
  const ref = pathRef(token)
  if (!ref) return null
  const { base, relative, inside } = pathInFolder(root, ref.path)
  if (!inside) return null
  const { editor, editorSsh } = useClientSettings.getState()
  return editorLink(
    editor,
    { root: base, file: relative, line: ref.line },
    sshTarget(editorSsh, window.location.hostname),
  )
}

// shortcut: one screen row at a time, so a link the terminal wrapped over two rows is not
// found whole, and offsets count characters, so a wide character before it shifts its
// underline; upgrade when someone hits it (xterm's own link addon has the wrap logic).
export function linksIn(line: string, root: string | null): TerminalLink[] {
  const links: TerminalLink[] = []
  for (const match of line.matchAll(/\S+/g)) {
    const lead = WRAPPED_START.exec(match[0])?.[0].length ?? 0
    const text = match[0].slice(lead).replace(WRAPPED_END, '')
    if (!text) continue
    const start = match.index + lead
    if (URL_START.test(text)) {
      const href = webAddress(text)
      if (href)
        links.push({
          text,
          start,
          end: start + text.length,
          href,
          external: true,
        })
      continue
    }
    const target = withoutColumn(text)
    const href = hrefOf(target, root)
    if (href)
      links.push({
        text: target,
        start,
        end: start + target.length,
        href,
        external: false,
      })
  }
  return links
}

export function openTerminalLink(link: TerminalLink) {
  if (link.external) openWebAddress(link.href)
  else window.location.href = link.href
}
