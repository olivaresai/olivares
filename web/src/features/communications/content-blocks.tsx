// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { KvList, KvRow } from '@/components/ui/kv'
import type { ContentBlock, Fulfillment, MessageContent } from './types'

/**
 * INERT RENDERING of received content. Every block arrives from another principal
 * and is painted as TEXT: React escapes it, markdown is shown as the characters it
 * is, a reference is a label with a kind, a ref and a hash — never a link — and an
 * action reference is a code and a label, never a button. Nothing here fetches,
 * follows or executes anything the content names; the console has no authority to
 * derive from a message body, and a hostile body must land as harmless glyphs.
 */
export function Mono({ children }: { children: React.ReactNode }) {
  return (
    <code className="break-all font-mono text-caption tabular-nums">
      {children}
    </code>
  )
}

function ReferenceChip({
  reference,
}: {
  reference: NonNullable<ContentBlock['reference']>
}) {
  const { t } = useTranslation('communications')
  return (
    <span
      data-slot="content-reference"
      className="inline-flex max-w-full flex-wrap items-center gap-1 rounded-sm border border-border bg-muted px-1.5 py-0.5 text-caption"
    >
      <span className="text-muted-foreground">{t('content.reference')}</span>
      <Mono>{reference.kind}</Mono>
      <Mono>{reference.ref}</Mono>
      {reference.hash ? (
        <>
          <span className="text-muted-foreground">{t('content.hash')}</span>
          <Mono>{reference.hash}</Mono>
        </>
      ) : null}
    </span>
  )
}

function BlockView({ block, index }: { block: ContentBlock; index: number }) {
  const { t } = useTranslation('communications')
  const typeLabel = t(`blockType.${block.type}`, { defaultValue: block.type })
  return (
    <li
      data-slot="content-block"
      data-block-type={block.type}
      className="flex flex-col gap-1 rounded-md border border-border bg-surface p-3"
    >
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant="outline">
          {index + 1} · {typeLabel}
        </Badge>
        {block.type === 'text' && block.format === 'markdown' ? (
          <Badge variant="neutral">{t('content.markdownAsText')}</Badge>
        ) : null}
        {(block.type === 'status' || block.type === 'action_ref') &&
        block.code ? (
          <Badge variant="info">
            <Mono>{block.code}</Mono>
          </Badge>
        ) : null}
      </div>
      {block.text ? (
        <p className="whitespace-pre-wrap break-words font-sans text-body text-foreground">
          {block.text}
        </p>
      ) : block.type === 'text' ? (
        <p className="text-body text-muted-foreground">{t('content.empty')}</p>
      ) : null}
      {block.reference ? <ReferenceChip reference={block.reference} /> : null}
      {block.type === 'action_ref' ? (
        <p className="text-caption text-muted-foreground">
          {t('content.action')}
        </p>
      ) : null}
    </li>
  )
}

export function ContentView({ content }: { content: MessageContent }) {
  const { t } = useTranslation('communications')
  return (
    <section
      data-slot="message-content"
      className="flex flex-col gap-2"
      aria-label={t('content.title')}
    >
      <p className="break-words text-body font-medium text-foreground">
        {content.subject || t('content.empty')}
      </p>
      <ol className="flex flex-col gap-2">
        {content.blocks.map((block, i) => (
          <BlockView key={i} block={block} index={i} />
        ))}
      </ol>
      <p className="text-caption text-muted-foreground">{t('content.inert')}</p>
    </section>
  )
}

export function FulfillmentView({ fulfillment }: { fulfillment: Fulfillment }) {
  const { t } = useTranslation('communications')
  return (
    <KvList>
      <KvRow label={t('fulfillment.state')} mono>
        {fulfillment.state}
      </KvRow>
      <KvRow label={t('fulfillment.required')} mono>
        {fulfillment.required}
      </KvRow>
      <KvRow label={t('fulfillment.acknowledged')} mono>
        {fulfillment.acknowledged}
      </KvRow>
      <KvRow label={t('fulfillment.viable')} mono>
        {fulfillment.viable}
      </KvRow>
      <KvRow label={t('fulfillment.unmet')} mono>
        {fulfillment.unmet}
      </KvRow>
      {fulfillment.quorum !== undefined ? (
        <KvRow label={t('fulfillment.quorum')} mono>
          {fulfillment.quorum}
        </KvRow>
      ) : null}
    </KvList>
  )
}
