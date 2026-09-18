// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Plus, Trash2 } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { KvList, KvRow } from '@/components/ui/kv'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { AuthorityLostError } from '@/features/agentops/auth-boundary'
import { communicationsKeys, sendNotice, type SendOutcome } from './api'
import type { CommunicationsScope } from './boundary'
import { localToIso } from './channel-create-form'
import { FulfillmentView, Mono } from './content-blocks'
import { classifyFailure, type Failure } from './errors'
import { FailureNotice } from './failure-notice'
import { buildSendIntent, useIntentGuard, type SendIntent } from './intent'
import { SubjectPicker, type Me, type PickedSubject } from './subject-picker'
import {
  BLOCKS_MAX,
  CONTENT_BLOCK_TYPES,
  NOTICE_URGENCIES,
  TEXT_FORMATS,
  type ContentBlock,
  type RecipientKind,
  type SendNoticeInput,
} from './types'

interface BlockDraft {
  id: string
  type: ContentBlock['type']
  format: string
  text: string
  code: string
  refKind: string
  refRef: string
  refHash: string
}

let blockSeq = 0
const newBlock = (): BlockDraft => ({
  id: `b${++blockSeq}`,
  type: 'text',
  format: 'plain',
  text: '',
  code: '',
  refKind: '',
  refRef: '',
  refHash: '',
})

/** `<` followed by a letter, `/`, `!` or `?`: the engine's own definition of a raw HTML
 * tag in a markdown block (containsRawMarkdownHTML). */
const RAW_HTML_TAG = /<[A-Za-z/!?]/

type Phase =
  | 'edit'
  | 'confirm'
  | 'sending'
  | 'applied'
  | 'replayed'
  | 'ambiguous'
  | 'refused'

/**
 * ComposeDialog — edit, CONFIRM, send. The confirmation step builds the intention:
 * from then on the key, the body and the target are frozen and shown, the send and
 * any retry after an ambiguous transport result re-send exactly that object, and
 * editing again is only possible after the operator DISCARDS the intention, which
 * makes the next confirmation a new one with a new key. A refusal is definitive
 * and shown as the server phrased it; a replay is said to be a replay.
 */
