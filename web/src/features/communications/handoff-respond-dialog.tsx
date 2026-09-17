// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useCallback, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
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
import type { HandoffResponseOutcome } from './api'
import { FailureNotice } from './failure-notice'
import type { HandoffOperation } from './handoff-operation'
import {
  collectReferences,
  type ReferenceDraft,
} from './handoff-reference-drafts'
import { ReferenceEditor } from './handoff-references'
import type { HandoffResponseIntent } from './intent'
import { useOpenerFocus } from './handoff-opener-focus'
import {
  HANDOFF_REASON_CODE_MAX_BYTES,
  HANDOFF_REASON_CODE_PATTERN,
  HANDOFF_REASON_PRESETS,
  HANDOFF_REASON_REFERENCES_MAX,
  HANDOFF_REASON_TEXT_MAX_BYTES,
  type HandoffReason,
  type HandoffResponseInput,
  type HandoffTransition,
} from './types'

const CUSTOM = '__custom__'
const bytes = (value: string) => new TextEncoder().encode(value).length

/** The target of a response, as the room hands it over. These are administrative
 * identifiers only: no summary, next action or risk is copied out of the protected
 * detail, so closing that detail loses the content and keeps the operation. */
export interface HandoffRespondTarget {
  handoffId: string
  etag: string
  workItemId: string
  recipient: { kind: string; ref: string }
  deliveryId: string
  transition: HandoffTransition
}

/**
 * Accept, or reject with a reason.
 *
 * Accept sends no reason and reject cannot send an empty one: the engine's
 * normaliser refuses a rejection without a reason and an accept that carries one,
 * so neither shape is offered.
 *
 * The four reason presets are console defaults, not a backend enum. The engine
 * keeps an open code vocabulary, so a custom code is always available and is
 * validated against the engine's bounded-token rule and nothing else.
 *
 * `resulting_lease_fence` is optional. At R45 a user-to-user transfer of a
 * never-held vacant generation advances the owner epoch, creates an Ack and leaves
 * the lease vacant, so the response carries no fence. A fence is shown only when
 * the response contains one, and never described as a running session.
 */
