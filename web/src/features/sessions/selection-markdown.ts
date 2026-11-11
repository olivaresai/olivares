// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A SELECTION OF A REPLY, COPIED AS THE MARKDOWN IT WAS. The reply is painted from Markdown
// by this console (./markdown-text), so the elements a selection can hold are a small, known
// set: paragraphs, bold, italic, code, links, lists, quotes, tables and fenced blocks. This
// reads them back. What it does not know it copies as its text; a control of the page
// (`data-md-skip`, a button) is never part of the copy.

function kids(node: Node): string {
  return Array.from(node.childNodes).map(walk).join('')
}

function cells(row: Element): string[] {
  return Array.from(row.children).map((cell) =>
    walk(cell).trim().replace(/\|/g, '\\|').replace(/\n+/g, ' '),
  )
}

function walk(node: Node): string {
  if (node.nodeType === Node.TEXT_NODE) return node.textContent ?? ''
  if (node.nodeType === Node.DOCUMENT_FRAGMENT_NODE) return kids(node)
  if (!(node instanceof Element)) return ''
  if (node instanceof HTMLElement && 'mdSkip' in node.dataset) return ''
  switch (node.tagName) {
    case 'BUTTON':
      return ''
    case 'STRONG':
    case 'B':
      return `**${kids(node)}**`
    case 'EM':
    case 'I':
      return `*${kids(node)}*`
    case 'CODE':
      return `\`${node.textContent ?? ''}\``
    case 'A':
      return `[${kids(node)}](${node.getAttribute('href') ?? ''})`
    case 'BR':
      return '\n'
    case 'PRE': {
      const lang = node.getAttribute('data-lang') ?? ''
      const body = (node.textContent ?? '').replace(/\n$/, '')
      return `\`\`\`${lang}\n${body}\n\`\`\`\n\n`
    }
    case 'UL':
    case 'OL': {
      const first = Number.parseInt(node.getAttribute('start') ?? '1', 10) || 1
      const items = Array.from(node.children).map((li, i) => {
        const mark = node.tagName === 'OL' ? `${first + i}. ` : '- '
        const body = walk(li)
          .trim()
          .replace(/\n/g, `\n${' '.repeat(mark.length)}`)
        return `${mark}${body}\n`
      })
      return `${items.join('')}\n`
    }
    case 'BLOCKQUOTE':
      return `${kids(node)
        .trim()
        .split('\n')
        .map((line) => `> ${line}`.trimEnd())
        .join('\n')}\n\n`
    case 'TR':
      return `| ${cells(node).join(' | ')} |\n`
    case 'THEAD': {
      const head = node.querySelector('tr')
      const width = head ? head.children.length : 0
      return `${kids(node)}|${' --- |'.repeat(width)}\n`
    }
    case 'P':
    case 'DIV':
    case 'TABLE':
      return `${kids(node)}\n\n`
    default:
      return kids(node)
  }
}

/** The Markdown of a selection's contents (`range.cloneContents()`), or '' when nothing
 * is left of it. */
export function selectionMarkdown(contents: Node): string {
  return walk(contents)
    .replace(/\n{3,}/g, '\n\n')
    .trim()
}
