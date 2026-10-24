// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ASSISTANT'S WORDS, PAINTED AS THE MARKDOWN THEY ARE (#1784). The reply is parsed into
// data (./markdown) and painted as elements: nothing here builds a string of HTML, so what
// the model says can never become markup. A link says where it goes (its title) and opens
// without the opener.
import { Fragment, memo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { CopyButton } from '@/components/ui/copy-button'
import {
  parseMarkdown,
  type Block,
  type Inline,
  type HrefResolver,
} from './markdown'

interface MarkdownPresentation {
  renderLink?: (href: string, children: ReactNode) => ReactNode
  /** A wiki is a document; replies keep their headings as bold lines. */
  headingOffset?: number
}

function Inlines({
  nodes,
  renderLink,
}: { nodes: readonly Inline[] } & MarkdownPresentation): ReactNode {
  return nodes.map((node, i) => {
    if (typeof node === 'string') return <Fragment key={i}>{node}</Fragment>
    switch (node.kind) {
      case 'code':
        return (
          <code
            key={i}
            className="rounded-ctl bg-muted px-1 font-mono text-mono-s text-text"
          >
            {node.text}
          </code>
        )
      case 'strong':
        return (
          <strong key={i} className="font-semibold">
            <Inlines nodes={node.children} renderLink={renderLink} />
          </strong>
        )
      case 'em':
        return (
          <em key={i}>
            <Inlines nodes={node.children} renderLink={renderLink} />
          </em>
        )
      case 'link':
        if (renderLink)
          return (
            <Fragment key={i}>
              {renderLink(
                node.href,
                <Inlines nodes={node.children} renderLink={renderLink} />,
              )}
            </Fragment>
          )
        return (
          <a
            key={i}
            href={node.href}
            title={node.href}
            target="_blank"
            rel="noopener noreferrer"
            className="text-text underline underline-offset-[3px] decoration-text-3 decoration-1 hover:decoration-text hover:decoration-2"
          >
            <Inlines nodes={node.children} />
          </a>
        )
    }
  })
}

/** An item that is one paragraph reads as its words, without a paragraph box around them. */
function ListItem({
  blocks,
  ...presentation
}: { blocks: readonly Block[] } & MarkdownPresentation) {
  const only = blocks.length === 1 ? blocks[0] : undefined
  if (only?.kind === 'paragraph')
    return (
      <li>
        <Inlines nodes={only.inline} {...presentation} />
      </li>
    )
  return (
    <li>
      <Blocks blocks={blocks} gap="gap-1" {...presentation} />
    </li>
  )
}

function Blocks({
  blocks,
  gap = 'gap-2',
  ...presentation
}: {
  blocks: readonly Block[]
  gap?: string
} & MarkdownPresentation) {
  return (
    <div className={`flex flex-col ${gap}`}>
      {blocks.map((block, i) => {
        switch (block.kind) {
          case 'paragraph':
            return (
              <p key={i} className="whitespace-pre-wrap">
                <Inlines nodes={block.inline} {...presentation} />
              </p>
            )
          case 'heading': {
            if (presentation.headingOffset !== undefined) {
              const Heading =
                `h${Math.min(6, block.level + presentation.headingOffset)}` as 'h1'
              return (
                <Heading key={i} className="mt-3 text-heading font-semibold">
                  <Inlines nodes={block.inline} {...presentation} />
                </Heading>
              )
            }
            // The transcript is not the page's outline: a reply's heading is a bold line.
            return (
              <p key={i} className="font-semibold">
                <Inlines nodes={block.inline} {...presentation} />
              </p>
            )
          }
          case 'code':
            // A code block copies its text exactly (dev#2788).
            return (
              <div key={i} className="relative">
                <pre
                  tabIndex={0}
                  data-slot="reply-code"
                  className="m-0 overflow-x-auto rounded-ctl bg-muted p-3 pe-10 font-mono text-mono-s text-text focus-visible:ring-2 focus-visible:ring-focus"
                >
                  <code>{block.text}</code>
                </pre>
                <CodeCopy text={block.text} />
              </div>
            )
          case 'quote':
            return (
              <blockquote
                key={i}
                className="m-0 border-l-2 border-line pl-3 text-text-2"
              >
                <Blocks blocks={block.blocks} gap="gap-1" {...presentation} />
              </blockquote>
            )
          case 'list': {
            const Tag = block.ordered ? 'ol' : 'ul'
            return (
              <Tag
                key={i}
                start={block.ordered ? block.start : undefined}
                className={`m-0 flex flex-col gap-1 pl-5 ${block.ordered ? 'list-decimal' : 'list-disc'}`}
              >
                {block.items.map((item, n) => (
                  <ListItem key={n} blocks={item} {...presentation} />
                ))}
              </Tag>
            )
          }
          case 'table':
            return (
              <div
                key={i}
                tabIndex={0}
                className="overflow-x-auto focus-visible:ring-2 focus-visible:ring-focus"
              >
                <table className="w-full border-collapse text-caption">
                  <thead>
                    <tr>
                      {block.head.map((cell, c) => (
                        <th
                          key={c}
                          scope="col"
                          className="border-b border-line px-2 py-1 text-left font-medium"
                        >
                          <Inlines nodes={cell} {...presentation} />
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {block.rows.map((row, r) => (
                      <tr key={r}>
                        {row.map((cell, c) => (
                          <td
                            key={c}
                            className="border-b border-line px-2 py-1 align-top"
                          >
                            <Inlines nodes={cell} {...presentation} />
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          case 'rule':
            return <hr key={i} className="m-0 border-line" />
        }
      })}
    </div>
  )
}

/** The button sits on the block's corner; a failure is said under the block, where it
 * covers none of the code the person is told to select. */
function CodeCopy({ text }: { text: string }) {
  const { t } = useTranslation('common')
  return (
    <CopyButton
      text={() => text}
      label={t('ui.copy.code')}
      copiedLabel={t('ui.copy.copied')}
      failedLabel={t('ui.copy.failed')}
      className="absolute end-1 top-1 bg-muted"
    />
  )
}

/** Re-parsed only when the words change: a long thread repaints on every frame. */
export const MarkdownText = memo(function MarkdownText({
  text,
  resolveHref,
  ...presentation
}: {
  text: string
  resolveHref?: HrefResolver
} & MarkdownPresentation) {
  return <Blocks blocks={parseMarkdown(text, resolveHref)} {...presentation} />
})