export function HandoffRespondDialog({
  open,
  onOpenChange,
  target,
  operation,
  canRespond,
  buildIntent,
  onEndInvocation,
  targetRefused = false,
  getOpener,
  getFallbackFocus,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  target: HandoffRespondTarget | null
  operation: HandoffOperation<HandoffResponseIntent, HandoffResponseOutcome>
  canRespond: boolean
  /** A newer target arrived while a transmitted command was still unresolved. */
  targetRefused?: boolean
  getOpener?: () => HTMLElement | null
  getFallbackFocus?: () => HTMLElement | null
  /** Freezes the intent for the validated body. The ROOM owns the scope and the
   * boundary, so it — not this transient panel — is what mints the immutable
   * intention the transport will judge. */
  buildIntent: (
    target: HandoffRespondTarget,
    body: HandoffResponseInput,
  ) => HandoffResponseIntent
  /**
   * End this invocation. `reset` returns an already resolved operation to idle;
   * `discard` is the explicit stop-tracking act for one that may have been sent.
   * Either way the host drops the target and validator and closes this editor —
   * the next response starts from a freshly read Delivery detail.
   */
  onEndInvocation: (mode: 'reset' | 'discard') => void
}) {
  const { t } = useTranslation('communications')
  const idp = useId()
  const noOpener = useCallback(() => null, [])
  const opener = useOpenerFocus({
    open,
    getOpener: getOpener ?? noOpener,
    getFallback: getFallbackFocus,
  })

  const [preset, setPreset] = useState<string>('')
  const [customCode, setCustomCode] = useState('')
  const [text, setText] = useState('')
  const [references, setReferences] = useState<ReferenceDraft[]>([])
  const [problems, setProblems] = useState<string[]>([])
  /** Per-field messages, so a required input is marked as well as summarised. */
  const [fieldErrors, setFieldErrors] = useState<{
    code?: string
    text?: string
    references?: string
  }>({})

  const phase = operation.state.phase
  const editing = phase === 'idle'
  const busy = phase === 'submitting'
  const transition = target?.transition ?? 'accept'

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

  const buildBody = (): HandoffResponseInput | null => {
    if (transition === 'accept') {
      setProblems([])
      setFieldErrors({})
      return { transition: 'accept' }
    }
    const found: string[] = []
    const fields: { code?: string; text?: string; references?: string } = {}
    const note = (field: 'code' | 'text' | 'references', message: string) => {
      found.push(message)
      if (!fields[field]) fields[field] = message
    }
    const code = (preset === CUSTOM ? customCode : preset).trim()
    if (code === '') note('code', t('handoff.respond.validation.code'))
    else if (bytes(code) > HANDOFF_REASON_CODE_MAX_BYTES)
      note('code', t('handoff.respond.validation.codeLength'))
    else if (!HANDOFF_REASON_CODE_PATTERN.test(code))
      note('code', t('handoff.respond.validation.codeShape'))
    const textValue = text.trim()
    if (textValue !== '' && bytes(textValue) > HANDOFF_REASON_TEXT_MAX_BYTES)
      note('text', t('handoff.respond.validation.textLength'))
    const { refs, invalid } = collectReferences(references)
    if (invalid) note('references', t('handoff.respond.validation.references'))
    if (refs.length > HANDOFF_REASON_REFERENCES_MAX)
      note('references', t('handoff.respond.validation.referencesMax'))

    setProblems([...new Set(found)])
    setFieldErrors(fields)
    if (found.length > 0) return null
    const reason: HandoffReason = { code }
    if (textValue !== '') reason.text = textValue
    if (refs.length > 0) reason.references = refs
    return { transition: 'reject', reason }
  }

  return (
    // Hiding the panel while the command is pending is safe: the host keeps
    // tracking it and the outcome classification does not change.
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="flex max-h-[85vh] w-full flex-col gap-4 sm:max-w-xl"
        onCloseAutoFocus={opener.onCloseAutoFocus}
      >
        <DialogHeader>
          <DialogTitle>
            {transition === 'accept'
              ? t('handoff.respond.acceptTitle')
              : t('handoff.respond.rejectTitle')}
          </DialogTitle>
          <DialogDescription>
            {transition === 'accept'
              ? t('handoff.respond.acceptDescription')
              : t('handoff.respond.rejectDescription')}
          </DialogDescription>
        </DialogHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto pr-1">
          {target ? (
            <KvList>
              <KvRow label={t('handoff.detail.workItem')} mono>
                {target.workItemId}
              </KvRow>
              <KvRow label={t('handoff.detail.to')} mono>
                {target.recipient.kind}:{target.recipient.ref}
              </KvRow>
              <KvRow label={t('handoff.detail.handoffId')} mono>
                {target.handoffId}
              </KvRow>
              <KvRow label={t('handoff.detail.precondition')} mono>
                {target.etag}
              </KvRow>
            </KvList>
          ) : null}

          {editing && transition === 'reject' ? (
            <div
              className="flex flex-col gap-3"
              data-slot="handoff-reject-form"
            >
              <Field
                label={t('handoff.respond.reasonCode')}
                htmlFor={`${idp}-code`}
                description={t('handoff.respond.reasonCodeHint')}
                error={fieldErrors.code}
              >
                <Select value={preset} onValueChange={setPreset}>
                  <SelectTrigger
                    id={`${idp}-code`}
                    aria-label={t('handoff.respond.reasonCode')}
                    aria-required="true"
                  >
                    <SelectValue
                      placeholder={t('handoff.respond.reasonCodeEmpty')}
                    />
                  </SelectTrigger>
                  <SelectContent>
                    {HANDOFF_REASON_PRESETS.map((c) => (
                      <SelectItem key={c} value={c}>
                        {t(`handoff.respond.reasons.${c}`)}
                      </SelectItem>
                    ))}
                    <SelectItem value={CUSTOM}>
                      {t('handoff.respond.reasons.custom')}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </Field>
              {preset === CUSTOM ? (
                <Field
                  label={t('handoff.respond.customCode')}
                  htmlFor={`${idp}-custom`}
                  description={t('handoff.respond.customCodeHint')}
                  error={fieldErrors.code}
                >
                  <Input
                    id={`${idp}-custom`}
                    aria-required="true"
                    value={customCode}
                    onChange={(e) => setCustomCode(e.target.value)}
                    autoComplete="off"
                    mono
                  />
                </Field>
              ) : null}
              <Field
                label={t('handoff.respond.reasonText')}
                htmlFor={`${idp}-text`}
                description={t('handoff.respond.reasonTextHint')}
                error={fieldErrors.text}
              >
                <Textarea
                  id={`${idp}-text`}
                  value={text}
                  onChange={(e) => setText(e.target.value)}
                  rows={3}
                />
              </Field>
              <ReferenceEditor
                drafts={references}
                onChange={setReferences}
                idPrefix={`${idp}-reason`}
                max={HANDOFF_REASON_REFERENCES_MAX}
                label={t('handoff.respond.reasonReferences')}
                error={fieldErrors.references}
              />
              {problems.length > 0 ? (
                <ul
                  role="alert"
                  data-testid="respond-problems"
                  className="flex flex-col gap-1 rounded-md border border-danger-line bg-danger-soft px-3 py-2 text-sm text-danger"
                  data-slot="handoff-respond-problems"
                >
                  {problems.map((p) => (
                    <li key={p}>{p}</li>
                  ))}
                </ul>
              ) : null}
            </div>
          ) : null}

          {targetRefused ? (
            <div
              role="alert"
              className="rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-sm text-warning"
              data-slot="handoff-respond-target-refused"
            >
              <p className="font-medium">
                {t('handoff.respond.targetRefusedTitle')}
              </p>
              <p>{t('handoff.respond.targetRefusedBody')}</p>
            </div>
          ) : null}

          {/* An earlier attempt of this command ended without establishing its
              outcome. A later definitive answer describes the request that
              returned it and cannot settle that one. While the current attempt is
              itself pending or unknown, its own state already says so. */}
          {operation.priorUnresolvedAttempt &&
          phase !== 'submitting' &&
          phase !== 'uncertain' ? (
            <div
              role="status"
              className="rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-sm text-warning"
              data-slot="handoff-respond-prior-unresolved"
            >
              {t('handoff.uncertain.priorTransmission')}
            </div>
          ) : null}

          {reviewed ? (
            <section
              aria-label={t('handoff.respond.confirmTitle')}
              className="flex flex-col gap-2 rounded-md border border-border bg-muted p-3"
              data-slot="handoff-respond-intent"
            >
              <p className="text-sm font-medium">
                {t('handoff.respond.confirmTitle')}
              </p>
              <p className="text-sm text-muted-foreground">
                {reviewed.body.transition === 'accept'
                  ? t('handoff.respond.confirmAcceptBody')
                  : t('handoff.respond.confirmRejectBody')}
              </p>
              <KvList>
                <KvRow label={t('handoff.respond.transition')} mono>
                  {reviewed.body.transition}
                </KvRow>
                {reviewed.body.reason ? (
                  <KvRow label={t('handoff.respond.reasonCode')} mono>
                    {reviewed.body.reason.code}
                  </KvRow>
                ) : null}
                <KvRow label={t('handoff.detail.precondition')} mono>
                  {reviewed.etag}
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
              className="rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-sm text-warning"
              data-slot="handoff-respond-lost"
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
            <div data-slot="handoff-respond-conflict">
              <FailureNotice
                failure={failure}
                title={
                  operation.priorUnresolvedAttempt
                    ? t('handoff.respond.conflictTitleLatest')
                    : t('handoff.respond.conflictTitle')
                }
              />
              <p className="mt-1 text-sm text-muted-foreground">
                {operation.priorUnresolvedAttempt
                  ? t('handoff.respond.conflictBodyLatest')
                  : t('handoff.respond.conflictBody')}
              </p>
            </div>
          ) : null}
          {phase === 'uncertain' && failure ? (
            <div data-slot="handoff-respond-uncertain">
              <FailureNotice
                failure={failure}
                title={t('handoff.uncertain.title')}
              />
              <p className="mt-1 text-sm text-muted-foreground">
                {t('handoff.uncertain.body')}
              </p>
            </div>
          ) : null}
          {phase === 'refused' && failure ? (
            <FailureNotice
              failure={failure}
              title={
                operation.priorUnresolvedAttempt
                  ? t('handoff.respond.refusedTitleLatest')
                  : t('handoff.respond.refusedTitle')
              }
            />
          ) : null}

          {operation.state.phase === 'confirmed' ? (
            <HandoffResponseReceipt
              outcome={operation.state.outcome}
              intent={operation.state.intent}
            />
          ) : null}
        </div>

        <DialogFooter>
          {editing ? (
            <Button
              type="button"
              variant="primary"
              disabled={!canRespond || !target}
              onClick={() => {
                const body = buildBody()
                if (!body || !target) return
                operation.review(buildIntent(target, body))
              }}
            >
              {t('handoff.respond.review')}
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
                disabled={!canRespond}
              >
                {transition === 'accept'
                  ? t('handoff.respond.confirmAccept')
                  : t('handoff.respond.confirmReject')}
              </Button>
            </>
          ) : null}
          {busy ? (
            <Button
              type="button"
              variant="secondary"
              onClick={() => onEndInvocation('discard')}
            >
              {t('handoff.uncertain.discard')}
            </Button>
          ) : null}
          {busy ? (
            <Button type="button" variant="primary" disabled>
              <Spinner className="size-3.5" />
              {t('handoff.respond.submitting')}
            </Button>
          ) : null}
          {phase === 'uncertain' ? (
            <>
              <Button
                type="button"
                variant="secondary"
                onClick={() => onEndInvocation('discard')}
              >
                {t('handoff.uncertain.discard')}
              </Button>
              <Button
                type="button"
                variant="primary"
                onClick={operation.retrySame}
                disabled={!canRespond}
              >
                {t('actions.retrySameKey')}
              </Button>
            </>
          ) : null}
          {phase === 'confirmed' ||
          phase === 'refused' ||
          phase === 'conflict' ||
          phase === 'lost' ? (
            <Button
              type="button"
              variant="secondary"
              onClick={() => onEndInvocation('reset')}
              data-slot="handoff-respond-start-new"
            >
              {t('handoff.respond.startNew')}
            </Button>
          ) : null}
          {/* No second "Close": DialogContent already renders a labelled close
              control, and a footer button with the same accessible name would give
              the dialog two indistinguishable exits by name. */}
        </DialogFooter>
        <p
          className="text-xs text-muted-foreground"
          data-slot="handoff-respond-tracking-limits"
        >
          {phase === 'submitting' || phase === 'uncertain'
            ? t('handoff.tracking.pendingHide')
            : t('handoff.tracking.limits')}
        </p>
      </DialogContent>
    </Dialog>
  )
}

