// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useCallback, useId, useRef, useState } from 'react'
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
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import {
  communicationsKeys,
  listChannels,
  type HandoffOfferOutcome,
} from './api'
import type { CommunicationsScope } from './boundary'
import { Mono } from './content-blocks'
import type { Failure } from './errors'
import { FailureNotice } from './failure-notice'
import { localZoneLabel, readDeadline } from './handoff-deadline'
import type { HandoffWorkItemView } from './handoff-offer-host'
import type { HandoffOperation } from './handoff-operation'
import {
  collectReferences,
  type ReferenceDraft,
} from './handoff-reference-drafts'
import { ReferenceEditor } from './handoff-references'
import { buildHandoffOfferIntent, type HandoffOfferIntent } from './intent'
import { useOpenerFocus } from './handoff-opener-focus'
import { SubjectPicker, type Me, type PickedSubject } from './subject-picker'
import {
  HANDOFF_ARTIFACT_REFS_MAX,
  HANDOFF_RECIPIENT_REF_MAX_BYTES,
  HANDOFF_TEXT_MAX_BYTES,
  PAGE_LIMIT_DEFAULT,
  type HandoffOfferInput,
  type HandoffRecipientKind,
} from './types'

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const bytes = (value: string) => new TextEncoder().encode(value).length

/**
 * The offer, from a fresh WorkItem read to a receipt.
 *
 * It owns the draft and nothing else: submitting, uncertain and confirmed belong
 * to the host above it, which is what lets this panel be hidden without turning a
 * transmitted request back into an editable form. Every phase it paints arrives
 * through `operation.state`.
 *
 * Three refusals are deliberate. It never selects a workspace — the offer is
 * workspace-scoped while the work list is tenant-wide, so an item outside the
 * selected workspace names the workspace to select and disables confirmation. It
 * never invents a precondition: without the server's WorkItem ETag there is no
 * `If-Match` and no offer. It never calls a 201 a transfer: the receipt says an
 * offer exists and a response is pending.
 */