export function ComposeDialog({
  open,
  onOpenChange,
  channel,
  scope,
  canSend,
  canUserRead,
  canAgentRead,
  me,
  onOutcome,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  channel: { id: string; name: string; slug: string }
  scope: CommunicationsScope
  canSend: boolean
  canUserRead: boolean
  canAgentRead: boolean
  me: Me
  onOutcome?: (outcome: SendOutcome) => void
}) {
  const { t } = useTranslation('communications')
  const queryClient = useQueryClient()
  const idp = useId()
  const workspace = scope.workspace ?? ''

  const [recipient, setRecipient] = useState<PickedSubject>({
    kind: 'user',
    ref: '',
  })
  const [subject, setSubject] = useState('')
  const [blocks, setBlocks] = useState<BlockDraft[]>(() => [newBlock()])
  const [urgency, setUrgency] = useState<string>('normal')
  const [availableAt, setAvailableAt] = useState('')
  const [errors, setErrors] = useState<string[]>([])
  const [phase, setPhase] = useState<Phase>('edit')
  const [intent, setIntent] = useState<SendIntent | null>(null)
  const [outcome, setOutcome] = useState<SendOutcome | null>(null)
  const [failure, setFailure] = useState<Failure | null>(null)
  const [lostCount, setLostCount] = useState(0)
  // There is no DialogTrigger. Capture the origin in Radix's opening event,
  // before its autofocus, and bind it to that content's closing lifecycle.
  const originRef = useRef<{
    content: HTMLElement
    element: HTMLElement | null
  } | null>(null)

  const guard = useIntentGuard({
    allowed: canSend,
    boundary: scope.key,
    permission: 'sessions:message-send:write',
  })

  // The permission left while a confirmation was open or a send in flight: the
  // intention is over before this render paints, and the request was aborted.
  if (!canSend && (phase === 'confirm' || phase === 'sending')) {
    setPhase('edit')
    setIntent(null)
    setLostCount((n) => n + 1)
  }
  useEffect(() => {
    if (lostCount > 0) toast.warning(t('authority.confirmationClosed'))
  }, [lostCount, t])

  const send = useMutation<SendOutcome, unknown, SendIntent>({
    mutationFn: (i) => {
      const signal = guard.begin()
      if (!signal) throw new AuthorityLostError()
      return sendNotice(
        i,
        { tenant: i.scope.tenant, guard: guard.check },
        signal,
      )
    },
    onSuccess: (o) => {
      setOutcome(o)
      setPhase(o.replayed ? 'replayed' : 'applied')
      onOutcome?.(o)
      void queryClient.invalidateQueries({
        queryKey: communicationsKeys.workspaceScope(
          scope.tenant,
          scope.epoch,
          workspace,
        ),
      })
    },
    onError: (err) => {
      if (err instanceof AuthorityLostError) {
        setPhase('edit')
        setIntent(null)
        setLostCount((n) => n + 1)
        return
      }
      const f = classifyFailure(err)
      if (f.kind === 'aborted') {
        setPhase('confirm')
        return
      }
      setFailure(f)
      setPhase(f.kind === 'ambiguous' ? 'ambiguous' : 'refused')
    },
  })

  /**
   * Refuses the intents the ENGINE is known to refuse (CanonicalMessageContent,
   * modules/sessions/communication_state.go): a subject of 1..256 bytes; a text
   * block with a format and text, and no raw HTML tag under markdown; a reference or
   * action-reference block that is ONLY its reference (kind, ref, optional hash); a
   * status block that is ONLY a token code of at most 128 characters. Everything the
   * engine may still refuse (eligibility, grants, limits) is its answer, shown verbatim.
   */
  const validate = (): SendNoticeInput | null => {
    const problems: string[] = []
    if (recipient.ref.trim() === '')
      problems.push(t('compose.validation.recipient'))
    const subjectValue = subject.trim()
    if (subjectValue === '') problems.push(t('compose.validation.subject'))
    else if (new TextEncoder().encode(subjectValue).length > 256)
      problems.push(t('compose.validation.subjectLength'))
    if (blocks.length === 0) problems.push(t('compose.validation.blocks'))
    const out: ContentBlock[] = []
    for (const b of blocks) {
      if (b.type === 'text') {
        if (b.text.trim() === '') problems.push(t('compose.validation.text'))
        if (b.format === 'markdown' && RAW_HTML_TAG.test(b.text))
          problems.push(t('compose.validation.markdownHtml'))
        out.push({
          type: 'text',
          format: (b.format || 'plain') as ContentBlock['format'],
          text: b.text,
        })
      } else if (b.type === 'reference' || b.type === 'action_ref') {
        if (b.refKind.trim() === '' || b.refRef.trim() === '')
          problems.push(t('compose.validation.reference'))
        const block: ContentBlock = {
          type: b.type,
          reference: { kind: b.refKind.trim(), ref: b.refRef.trim() },
        }
        if (b.refHash.trim() !== '' && block.reference)
          block.reference.hash = b.refHash.trim()
        out.push(block)
      } else {
        const code = b.code.trim()
        if (code === '' || code.length > 128 || /\s/.test(code))
          problems.push(t('compose.validation.code'))
        out.push({ type: 'status', code })
      }
    }
    let available: string | null = null
    if (availableAt !== '') {
      available = localToIso(availableAt)
      if (!available) problems.push(t('compose.validation.availableAt'))
    }
    setErrors([...new Set(problems)])
    if (problems.length > 0) return null
    const body: SendNoticeInput = {
      channel_id: channel.id,
      recipient: {
        kind: recipient.kind as RecipientKind,
        ref: recipient.ref.trim(),
      },
      content: { subject: subjectValue, blocks: out },
    }
    if (urgency) body.urgency = urgency as SendNoticeInput['urgency']
    if (available) body.available_at = available
    return body
  }

  const onConfirm = () => {
    if (!canSend || !workspace) return
    const body = validate()
    if (!body) return
    setIntent(
      buildSendIntent(
        { tenant: scope.tenant, workspace, boundary: scope.key },
        channel.id,
        body,
      ),
    )
    setFailure(null)
    setOutcome(null)
    setPhase('confirm')
  }
  const onSend = () => {
    if (!intent || !canSend) return
    setPhase('sending')
    send.mutate(intent)
  }
  const onDiscard = () => {
    guard.end()
    setIntent(null)
    setFailure(null)
    setPhase('edit')
  }
  const close = (o: boolean) => {
    if (phase === 'sending') return
    onOpenChange(o)
  }

  const updateBlock = (id: string, patch: Partial<BlockDraft>) =>
    setBlocks((bs) => bs.map((b) => (b.id === id ? { ...b, ...patch } : b)))

  const editing = phase === 'edit'

  return (
    <Dialog open={open} onOpenChange={close}>
      {/* Bounded height, local body scroll. DialogContent is a centred panel
          with no ceiling: header + max-h-[70vh] body + footer measured 710.5px
          on a 390×640 viewport (title y=-10, Cancel bottom 650). */}
      <DialogContent
        className="flex max-h-[85vh] max-w-2xl min-h-0 flex-col overflow-hidden"
        data-slot="compose-dialog"
        onOpenAutoFocus={(event) => {
          const content = event.currentTarget
          if (!(content instanceof HTMLElement)) return
          const element = content.ownerDocument.activeElement
          originRef.current = {
            content,
            element:
              element instanceof HTMLElement &&
              element !== content.ownerDocument.body
                ? element
                : null,
          }
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          const origin = originRef.current
          // Radix defers this event: ignore StrictMode effect cleanup while
          // connected, and an old content's close after a new opening.
          if (
            !origin ||
            origin.content !== event.currentTarget ||
            origin.content.isConnected
          )
            return
          originRef.current = null
          if (origin.element?.isConnected) origin.element.focus()
        }}
      >
        <DialogHeader className="shrink-0 pr-8">
          <DialogTitle>{t('compose.title')}</DialogTitle>
          <DialogDescription>
            {t('compose.description', { channel: channel.name })}
          </DialogDescription>
        </DialogHeader>

        {editing ? (
          <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-x-hidden overflow-y-auto pr-1">
            {errors.length > 0 ? (
              <ul role="alert" className="list-disc pl-5 text-body text-danger">
                {errors.map((e) => (
                  <li key={e}>{e}</li>
                ))}
              </ul>
            ) : null}
            <fieldset className="flex flex-col gap-2">
              <legend className="text-body font-medium">
                {t('compose.recipient')}
              </legend>
              <SubjectPicker
                mode="recipient"
                value={recipient}
                onChange={setRecipient}
                scope={scope}
                canUserRead={canUserRead}
                canAgentRead={canAgentRead}
                me={me}
                idPrefix={`${idp}-recipient`}
              />
            </fieldset>
            <Field
              label={t('compose.subjectField')}
              htmlFor={`${idp}-subject`}
              required
            >
              <Input
                id={`${idp}-subject`}
                value={subject}
                onChange={(e) => setSubject(e.target.value)}
                autoComplete="off"
              />
            </Field>
            <fieldset className="flex flex-col gap-3">
              <legend className="text-body font-medium">
                {t('compose.blocks')}
              </legend>
              {blocks.map((b, i) => (
                <div
                  key={b.id}
                  className="flex flex-col gap-2 rounded-md border border-border bg-surface p-3"
                  data-slot="block-draft"
                >
                  <div className="grid gap-2 sm:grid-cols-2">
                    <Field
                      label={t('compose.block.type')}
                      htmlFor={`${idp}-b-${b.id}-type`}
                    >
                      <Select
                        value={b.type}
                        onValueChange={(v) =>
                          updateBlock(b.id, {
                            type: v as ContentBlock['type'],
                          })
                        }
                      >
                        <SelectTrigger
                          id={`${idp}-b-${b.id}-type`}
                          aria-label={t('compose.block.type')}
                        >
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {CONTENT_BLOCK_TYPES.map((k) => (
                            <SelectItem key={k} value={k}>
                              {t(`blockType.${k}`)}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </Field>
                    {b.type === 'text' ? (
                      <Field
                        label={t('compose.block.format')}
                        htmlFor={`${idp}-b-${b.id}-format`}
                      >
                        <Select
                          value={b.format}
                          onValueChange={(v) =>
                            updateBlock(b.id, { format: v })
                          }
                        >
                          <SelectTrigger
                            id={`${idp}-b-${b.id}-format`}
                            aria-label={t('compose.block.format')}
                          >
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {TEXT_FORMATS.map((k) => (
                              <SelectItem key={k} value={k}>
                                {t(`format.${k}`)}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      </Field>
                    ) : null}
                    {b.type === 'status' ? (
                      <Field
                        label={t('compose.block.code')}
                        htmlFor={`${idp}-b-${b.id}-code`}
                        description={t('compose.block.codeHint')}
                      >
                        <Input
                          id={`${idp}-b-${b.id}-code`}
                          value={b.code}
                          onChange={(e) =>
                            updateBlock(b.id, { code: e.target.value })
                          }
                          autoComplete="off"
                          mono
                        />
                      </Field>
                    ) : null}
                  </div>
                  {b.type === 'text' ? (
                    <Field
                      label={t('compose.block.text')}
                      htmlFor={`${idp}-b-${b.id}-text`}
                      description={
                        b.format === 'markdown'
                          ? t('compose.block.markdownHint')
                          : undefined
                      }
                    >
                      <Textarea
                        id={`${idp}-b-${b.id}-text`}
                        value={b.text}
                        onChange={(e) =>
                          updateBlock(b.id, { text: e.target.value })
                        }
                        rows={4}
                      />
                    </Field>
                  ) : null}
                  {b.type === 'reference' || b.type === 'action_ref' ? (
                    <div className="grid gap-2 sm:grid-cols-3">
                      <Field
                        label={t('compose.block.referenceKind')}
                        htmlFor={`${idp}-b-${b.id}-rk`}
                      >
                        <Input
                          id={`${idp}-b-${b.id}-rk`}
                          value={b.refKind}
                          onChange={(e) =>
                            updateBlock(b.id, { refKind: e.target.value })
                          }
                          autoComplete="off"
                          mono
                        />
                      </Field>
                      <Field
                        label={t('compose.block.referenceRef')}
                        htmlFor={`${idp}-b-${b.id}-rr`}
                      >
                        <Input
                          id={`${idp}-b-${b.id}-rr`}
                          value={b.refRef}
                          onChange={(e) =>
                            updateBlock(b.id, { refRef: e.target.value })
                          }
                          autoComplete="off"
                          mono
                        />
                      </Field>
                      <Field
                        label={t('compose.block.referenceHash')}
                        htmlFor={`${idp}-b-${b.id}-rh`}
                        description={t('compose.block.referenceHint')}
                      >
                        <Input
                          id={`${idp}-b-${b.id}-rh`}
                          value={b.refHash}
                          onChange={(e) =>
                            updateBlock(b.id, { refHash: e.target.value })
                          }
                          autoComplete="off"
                          mono
                        />
                      </Field>
                    </div>
                  ) : null}
                  <div>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      aria-label={`${t('actions.removeBlock')} ${i + 1}`}
                      disabled={blocks.length <= 1}
                      onClick={() =>
                        setBlocks((bs) => bs.filter((x) => x.id !== b.id))
                      }
                    >
                      <Trash2 className="size-4" aria-hidden="true" />
                      {t('actions.removeBlock')}
                    </Button>
                  </div>
                </div>
              ))}
              <div>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={blocks.length >= BLOCKS_MAX}
                  onClick={() => setBlocks((bs) => [...bs, newBlock()])}
                >
                  <Plus className="size-4" aria-hidden="true" />
                  {t('actions.addBlock')}
                </Button>
              </div>
            </fieldset>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={t('compose.urgency')} htmlFor={`${idp}-urgency`}>
                <Select value={urgency} onValueChange={setUrgency}>
                  <SelectTrigger
                    id={`${idp}-urgency`}
                    aria-label={t('compose.urgency')}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {NOTICE_URGENCIES.map((u) => (
                      <SelectItem key={u} value={u}>
                        {t(`urgency.${u}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field
                label={t('compose.availableAt')}
                htmlFor={`${idp}-available`}
                description={t('compose.availableAtHint')}
              >
                <Input
                  id={`${idp}-available`}
                  type="datetime-local"
                  value={availableAt}
                  onChange={(e) => setAvailableAt(e.target.value)}
                />
              </Field>
            </div>
          </div>
        ) : null}

        {intent && !editing ? (
          <div
            className="flex min-h-0 flex-1 flex-col gap-3 overflow-x-hidden overflow-y-auto pr-1"
            data-slot="send-intent"
          >
            {phase === 'confirm' || phase === 'sending' ? (
              <div className="rounded-md border border-border bg-muted px-3 py-2 text-body">
                <p className="font-medium">{t('compose.confirm.title')}</p>
                <p className="text-muted-foreground">
                  {t('compose.confirm.body')}
                </p>
              </div>
            ) : null}
            {phase === 'applied' && outcome ? (
              <div
                role="status"
                className="rounded-md border border-success-line bg-success-soft px-3 py-2 text-body text-success"
              >
                <p className="font-medium">
                  {t('compose.result.appliedTitle')}
                </p>
                <p>{t('compose.result.appliedBody')}</p>
              </div>
            ) : null}
            {phase === 'replayed' && outcome ? (
              <div
                role="status"
                className="rounded-md border border-info-line bg-info-soft px-3 py-2 text-body text-info"
              >
                <p className="font-medium">
                  {t('compose.result.replayedTitle')}
                </p>
                <p>{t('compose.result.replayedBody')}</p>
              </div>
            ) : null}
            {phase === 'ambiguous' && failure ? (
              <FailureNotice
                failure={failure}
                title={t('compose.result.ambiguousTitle')}
              />
            ) : null}
            {phase === 'ambiguous' ? (
              <p className="text-body text-muted-foreground">
                {t('compose.result.ambiguousBody')}
              </p>
            ) : null}
            {phase === 'refused' && failure ? (
              <>
                <FailureNotice
                  failure={failure}
                  title={t('compose.result.refusedTitle')}
                />
                <p className="text-body text-muted-foreground">
                  {t('compose.result.refusedBody')}
                </p>
              </>
            ) : null}
            <KvList>
              <KvRow label={t('compose.confirm.key')} mono>
                <span className="break-all">{intent.key}</span>
              </KvRow>
              <KvRow label={t('compose.confirm.channel')} mono>
                <span className="break-all">
                  {channel.slug} · {intent.channelId}
                </span>
              </KvRow>
              <KvRow label={t('compose.confirm.recipient')} mono>
                <span className="break-all">
                  {intent.body.recipient.kind}:{intent.body.recipient.ref}
                </span>
              </KvRow>
              <KvRow label={t('compose.confirm.subject')} align="start">
                <span className="break-words">
                  {intent.body.content.subject}
                </span>
              </KvRow>
              <KvRow label={t('compose.confirm.blocksCount')} mono>
                {intent.body.content.blocks.length}
              </KvRow>
              <KvRow label={t('compose.confirm.urgency')}>
                {t(`urgency.${intent.body.urgency ?? ''}`)}
              </KvRow>
              {intent.body.available_at ? (
                <KvRow label={t('compose.confirm.availableAt')} mono>
                  {intent.body.available_at}
                </KvRow>
              ) : null}
            </KvList>
            {outcome ? (
              <section
                aria-label={t('receipt.title')}
                className="flex flex-col gap-2"
                data-slot="send-receipt"
              >
                <div className="flex flex-wrap gap-1.5">
                  <Badge variant={outcome.replayed ? 'info' : 'success'}>
                    {outcome.replayed
                      ? t('compose.result.replayedTitle')
                      : t('compose.result.appliedTitle')}
                  </Badge>
                  <Badge variant="outline">
                    <Mono>{outcome.result.verdict}</Mono>
                  </Badge>
                  <Badge variant="outline">
                    <Mono>{outcome.result.code}</Mono>
                  </Badge>
                </div>
                <KvList>
                  <KvRow label={t('receipt.messageId')} mono>
                    <span className="break-all">
                      {outcome.result.message_id}
                    </span>
                  </KvRow>
                  <KvRow label={t('receipt.deliveryId')} mono>
                    <span className="break-all">
                      {outcome.result.delivery_id}
                    </span>
                  </KvRow>
                  <KvRow label={t('receipt.commandId')} mono>
                    <span className="break-all">
                      {outcome.result.command_id}
                    </span>
                  </KvRow>
                  <KvRow label={t('receipt.eventId')} mono>
                    <span className="break-all">{outcome.result.event_id}</span>
                  </KvRow>
                  <KvRow label={t('receipt.state')} mono>
                    {outcome.result.state}
                  </KvRow>
                  <KvRow label={t('receipt.version')} mono>
                    {outcome.result.version}
                  </KvRow>
                  <KvRow label={t('receipt.deliveryCount')} mono>
                    {outcome.result.delivery_count}
                  </KvRow>
                  <KvRow label={t('receipt.requiredCount')} mono>
                    {outcome.result.required_count}
                  </KvRow>
                  <KvRow label={t('receipt.auditSeq')} mono>
                    {outcome.result.audit_seq}
                  </KvRow>
                  <KvRow label={t('receipt.payloadDigest')} mono>
                    <span className="break-all">
                      {outcome.result.payload_digest}
                    </span>
                  </KvRow>
                </KvList>
                <FulfillmentView fulfillment={outcome.result.fulfillment} />
              </section>
            ) : null}
          </div>
        ) : null}

        <DialogFooter className="shrink-0">
          {editing ? (
            <>
              <Button
                type="button"
                variant="secondary"
                onClick={() => close(false)}
              >
                {t('actions.cancel')}
              </Button>
              <Button
                type="button"
                variant="primary"
                disabled={!canSend}
                onClick={onConfirm}
              >
                {t('actions.compose')}
              </Button>
            </>
          ) : null}
          {phase === 'confirm' || phase === 'sending' ? (
            <>
              <Button
                type="button"
                variant="secondary"
                disabled={phase === 'sending'}
                onClick={onDiscard}
              >
                {t('actions.discardAndEdit')}
              </Button>
              <Button
                type="button"
                variant="primary"
                disabled={phase === 'sending' || !canSend}
                onClick={onSend}
              >
                {phase === 'sending' ? <Spinner className="size-3.5" /> : null}
                {phase === 'sending'
                  ? t('actions.sending')
                  : t('actions.confirm')}
              </Button>
            </>
          ) : null}
          {phase === 'ambiguous' ? (
            <>
              <Button type="button" variant="secondary" onClick={onDiscard}>
                {t('actions.discardAndEdit')}
              </Button>
              <Button
                type="button"
                variant="primary"
                disabled={!canSend}
                onClick={onSend}
              >
                {t('actions.retrySameKey')}
              </Button>
            </>
          ) : null}
          {phase === 'refused' ? (
            <>
              <Button type="button" variant="secondary" onClick={onDiscard}>
                {t('actions.discardAndEdit')}
              </Button>
              <Button
                type="button"
                variant="primary"
                onClick={() => close(false)}
              >
                {t('actions.close')}
              </Button>
            </>
          ) : null}
          {phase === 'applied' || phase === 'replayed' ? (
            <Button
              type="button"
              variant="primary"
              onClick={() => close(false)}
            >
              {t('actions.close')}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