/**
 * The response receipt, and the three sentences it is not allowed to say.
 *
 *  · It never calls an accepted transfer a running session or an execution lease.
 *  · It shows `resulting_lease_fence` only when the response CARRIES one. At R45 the
 *    real user-to-user path over a vacant generation omits it entirely, and a "fence
 *    0" printed from a missing field would describe an execution that never started.
 *  · It shows `ack_id` only when present, for the same reason.
 *
 * A rejection receipt states that ownership did NOT move, because "the command
 * succeeded" and "the work changed hands" are different facts and only one of them
 * is true here.
 */
function HandoffResponseReceipt({
  outcome,
  intent,
}: {
  outcome: HandoffResponseOutcome
  intent: HandoffResponseIntent
}) {
  const { t } = useTranslation('communications')
  const accepted = intent.body.transition === 'accept'
  return (
    <section
      aria-label={t('receipt.title')}
      className="flex flex-col gap-2"
      data-slot="handoff-respond-receipt"
    >
      <div
        role="status"
        className={
          outcome.replayed
            ? 'rounded-md border border-info-line bg-info-soft px-3 py-2 text-sm text-info'
            : 'rounded-md border border-success-line bg-success-soft px-3 py-2 text-sm text-success'
        }
      >
        <p className="font-medium">
          {outcome.replayed
            ? t('handoff.respond.replayedTitle')
            : accepted
              ? t('handoff.respond.acceptedTitle')
              : t('handoff.respond.rejectedTitle')}
        </p>
        <p>
          {accepted
            ? t('handoff.respond.acceptedBody')
            : t('handoff.respond.rejectedBody')}
        </p>
      </div>
      <KvList>
        <KvRow label={t('handoff.receipt.handoffId')} mono>
          {outcome.result.handoff_id}
        </KvRow>
        <KvRow label={t('handoff.detail.workItem')} mono>
          {outcome.result.work_item_id}
        </KvRow>
        <KvRow label={t('receipt.commandId')} mono>
          {outcome.result.command_id}
        </KvRow>
        <KvRow label={t('receipt.state')} mono>
          {outcome.result.state}
        </KvRow>
        {/* The receipt's own epoch, labelled as such. Current ownership is a
            property of the work item and is read there, not inferred here. */}
        <KvRow label={t('handoff.receipt.ownerEpoch')} mono>
          {outcome.result.owner_epoch}
        </KvRow>
        {outcome.result.ack_id ? (
          <KvRow label={t('receipt.ackId')} mono>
            {outcome.result.ack_id}
          </KvRow>
        ) : null}
        {outcome.result.resulting_lease_fence !== undefined ? (
          <KvRow label={t('handoff.receipt.leaseFence')} mono>
            {outcome.result.resulting_lease_fence}
          </KvRow>
        ) : null}
        <KvRow label={t('receipt.etag')} mono>
          {outcome.etag ?? outcome.result.etag}
        </KvRow>
        <KvRow label={t('receipt.auditSeq')} mono>
          {outcome.result.audit_seq}
        </KvRow>
      </KvList>
      {accepted && outcome.result.resulting_lease_fence === undefined ? (
        <p
          className="text-xs text-muted-foreground"
          data-slot="handoff-no-lease"
        >
          {t('handoff.receipt.noLease')}
        </p>
      ) : null}
      <p
        className="text-xs text-muted-foreground"
        data-slot="handoff-receipt-not-current"
      >
        {t('handoff.receipt.notCurrentOwnership')}
      </p>
    </section>
  )
}