export function HandoffOfferDialog({
  open,
  onOpenChange,
  scope,
  canOffer,
  canChannelRead,
  canUserRead,
  canAgentRead,
  me,
  requestedItemId,
  view,
  readStatus,
  readFailure,
  onReread,
  onStartOver,
  onStopTracking,
  stoppedAfterSend = false,
  operation,
  targetRefused = false,
  getOpener,
  getFallbackFocus,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  scope: CommunicationsScope
  canOffer: boolean
  canChannelRead: boolean
  canUserRead: boolean
  canAgentRead: boolean
  me: Me
  requestedItemId: string | null
  view: HandoffWorkItemView | null
  readStatus: 'idle' | 'loading' | 'ready' | 'forbidden' | 'error'
  readFailure: Failure | null
  onReread: () => void
  /** Reset the controller and reread the item: conflict recovery is an observation. */
  onStartOver: () => void
  /**
   * Explicit stop-tracking. The host ends the operation and starts a new
   * invocation, so the draft and the snapshot this command was reviewed against
   * are both replaced by a fresh read.
   */
  onStopTracking: () => void
  /** Tracking was stopped for a command that may already have been sent. */
  stoppedAfterSend?: boolean
  operation: HandoffOperation<HandoffOfferIntent, HandoffOfferOutcome>
  /** A newer target arrived while a transmitted command was still unresolved. */
  targetRefused?: boolean
  getOpener?: () => HTMLElement | null
  getFallbackFocus?: () => HTMLElement | null
}) {
  const { t, i18n } = useTranslation('communications')
  const idp = useId()
  const noOpener = useCallback(() => null, [])
  const opener = useOpenerFocus({
    open,
    getOpener: getOpener ?? noOpener,
    getFallback: getFallbackFocus,
  })
  const workspace = scope.workspace ?? ''

  const [channelId, setChannelId] = useState('')
  const [recipient, setRecipient] = useState<PickedSubject>({
    kind: 'user',
    ref: '',
  })
  const [summary, setSummary] = useState('')
  const [nextAction, setNextAction] = useState('')
  const [risk, setRisk] = useState('')
  const [references, setReferences] = useState<ReferenceDraft[]>([])
  // EMPTY on purpose: a seeded deadline is a commitment the console chose.
  const [deadline, setDeadline] = useState('')
  const [problems, setProblems] = useState<string[]>([])
  /** Per-field messages, so a required input is marked as well as summarised. */
  const [fieldErrors, setFieldErrors] = useState<{
    channel?: string
    recipient?: string
    summary?: string
    nextAction?: string
    risk?: string
    references?: string
    deadline?: string
  }>({})
  // The instant the draft is judged against is taken WHEN the operator confirms, so
  // an open dialog does not carry a stale "now" into a much later confirmation.
  const nowRef = useRef<() => number>(() => Date.now())

  const phase = operation.state.phase
  const editing = phase === 'idle'
  const busy = phase === 'submitting'

  /* The Channel catalog is offered when this principal can read it, and a Channel id
   * can ALWAYS be typed. A directory permission is not a prerequisite of a permitted
   * write, and a listed Channel is a candidate, never an eligibility decision. */
  const channels = useQuery({
    queryKey: communicationsKeys.catalog(scope.tenant, scope.epoch, workspace, {
      limit: PAGE_LIMIT_DEFAULT,
    }),
    queryFn: ({ signal }) =>
      listChannels(
        { workspace_id: workspace, limit: PAGE_LIMIT_DEFAULT },
        { tenant: scope.tenant },
        signal,
      ),
    enabled: open && canChannelRead && workspace !== '',
  })

  const itemMismatch =
    view !== null &&
    requestedItemId !== null &&
    view.item.id !== requestedItemId
  const workspaceMismatch =
    view !== null && (workspace === '' || view.item.workspace_id !== workspace)
  const noPrecondition = view !== null && !view.etag
  const blocked =
    !canOffer ||
    view === null ||
    itemMismatch ||
    workspaceMismatch ||
    noPrecondition

  const validate = (): HandoffOfferInput | null => {
    if (!view || !view.etag || blocked) return null
    const found: string[] = []
    const fields: Record<string, string> = {}
    const note = (field: string, message: string) => {
      found.push(message)
      if (!fields[field]) fields[field] = message
    }
    const channel = channelId.trim()
    if (channel === '') note('channel', t('handoff.offer.validation.channel'))
    else if (!UUID.test(channel))
      note('channel', t('handoff.offer.validation.channelFormat'))
    const ref = recipient.ref.trim()
    if (ref === '') note('recipient', t('handoff.offer.validation.recipient'))
    else if (bytes(ref) > HANDOFF_RECIPIENT_REF_MAX_BYTES)
      note('recipient', t('handoff.offer.validation.recipientLength'))
    const summaryValue = summary.trim()
    if (summaryValue === '')
      note('summary', t('handoff.offer.validation.summary'))
    else if (bytes(summaryValue) > HANDOFF_TEXT_MAX_BYTES)
      note('summary', t('handoff.offer.validation.summaryLength'))
    const nextValue = nextAction.trim()
    if (nextValue === '')
      note('nextAction', t('handoff.offer.validation.nextAction'))
    else if (bytes(nextValue) > HANDOFF_TEXT_MAX_BYTES)
      note('nextAction', t('handoff.offer.validation.nextActionLength'))
    const riskValue = risk.trim()
    if (riskValue !== '' && bytes(riskValue) > HANDOFF_TEXT_MAX_BYTES)
      note('risk', t('handoff.offer.validation.riskLength'))
    const { refs, invalid } = collectReferences(references)
    if (invalid) note('references', t('handoff.offer.validation.references'))
    if (refs.length > HANDOFF_ARTIFACT_REFS_MAX)
      note('references', t('handoff.offer.validation.referencesMax'))
    const when = readDeadline(deadline, nowRef.current())
    if (!when.ok)
      note('deadline', t(`handoff.offer.validation.deadline.${when.problem}`))

    setProblems([...new Set(found)])
    setFieldErrors(fields)
    if (found.length > 0 || !when.ok) return null

    const body: HandoffOfferInput = {
      channel_id: channel,
      work_item_id: view.item.id,
      recipient: { kind: recipient.kind as HandoffRecipientKind, ref },
      handoff: { summary: summaryValue, next_action: nextValue },
      ack_deadline: when.reading.utc,
      // The epoch of the SAME read the ETag came from. Sent explicitly so the engine
      // refuses an item whose ownership moved since the operator looked at it.
      expected_owner_epoch: view.item.owner_epoch,
    }
    if (riskValue !== '') body.handoff.risk = riskValue
    if (refs.length > 0) body.handoff.artifact_refs = refs
    return body
  }

  const onReview = () => {
    const body = validate()
    if (!body || !view?.etag || !workspace) return
    operation.review(
      buildHandoffOfferIntent(
        { tenant: scope.tenant, workspace, boundary: scope.key },
        view.item.id,
        view.etag,
        body,
      ),
    )
  }

  const reviewed =
    operation.state.phase === 'reviewing' ||
    operation.state.phase === 'submitting' ||
    operation.state.phase === 'confirmed' ||
    operation.state.phase === 'uncertain'
      ? operation.state.intent
      : null
  const failure =
    operation.state.phase === 'refused' ||
    operation.state.phase === 'conflict' ||
    operation.state.phase === 'uncertain'
      ? operation.state.failure
      : null

  const deadlineNow = readDeadline(deadline, 0)

  return (
    // Hiding the panel while the command is pending is safe: the host keeps
    // tracking it and the outcome classification does not change.
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="flex max-h-[85vh] w-full flex-col gap-4 sm:max-w-2xl"
        onCloseAutoFocus={opener.onCloseAutoFocus}
      >
        <DialogHeader>
          <DialogTitle>{t('handoff.offer.title')}</DialogTitle>
          <DialogDescription>
            {t('handoff.offer.description')}
          </DialogDescription>
        </DialogHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto pr-1">
          {readStatus === 'loading' || readStatus === 'idle' ? (
            <div role="status" aria-busy="true">
              <span className="sr-only">{t('states.reading')}</span>
              <Skeleton className="h-24 w-full" />
            </div>
          ) : null}
          {readStatus === 'forbidden' ? (
            <FailureNotice
              failure={{ kind: 'forbidden' }}
              title={t('handoff.offer.readRefusedTitle')}
            />
          ) : null}
          {readStatus === 'error' ? (
            <FailureNotice
              failure={{ kind: 'other' }}
              title={t('handoff.offer.readRefusedTitle')}
            />
          ) : null}
          {readFailure ? (
            <FailureNotice
              failure={readFailure}
              title={t('handoff.offer.readRefusedTitle')}
            />
          ) : null}

          {view ? (
            <section
              aria-label={t('handoff.offer.itemTitle')}
              data-slot="handoff-offer-item"
            >
              <KvList>
                <KvRow label={t('handoff.offer.item')}>{view.item.title}</KvRow>
                <KvRow label={t('handoff.offer.itemId')} mono>
                  {view.item.id}
                </KvRow>
                <KvRow label={t('handoff.offer.itemStatus')} mono>
                  {view.item.status}
                </KvRow>
                <KvRow label={t('handoff.offer.currentOwner')} mono>
                  {view.item.owner_kind}:{view.item.owner_ref}
                </KvRow>
                <KvRow label={t('handoff.offer.ownerEpoch')} mono>
                  {view.item.owner_epoch}
                </KvRow>
                <KvRow label={t('handoff.offer.itemWorkspace')} mono>
                  {view.item.workspace_id}
                </KvRow>
                <KvRow label={t('handoff.offer.precondition')} mono>
                  {view.etag ?? '—'}
                </KvRow>
              </KvList>
            </section>
          ) : null}

          {itemMismatch ? (
            <div
              role="alert"
              className="rounded-md border border-danger-line bg-danger-soft px-3 py-2 text-body text-danger"
              data-slot="handoff-offer-item-mismatch"
            >
              {t('handoff.offer.itemMismatch')}
            </div>
          ) : null}
          {workspaceMismatch ? (
            <div
              role="alert"
              className="rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-body text-warning"
              data-slot="handoff-offer-workspace-mismatch"
            >
              <p className="font-medium">
                {t('handoff.offer.workspaceMismatchTitle')}
              </p>
              <p>
                {workspace === ''
                  ? t('handoff.offer.workspaceNone')
                  : t('handoff.offer.workspaceMismatchBody', {
                      itemWorkspace: view?.item.workspace_id ?? '',
                      selected: scope.workspaceName || workspace,
                    })}
              </p>
            </div>
          ) : null}
          {noPrecondition ? (
            <div
              role="alert"
              className="rounded-md border border-danger-line bg-danger-soft px-3 py-2 text-body text-danger"
              data-slot="handoff-offer-no-precondition"
            >
              {t('handoff.offer.noPrecondition')}
            </div>
          ) : null}
          {!canOffer ? (
            <p className="text-caption text-muted-foreground">
              {t('handoff.offer.noPermission')}
            </p>
          ) : null}

          {editing && !blocked ? (
            <div className="flex flex-col gap-3" data-slot="handoff-offer-form">
              <Field
                label={t('handoff.offer.channel')}
                htmlFor={`${idp}-channel`}
                description={t('handoff.offer.channelHint')}
                error={fieldErrors.channel}
              >
                <Input
                  id={`${idp}-channel`}
                  aria-required="true"
                  value={channelId}
                  onChange={(e) => setChannelId(e.target.value)}
                  autoComplete="off"
                  mono
                />
              </Field>
              {canChannelRead ? (
                <Field
                  label={t('handoff.offer.channelPick')}
                  htmlFor={`${idp}-channel-pick`}
                >
                  <Select
                    value={channelId}
                    onValueChange={setChannelId}
                    disabled={!channels.data}
                  >
                    <SelectTrigger
                      id={`${idp}-channel-pick`}
                      aria-label={t('handoff.offer.channelPick')}
                    >
                      <SelectValue
                        placeholder={
                          channels.isLoading
                            ? t('subject.loading')
                            : t('handoff.offer.channelPick')
                        }
                      />
                    </SelectTrigger>
                    <SelectContent>
                      {(channels.data?.items ?? []).map((c) => (
                        <SelectItem key={c.id} value={c.id}>
                          {c.name} · {c.slug}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              ) : (
                <p className="text-caption text-muted-foreground">
                  {t('handoff.offer.channelNoDirectory')}
                </p>
              )}

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

              <Field
                label={t('handoff.offer.summary')}
                htmlFor={`${idp}-summary`}
                error={fieldErrors.summary}
              >
                <Textarea
                  id={`${idp}-summary`}
                  aria-required="true"
                  value={summary}
                  onChange={(e) => setSummary(e.target.value)}
                  rows={3}
                />
              </Field>
              <Field
                label={t('handoff.offer.nextAction')}
                htmlFor={`${idp}-next`}
                error={fieldErrors.nextAction}
              >
                <Textarea
                  id={`${idp}-next`}
                  aria-required="true"
                  value={nextAction}
                  onChange={(e) => setNextAction(e.target.value)}
                  rows={2}
                />
              </Field>
              <Field
                label={t('handoff.offer.risk')}
                htmlFor={`${idp}-risk`}
                description={t('handoff.offer.riskHint')}
                error={fieldErrors.risk}
              >
                <Textarea
                  id={`${idp}-risk`}
                  value={risk}
                  onChange={(e) => setRisk(e.target.value)}
                  rows={2}
                />
              </Field>

              <ReferenceEditor
                drafts={references}
                onChange={setReferences}
                idPrefix={`${idp}-artifact`}
                max={HANDOFF_ARTIFACT_REFS_MAX}
                label={t('handoff.offer.artifacts')}
                error={fieldErrors.references}
              />

              <Field
                label={t('handoff.offer.deadline')}
                htmlFor={`${idp}-deadline`}
                description={t('handoff.offer.deadlineHint')}
                error={fieldErrors.deadline}
              >
                <Input
                  id={`${idp}-deadline`}
                  aria-required="true"
                  type="datetime-local"
                  step={1}
                  value={deadline}
                  onChange={(e) => setDeadline(e.target.value)}
                />
              </Field>
              {deadlineNow.ok ? (
                <p
                  className="text-caption text-muted-foreground"
                  data-slot="handoff-offer-deadline-review"
                >
                  {t('handoff.offer.deadlineReview', {
                    zone: localZoneLabel(deadlineNow.reading.at),
                    utc: deadlineNow.reading.utc,
                  })}
                </p>
              ) : null}

              {problems.length > 0 ? (
                <ul
                  role="alert"
                  data-testid="offer-problems"
                  className="flex flex-col gap-1 rounded-md border border-danger-line bg-danger-soft px-3 py-2 text-body text-danger"
                  data-slot="handoff-offer-problems"
                >
                  {problems.map((p) => (
                    <li key={p}>{p}</li>
                  ))}
                </ul>
              ) : null}
            </div>
          ) : null}

          {stoppedAfterSend ? (
            <div
              role="status"
              className="rounded-md border border-border bg-muted/30 px-3 py-2 text-body text-muted-foreground"
              data-slot="handoff-offer-stopped"
            >
              {t('handoff.tracking.stoppedAfterSend')}
            </div>
          ) : null}
          {targetRefused ? (
            <div
              role="alert"
              className="rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-body text-warning"
              data-slot="handoff-offer-target-refused"
            >
              <p className="font-medium">
                {t('handoff.offer.targetRefusedTitle')}
              </p>
              <p>{t('handoff.offer.targetRefusedBody')}</p>
            </div>
          ) : null}

          {/* An earlier attempt of this command ended without establishing its
              outcome. A later definitive answer describes the request that
              returned it and cannot settle that one. */}
          {operation.priorUnresolvedAttempt &&
          phase !== 'submitting' &&
          phase !== 'uncertain' ? (
            <div
              role="status"
              className="rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-body text-warning"
              data-slot="handoff-offer-prior-unresolved"
            >
              {t('handoff.uncertain.priorTransmission')}
            </div>
          ) : null}

          {reviewed ? (
            <section
              aria-label={t('handoff.offer.confirmTitle')}
              className="flex flex-col gap-2 rounded-md border border-border bg-muted p-3"
              data-slot="handoff-offer-intent"
            >
              <p className="text-body font-medium">
                {t('handoff.offer.confirmTitle')}
              </p>
              <p className="text-body text-muted-foreground">
                {t('handoff.offer.confirmBody')}
              </p>
              <KvList>
                <KvRow label={t('handoff.offer.item')} mono>
                  {reviewed.workItemId}
                </KvRow>
                <KvRow label={t('handoff.offer.recipient')} mono>
                  {reviewed.body.recipient.kind}:{reviewed.body.recipient.ref}
                </KvRow>
                <KvRow label={t('handoff.offer.channel')} mono>
                  {reviewed.body.channel_id}
                </KvRow>
                <KvRow label={t('handoff.offer.deadline')} mono>
                  {reviewed.body.ack_deadline}
                </KvRow>
                <KvRow label={t('handoff.offer.precondition')} mono>
                  {reviewed.etag}
                </KvRow>
                <KvRow label={t('handoff.offer.ownerEpoch')} mono>
                  {String(reviewed.body.expected_owner_epoch)}
                </KvRow>
                <KvRow label={t('handoff.offer.key')} mono>
                  {reviewed.key}
                </KvRow>
              </KvList>
            </section>
          ) : null}

          {operation.state.phase === 'lost' ? (
            <div
              role="alert"
              className="rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-body text-warning"
              data-slot="handoff-offer-lost"
            >
              <p className="font-medium">{t('handoff.lost.title')}</p>
              {/* The pre-dispatch guard refusal establishes that THIS attempt
                  sent no bytes. It says nothing about an earlier attempt whose
                  transmission was never established, so beside retained history
                  the sentence is scoped to the latest attempt. */}
              <p>
                {operation.state.sent
                  ? t('handoff.lost.body')
                  : operation.priorUnresolvedAttempt
                    ? t('handoff.lost.latestNotSent')
                    : t('handoff.lost.notSent')}
              </p>
            </div>
          ) : null}
          {phase === 'conflict' && failure ? (
            <div data-slot="handoff-offer-conflict">
              <FailureNotice
                failure={failure}
                title={
                  operation.priorUnresolvedAttempt
                    ? t('handoff.offer.conflictTitleLatest')
                    : t('handoff.offer.conflictTitle')
                }
              />
              <p className="mt-1 text-body text-muted-foreground">
                {operation.priorUnresolvedAttempt
                  ? t('handoff.offer.conflictBodyLatest')
                  : t('handoff.offer.conflictBody')}
              </p>
            </div>
          ) : null}
          {phase === 'uncertain' && failure ? (
            <div data-slot="handoff-offer-uncertain">
              <FailureNotice
                failure={failure}
                title={t('handoff.uncertain.title')}
              />
              <p className="mt-1 text-body text-muted-foreground">
                {t('handoff.uncertain.body')}
              </p>
            </div>
          ) : null}
          {phase === 'refused' && failure ? (
            <FailureNotice
              failure={failure}
              title={
                operation.priorUnresolvedAttempt
                  ? t('handoff.offer.refusedTitleLatest')
                  : t('handoff.offer.refusedTitle')
              }
            />
          ) : null}

          {operation.state.phase === 'confirmed' ? (
            <section
              aria-label={t('receipt.title')}
              className="flex flex-col gap-2"
              data-slot="handoff-offer-receipt"
            >
              <div
                role="status"
                className={
                  operation.state.outcome.replayed
                    ? 'rounded-md border border-info-line bg-info-soft px-3 py-2 text-body text-info'
                    : 'rounded-md border border-success-line bg-success-soft px-3 py-2 text-body text-success'
                }
              >
                <p className="font-medium">
                  {operation.state.outcome.replayed
                    ? t('handoff.offer.replayedTitle')
                    : t('handoff.offer.createdTitle')}
                </p>
                {/* The one sentence this receipt exists to keep honest. */}
                <p>{t('handoff.offer.pendingBody')}</p>
              </div>
              <KvList>
                <KvRow label={t('handoff.receipt.handoffId')} mono>
                  {operation.state.outcome.result.handoff_id}
                </KvRow>
                <KvRow label={t('handoff.receipt.deliveryId')} mono>
                  {operation.state.outcome.result.delivery_id}
                </KvRow>
                <KvRow label={t('receipt.commandId')} mono>
                  {operation.state.outcome.result.command_id}
                </KvRow>
                <KvRow label={t('receipt.state')} mono>
                  {operation.state.outcome.result.state}
                </KvRow>
                <KvRow label={t('receipt.etag')} mono>
                  {operation.state.outcome.etag ??
                    operation.state.outcome.result.etag}
                </KvRow>
                <KvRow label={t('receipt.auditSeq')} mono>
                  {operation.state.outcome.result.audit_seq}
                </KvRow>
              </KvList>
              <Badge variant="outline">
                <Mono>
                  {t('handoff.receipt.status', {
                    status: operation.state.outcome.status,
                  })}
                </Mono>
              </Badge>
            </section>
          ) : null}

          <p className="text-caption text-muted-foreground">
            {t('handoff.offer.serverFinal', { lang: i18n.language })}
          </p>
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={onReread}
            disabled={!requestedItemId || busy}
          >
            {t('actions.reread')}
          </Button>
          {editing ? (
            <Button
              type="button"
              variant="primary"
              onClick={onReview}
              disabled={blocked}
            >
              {t('handoff.offer.review')}
            </Button>
          ) : null}
          {phase === 'reviewing' ? (
            <>
              <Button
                type="button"
                variant="secondary"
                onClick={operation.discard}
              >
                {t('actions.discardAndEdit')}
              </Button>
              <Button
                type="button"
                variant="primary"
                onClick={operation.submit}
                disabled={!canOffer}
              >
                {t('handoff.offer.confirm')}
              </Button>
            </>
          ) : null}
          {busy ? (
            <Button type="button" variant="secondary" onClick={onStopTracking}>
              {t('handoff.uncertain.discard')}
            </Button>
          ) : null}
          {busy ? (
            <Button type="button" variant="primary" disabled>
              <Spinner className="size-3.5" />
              {t('handoff.offer.submitting')}
            </Button>
          ) : null}
          {phase === 'uncertain' ? (
            <>
              <Button
                type="button"
                variant="secondary"
                onClick={onStopTracking}
              >
                {t('handoff.uncertain.discard')}
              </Button>
              <Button
                type="button"
                variant="primary"
                onClick={operation.retrySame}
                disabled={!canOffer}
              >
                {t('actions.retrySameKey')}
              </Button>
            </>
          ) : null}
          {phase === 'conflict' ||
          phase === 'refused' ||
          phase === 'lost' ||
          phase === 'confirmed' ? (
            <Button
              type="button"
              variant="secondary"
              onClick={onStartOver}
              data-slot="handoff-offer-start-new"
            >
              {t('handoff.offer.startOver')}
            </Button>
          ) : null}
          {/* No second "Close": DialogContent already renders a labelled close
              control, and a footer button with the same accessible name would give
              the dialog two indistinguishable exits by name. */}
        </DialogFooter>
        <p
          className="text-caption text-muted-foreground"
          data-slot="handoff-offer-tracking-limits"
        >
          {phase === 'submitting' || phase === 'uncertain'
            ? t('handoff.tracking.pendingHide')
            : t('handoff.tracking.limits')}
        </p>
      </DialogContent>
    </Dialog>
  )
}
