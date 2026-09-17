// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation } from '@tanstack/react-query'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { useEffect, useId, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
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
import {
  isEstablishedRefusal,
  useCapability,
  useCapabilityPreflight,
  type CapabilityAccess,
} from '@/lib/auth/capabilities'
import { updateChannel, type ChannelAdminOutcome } from './api'
import type { CommunicationsScope } from './boundary'
import { configurationQuestion } from './capabilities'
import { CapabilityNotice } from './capability-notice'
import { classifyFailure, type Failure } from './errors'
import { FailureNotice } from './failure-notice'
import {
  buildUpdateChannelIntent,
  permittedDispatchGuard,
  StaleIntentError,
  useIntentGuard,
  type UpdateChannelIntent,
} from './intent'
import {
  ACK_POLICIES,
  CHANNEL_DESCRIPTION_MAX_BYTES,
  CHANNEL_NAME_MAX_BYTES,
  CHANNEL_PATCH_STATES,
  CHANNEL_SENSITIVITIES,
  CHANNEL_WAKE_POLICIES,
  CONTENT_PROTECTIONS,
  type Channel,
  type ChannelUpdateField,
  type ChannelUpdateInput,
} from './types'

type Phase =
  | 'edit'
  | 'confirm'
  | 'submitting'
  | 'applied'
  | 'conflict'
  | 'unknown'
  | 'refused'

interface Draft {
  name: string
  description: string
  state: string
  sensitivity: string
  protection: string
  ackPolicy: string
  ackTimeout: string
  wake: string
  retention: string
  maxFanout: string
  maxDepth: string
}

function draftOf(c: Channel): Draft {
  return {
    name: c.name,
    description: c.description ?? '',
    state: c.state,
    sensitivity: c.sensitivity,
    protection: c.content_protection,
    ackPolicy: c.default_ack_policy,
    ackTimeout: String(c.default_ack_timeout_ms),
    wake: c.default_wake,
    retention: c.retention_policy_ref ?? '',
    maxFanout: String(c.max_fanout),
    maxDepth: String(c.max_automation_depth),
  }
}

/**
 * WHAT SURVIVES A TRANSIENT GAP IN THE SHEET'S OBSERVATION — and it is only ever the
 * operator's own typing.
 *
 * ⛔ NOT ONE BYTE THE ENGINE ANSWERED. No Channel, no ETag, no grant page, no observed
 *    instant: everything the sheet read is dropped with the read that carried it, so
 *    nothing authorized under a positive that has since expired is retained, painted
 *    again, or reused as authority. What is held is what the OPERATOR wrote — an
 *    intention, which no permission answer produces and none invalidates — and it is
 *    re-applied only on top of a NEW authorized read, by the same rule a fresh read
 *    already follows: the touched fields are the operator's, every other field is the
 *    engine's.
 *
 * The two phase flags travel with it because the sheet, not this form, is what remains
 * mounted to say that a prepared confirmation went away, and the sentence it may use
 * depends on whether the act had already been sent.
 */
export interface ChannelDraftHold {
  /** ONLY the fields the operator touched, in draft terms. An untouched field is not an
   *  intention, and carrying it would be carrying the engine's answer under another name. */
  edits: Readonly<Record<string, string>>
  /** A confirmation was prepared when the form was last seen. */
  confirming: boolean
  /** …and had already been sent, so nothing may promise that zero bytes left. */
  submitting: boolean
}

/**
 * WHICH SENTENCE CLOSES A CONFIRMATION, and the rule is that the console only says what
 * it was told.
 *
 * ⛔ THIS PATH USED TO SAY "the permission behind it was lost" FOR EVERY LOSS OF A
 *    POSITIVE. Under the migration the same branch now receives a concealed non-verdict,
 *    an expiry, a movement and a transport that did not answer — and it announced a
 *    permission loss for all of them. That is a cause the engine never published, stated
 *    to the operator as fact, and for `undisclosed` it is precisely the inference the
 *    concealment exists to prevent. Reproduced with the real form, hook, client and
 *    QueryClient: a mid-confirmation `undisclosed` produced that exact sentence.
 *
 * And a closure that arrives while the act is ALREADY SUBMITTING may not promise that
 * nothing was sent: the composed dispatch guard decides that immediately before the
 * bytes, this render does not know its verdict, and claiming zero sends would be the
 * same defect pointed the other way.
 */
function closureLine(access: CapabilityAccess, submitting: boolean): string {
  if (isEstablishedRefusal(access)) return 'authority.confirmationClosed'
  return submitting
    ? 'capability.actInterrupted'
    : 'capability.confirmationClosed'
}

const bytesOf = (s: string) => new TextEncoder().encode(s).length
const whole = (v: string) => /^\d+$/.test(v.trim())

/** Which contract field each control of the draft edits. The touched set is kept in
 * CONTRACT terms, so what the operator touched and what the PATCH may carry are the
 * same vocabulary. */
const FIELD_OF: Record<keyof Draft, ChannelUpdateField> = {
  name: 'name',
  description: 'description',
  state: 'state',
  sensitivity: 'sensitivity',
  protection: 'content_protection',
  ackPolicy: 'default_ack_policy',
  ackTimeout: 'default_ack_timeout_ms',
  wake: 'default_wake',
  retention: 'retention_policy_ref',
  maxFanout: 'max_fanout',
  maxDepth: 'max_automation_depth',
}

/**
 * Which control of THIS form instance has the caret, as the form's own field key — or null
 * when the operator is somewhere else entirely.
 *
 * It is a key, not a node and not a selector: the ids are per-instance (`useId`), so the
 * value survives a remount while naming nothing outside this form.
 */
function focusedField(idp: string): string | null {
  const active = document.activeElement
  const id = active instanceof HTMLElement ? active.id : ''
  return id.startsWith(`${idp}-`) ? id.slice(idp.length + 1) : null
}

/** A draft seeded from the read, with the operator's held edits laid back over it. */
function seedDraft(
  c: Channel,
  hold: ChannelDraftHold | null | undefined,
): Draft {
  const base = draftOf(c)
  if (!hold) return base
  for (const [key, value] of Object.entries(hold.edits))
    if (key in base) base[key as keyof Draft] = value
  return base
}

/** The touched set those held edits imply, in CONTRACT terms — the vocabulary the PATCH
 *  is built from, so a restored edit is sendable exactly like a freshly typed one. */
function heldTouched(
  hold: ChannelDraftHold | null | undefined,
): ReadonlySet<ChannelUpdateField> {
  const set = new Set<ChannelUpdateField>()
  if (hold)
    for (const key of Object.keys(hold.edits)) {
      const field = FIELD_OF[key as keyof Draft]
      if (field) set.add(field)
    }
  return set
}

/**
 * The PATCH body: `channel_id` plus only fields that BOTH differ from the base and
 * were TOUCHED by the operator.
 *
 * ⛔ THE `touched` ARGUMENT IS THE CORRECTION OF IR-I2-2, and the reason is a race,
 * not tidiness. A draft holds every field. When a fresh read moved a field the
 * operator never edited, a diff of "draft against the new base" reports that remote
 * change as a local edit and sends the OLD value back — under the NEW ETag, so the
 * rollback is CAS-valid and nothing refuses it. Filtering by what the operator
 * actually touched makes an untouched field unsendable; the form additionally
 * rebases untouched fields on every fresh read, so the two defences are independent.
 *
 * `touched` is optional so the pure function keeps its original meaning for the
 * callers that ask "what differs at all"; the form always passes it.
 */
export function diffChannel(
  base: Channel,
  d: Draft,
  touched?: ReadonlySet<ChannelUpdateField>,
): { body: ChannelUpdateInput; changed: ChannelUpdateField[] } {
  const body: ChannelUpdateInput = { channel_id: base.id }
  const changed: ChannelUpdateField[] = []
  const set = <K extends ChannelUpdateField>(
    key: K,
    value: ChannelUpdateInput[K],
    same: boolean,
  ) => {
    if (same) return
    if (touched && !touched.has(key)) return
    body[key] = value
    changed.push(key)
  }
  set('name', d.name.trim(), d.name.trim() === base.name)
  set(
    'description',
    d.description.trim(),
    d.description.trim() === (base.description ?? ''),
  )
  set('state', d.state as ChannelUpdateInput['state'], d.state === base.state)
  set(
    'sensitivity',
    d.sensitivity as ChannelUpdateInput['sensitivity'],
    d.sensitivity === base.sensitivity,
  )
  set(
    'content_protection',
    d.protection as ChannelUpdateInput['content_protection'],
    d.protection === base.content_protection,
  )
  set(
    'default_ack_policy',
    d.ackPolicy as ChannelUpdateInput['default_ack_policy'],
    d.ackPolicy === base.default_ack_policy,
  )
  set(
    'default_ack_timeout_ms',
    Number(d.ackTimeout),
    Number(d.ackTimeout) === base.default_ack_timeout_ms,
  )
  set(
    'default_wake',
    d.wake as ChannelUpdateInput['default_wake'],
    d.wake === base.default_wake,
  )
  set(
    'retention_policy_ref',
    d.retention.trim(),
    d.retention.trim() === (base.retention_policy_ref ?? ''),
  )
  set(
    'max_fanout',
    Number(d.maxFanout),
    Number(d.maxFanout) === base.max_fanout,
  )
  set(
    'max_automation_depth',
    Number(d.maxDepth),
    Number(d.maxDepth) === base.max_automation_depth,
  )
  return { body, changed }
}

/**
 * ChannelConfigForm — `PATCH /channels` from the administrative sheet. Name and
 * description are the everyday fields; the advanced group carries every other
 * field of the contract, none removed. The form is seeded from the Channel of the
 * sheet's FRESH read and sends only what changed, under that read's ETag. The
 * engine's own rules are applied before a request leaves — name 1–256 bytes,
 * description ≤ 4096, fanout ≥ 1, depth ≥ 0, `none` ⇔ timeout 0, no `inherit` on a
 * Channel, restricted ⇒ application-sealed, sensitivity and protection never
 * lowered, an archived Channel's state never changed — and everything else is the
 * engine's answer, shown as it comes. A 409/412/428 kills the intention: the
 * operator re-reads and confirms again; the ETag is never substituted. A lost
 * response is an unknown outcome to verify by re-reading, never a re-send.
 *
 * The draft is seeded from the read and, when the sheet held one across a transient gap
 * in its observation, from the operator's own edits laid back over that read. The form
 * reports those edits — never the read — so the hold can outlive one instance of it.
 */
export function ChannelConfigForm({
  channel,
  etag,
  scope,
  reading,
  onReread,
  onApplied,
  takeHold,
  onHold,
}: {
  channel: Channel
  /** The Channel ETag of the read `channel` came from. */
  etag: string
  scope: CommunicationsScope
  /** Whether the sheet's fresh read is in flight: a confirmation waits for it. */
  reading: boolean
  onReread: () => void
  /** The 200 was read: the sheet re-reads and invalidates its siblings. */
  onApplied: (outcome: ChannelAdminOutcome) => void
  /** The operator's own edits from an EARLIER mount of this same form, held above the
   *  permission cut by the route's continuity boundary. PULLED once, in this form's own
   *  mount initializer, which is the only instant a restored draft can be used at all: the
   *  boundary owns it and this form only ever produces it, so it cannot feed itself. */
  takeHold?: () => { hold: ChannelDraftHold; focus: string | null } | null
  /** Reports those edits, whether a confirmation is prepared, and which control the caret
   *  is in — as they happen, so an interruption cannot cost the last keystroke. */
  onHold?: (hold: ChannelDraftHold, focus: string | null) => void
}) {
  const { t } = useTranslation('communications')
  const idp = useId()
  const workspace = scope.workspace ?? ''
  /** Taken exactly once, at mount, and never re-read: after this the form's state is its
   *  own and the boundary's copy is only an output of it. */
  const [restored] = useState(() => takeHold?.() ?? null)
  const held = restored?.hold ?? null
  const [draft, setDraft] = useState<Draft>(() => seedDraft(channel, held))
  const [seeded, setSeeded] = useState(etag)
  /** The fields the operator actually edited, in contract terms. Kept apart from the
   * draft on purpose: the draft always holds every field, so it cannot say which of
   * its values are an intention and which are just the last thing that was read. */
  const [touched, setTouched] = useState<ReadonlySet<ChannelUpdateField>>(() =>
    heldTouched(held),
  )
  /** Fields a fresh read moved underneath an edit and this form rebased. Named so
   * the operator can see that the base changed, rather than discovering it in a
   * confirmation. */
  const [rebased, setRebased] = useState<ChannelUpdateField[]>([])
  const [advanced, setAdvanced] = useState(false)
  const [errors, setErrors] = useState<string[]>([])
  const [phase, setPhase] = useState<Phase>('edit')
  const [intent, setIntent] = useState<UpdateChannelIntent | null>(null)
  const [outcome, setOutcome] = useState<ChannelAdminOutcome | null>(null)
  const [failure, setFailure] = useState<Failure | null>(null)
  const [closure, setClosure] = useState<{ n: number; line: string } | null>(
    null,
  )

  const dirty = touched.size > 0

  // WHAT THE BOUNDARY IS TOLD TO HOLD, and it is only what the operator typed. Reported
  // rather than read back: the hold above is taken once, at mount, so this effect cannot
  // feed its own seed.
  //
  // ⛔ PUBLISHED AS IT HAPPENS, NOT ON THE WAY OUT. A cleanup at unmount would be one
  //    effect too late for the character typed in the millisecond before an expiry, and at
  //    a budget edge this form is not unmounted by itself — the whole route is. So every
  //    change of the draft, the touched set or the phase reaches the owner in the commit
  //    that made it.
  //
  //    The phase travels with it because whoever is still mounted has to be able to say a
  //    prepared confirmation was interrupted; the caret's control travels with it so the
  //    operator can be put back where they were, and neither is authority.
  useEffect(() => {
    if (!onHold) return
    const edits: Record<string, string> = {}
    for (const key of Object.keys(draft) as (keyof Draft)[])
      if (touched.has(FIELD_OF[key])) edits[key] = draft[key]
    onHold(
      {
        edits,
        confirming: phase === 'confirm',
        submitting: phase === 'submitting',
      },
      focusedField(idp),
    )
  }, [draft, touched, phase, onHold, idp])

  // ⛔ AN INTERRUPTED CONFIRMATION IS EXPLAINED WHEN THE OPERATOR CAN READ IT, AND ONCE.
  //    When the route itself was torn down there was no surface left to speak, and a
  //    sentence per budget edge would be five words every five seconds. So the marks
  //    travel in the capsule and are answered here, on the mount that recovers them: the
  //    confirmation is gone, the edits are not, and an act that was already being sent is
  //    never reported as a zero-send. The publish effect above clears the marks in this
  //    same commit, so a later mount finds nothing to repeat.
  useEffect(() => {
    if (!restored) return
    if (restored.hold.submitting) toast.warning(t('continuity.interruptedSend'))
    else if (restored.hold.confirming)
      toast.warning(t('continuity.interrupted'))
    // Mount-only: the capsule is taken once and this answers that one delivery.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // The caret goes back only if the operator has not put it somewhere else — and "else"
  // means OUTSIDE this surface: a sheet that has just mounted moves focus to itself, which
  // is the dialog's doing and not the operator's.
  //
  // ⛔ DEFERRED BY ONE TASK, AND NOT FOR TIDINESS. A child's effects run before its
  //    parents', so a restore attempted here would be undone a moment later by the
  //    dialog's own open-auto-focus. One task later the surface has settled and the
  //    operator's own choice, if they made one, is the thing on screen. Nothing is trapped
  //    either way: this either puts the caret back or does nothing at all.
  useEffect(() => {
    const key = restored?.focus
    if (!key) return
    const id = window.setTimeout(() => {
      const control = document.getElementById(`${idp}-${key}`)
      if (!(
        control instanceof HTMLInputElement ||
        control instanceof HTMLTextAreaElement
      ))
        return
      const active = document.activeElement
      const room = control.closest('[role="dialog"]') ?? document.body
      if (active && active !== document.body && !room.contains(active)) return
      control.focus()
      if (control instanceof HTMLInputElement) {
        const at = control.value.length
        control.setSelectionRange(at, at)
      }
    }, 0)
    return () => window.clearTimeout(id)
    // Mount-only, for the same reason as above.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // A FRESH READ REBASES EVERY UNTOUCHED FIELD (adjust-during-render). A pristine
  // form re-seeds whole; an edited one keeps ONLY what the operator touched and
  // takes every other field from the channel just read. Keeping the whole old draft
  // — what this form did before — meant a remote change to a field nobody edited
  // came back as an operator edit against the new ETag, which the engine would have
  // accepted (IR-I2-2). Any open confirmation is closed: a new base needs a new
  // review, and the ETag is never swapped underneath one.
  if (seeded !== etag) {
    setSeeded(etag)
    const fresh = draftOf(channel)
    if (!dirty) {
      setDraft(fresh)
      setRebased([])
    } else {
      // Computed from THIS render's draft, not inside a state updater: an updater
      // may run twice (StrictMode) and would then report the same field twice.
      const moved: ChannelUpdateField[] = []
      const next = { ...draft }
      for (const key of Object.keys(fresh) as (keyof Draft)[]) {
        const field = FIELD_OF[key]
        if (touched.has(field)) continue
        if (next[key] !== fresh[key]) moved.push(field)
        next[key] = fresh[key]
      }
      setDraft(next)
      setRebased(moved)
    }
    if (phase === 'confirm') {
      setPhase('edit')
      setIntent(null)
      // ⛔ AND IT SAYS SO. A prepared confirmation that simply VANISHES is the defect this
      //    console keeps finding in its own surfaces: the operator watches a review region
      //    disappear and is left to guess whether something was sent. The base moved, so
      //    the intention that was frozen against the previous ETag is spent — the
      //    validator is never swapped underneath it — and a NEW review is required.
      //
      //    Its own sentence, because its own cause: `authority.confirmationClosed` would
      //    name a permission loss that did not happen, `capability.confirmationClosed`
      //    would report a missing answer that was never missing, and `config.rebased`
      //    describes UNTOUCHED fields being refreshed, which may not have happened at all
      //    — a version can move with every field on screen identical, and that is exactly
      //    the case where the rebase banner stays silent.
      setClosure((c) => ({ n: (c?.n ?? 0) + 1, line: 'config.versionMoved' }))
    }
  }

  // ⛔ THE FORM'S OWN OPERATION, ASKED EXACTLY. `PATCH /v1/m/sessions/channels` locates
  //    its row in the BODY, so the question carries `body.channel_id` — the route's one
  //    declared entity locator, an identification input and never the write payload.
  //    Reading this channel's grant sheet does NOT imply configuring it, so this asks for
  //    itself rather than inheriting the sheet's answer.
  const patchAccess = useCapability(
    configurationQuestion(workspace, channel.id),
  )
  const preflight = useCapabilityPreflight()
  const mayPatch = patchAccess.access === 'allowed'

  // No `permission` argument: the dispatch-time RBAC membership re-check is REPLACED by
  // the exact preflight in `patch` below. Unmigrated callers keep theirs.
  const guard = useIntentGuard({ allowed: mayPatch, boundary: scope.key })
  if (!mayPatch && (phase === 'confirm' || phase === 'submitting')) {
    setPhase('edit')
    setIntent(null)
    setClosure((c) => ({
      n: (c?.n ?? 0) + 1,
      line: closureLine(patchAccess.access, phase === 'submitting'),
    }))
  }
  useEffect(() => {
    if (closure) toast.warning(t(closure.line))
  }, [closure, t])
  // ⛔ A DIFFERENT SENTENCE FOR A DIFFERENT FACT. `confirmationClosed` says the permission
  //    behind the act was lost — true when the reflection or the boundary moved, and a
  //    DIAGNOSIS when the exact preflight simply did not return a current positive: that
  //    can be an expiry, a concealed non-verdict or a transport failure, and the console
  //    cannot tell which. So the neutral line is used, and it names no cause.
  const [unconfirmedCount, setUnconfirmedCount] = useState(0)
  useEffect(() => {
    if (unconfirmedCount > 0) toast.warning(t('capability.notConfirmed'))
  }, [unconfirmedCount, t])

  const patch = useMutation<ChannelAdminOutcome, unknown, UpdateChannelIntent>({
    mutationFn: async (i) => {
      const signal = guard.begin()
      if (!signal) throw new AuthorityLostError()
      // THE PREFLIGHT, at confirm and not at render: a NEW exact observation of this very
      // operation, under this confirmation's signal and the surface's dispatch guard. Only
      // a current positive returns a permit; anything else refuses with zero bytes sent.
      const question = configurationQuestion(i.scope.workspace, i.channelId)
      if (!question) throw new StaleIntentError('capability')
      const permit = await preflight.request(question, {
        signal,
        dispatchGuard: guard.check,
      })
      if (!permit) throw new StaleIntentError('capability')
      return updateChannel(
        i,
        {
          tenant: i.scope.tenant,
          guard: permittedDispatchGuard(guard, permit),
        },
        signal,
      )
    },
    onSuccess: (o) => {
      setOutcome(o)
      setPhase('applied')
      setIntent(null)
      // The act landed: the intentions are spent, so the next fresh read re-seeds
      // the whole form instead of rebasing around edits that no longer exist.
      setTouched(new Set())
      setRebased([])
      onApplied(o)
    },
    onError: (err) => {
      if (err instanceof StaleIntentError && err.moved === 'capability') {
        setPhase('edit')
        setIntent(null)
        setUnconfirmedCount((n) => n + 1)
        return
      }
      if (err instanceof AuthorityLostError) {
        setPhase('edit')
        setIntent(null)
        // The BOUNDARY moved — an established local fact this console owns and can name.
        setClosure((c) => ({
          n: (c?.n ?? 0) + 1,
          line: 'authority.confirmationClosed',
        }))
        return
      }
      const f = classifyFailure(err)
      if (f.kind === 'aborted') {
        setPhase('confirm')
        return
      }
      setFailure(f)
      if (
        f.kind === 'version_mismatch' ||
        f.kind === 'version_required' ||
        f.kind === 'conflict' ||
        f.kind === 'terminal'
      ) {
        setIntent(null)
        setPhase('conflict')
      } else if (f.kind === 'ambiguous') {
        setPhase('unknown')
      } else {
        setIntent(null)
        setPhase('refused')
      }
    },
  })

  const update = (patchDraft: Partial<Draft>) => {
    setDraft((d) => ({ ...d, ...patchDraft }))
    // Every control names the contract field it edits, so the PATCH can be built
    // from what the operator INTENDED rather than from whatever the draft holds.
    setTouched((current) => {
      const next = new Set(current)
      for (const key of Object.keys(patchDraft) as (keyof Draft)[]) {
        next.add(FIELD_OF[key])
      }
      return next
    })
  }
  const archived = channel.state === 'archived'
  const archiving = !archived && draft.state === 'archived'

  const validate = (): ChannelUpdateInput | null => {
    const problems: string[] = []
    const name = draft.name.trim()
    if (name === '' || bytesOf(name) > CHANNEL_NAME_MAX_BYTES)
      problems.push(t('config.validation.name'))
    if (bytesOf(draft.description.trim()) > CHANNEL_DESCRIPTION_MAX_BYTES)
      problems.push(t('config.validation.description'))
    if (!whole(draft.maxFanout) || Number(draft.maxFanout) < 1)
      problems.push(t('config.validation.fanout'))
    if (!whole(draft.maxDepth)) problems.push(t('config.validation.depth'))
    if (!whole(draft.ackTimeout)) problems.push(t('config.validation.timeout'))
    else if ((draft.ackPolicy === 'none') !== (Number(draft.ackTimeout) === 0))
      problems.push(t('config.validation.ackTimeout'))
    if (
      draft.sensitivity === 'restricted' &&
      draft.protection !== 'application_sealed'
    )
      problems.push(t('config.validation.restrictedSealed'))
    if (
      channel.sensitivity === 'restricted' &&
      draft.sensitivity !== 'restricted'
    )
      problems.push(t('config.validation.sensitivityLowered'))
    if (
      channel.content_protection === 'application_sealed' &&
      draft.protection !== 'application_sealed'
    )
      problems.push(t('config.validation.protectionLowered'))
    if (archived && draft.state !== 'archived')
      problems.push(t('config.validation.archivedState'))
    const { body, changed } = diffChannel(channel, draft, touched)
    if (problems.length === 0 && changed.length === 0)
      problems.push(t('config.validation.nothingChanged'))
    setErrors(problems)
    return problems.length === 0 ? body : null
  }

  const review = (e: FormEvent) => {
    e.preventDefault()
    if (phase !== 'edit' || !mayPatch || !workspace) return
    const body = validate()
    if (!body) return
    setFailure(null)
    setOutcome(null)
    // Frozen NOW: the body of this review, the ETag of the read on screen, the
    // authority of this moment. Confirming sends exactly this.
    setIntent(
      buildUpdateChannelIntent(
        { tenant: scope.tenant, workspace, boundary: scope.key },
        channel.id,
        etag,
        body,
      ),
    )
    setPhase('confirm')
  }
  const confirm = () => {
    if (!intent || !mayPatch || reading) return
    setPhase('submitting')
    patch.mutate(intent)
  }
  const backToEdit = () => {
    guard.end()
    setIntent(null)
    setFailure(null)
    setPhase('edit')
  }
  const rereadAfterConflict = () => {
    guard.end()
    setIntent(null)
    setFailure(null)
    setPhase('edit')
    onReread()
  }

  const selectField = (
    id: string,
    label: string,
    value: string,
    onChange: (v: string) => void,
    options: readonly string[],
    labelOf: (v: string) => string,
    disabledOption?: (v: string) => boolean,
    description?: string,
    disabled = false,
  ) => (
    <Field label={label} htmlFor={id} description={description}>
      <Select
        value={value}
        onValueChange={onChange}
        disabled={disabled || phase !== 'edit'}
      >
        <SelectTrigger id={id} aria-label={label}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map((o) => (
            <SelectItem key={o} value={o} disabled={disabledOption?.(o)}>
              {labelOf(o)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )

  const changedRows = intent
    ? (Object.keys(intent.body) as (keyof ChannelUpdateInput)[])
        .filter((k) => k !== 'channel_id')
        .map((k) => ({
          key: k,
          value: String(intent.body[k] ?? ''),
        }))
    : []

  return (
    <form
      onSubmit={review}
      className="flex flex-col gap-4"
      noValidate
      data-slot="channel-config-form"
    >
      {archived ? (
        <p
          className="rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-sm text-warning"
          role="status"
        >
          {t('config.archivedNote')}
        </p>
      ) : null}
      {rebased.length > 0 ? (
        <p
          className="rounded-md border border-info-line bg-info-soft px-3 py-2 text-sm text-info"
          role="status"
          data-slot="config-rebased"
        >
          {t('config.rebased', {
            fields: rebased
              .map((f) => t(`config.fieldNames.${f}`, { defaultValue: f }))
              .join(', '),
          })}
        </p>
      ) : null}
      {errors.length > 0 ? (
        <ul role="alert" className="list-disc pl-5 text-sm text-danger">
          {errors.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      ) : null}
      <Field label={t('channel.fields.name')} htmlFor={`${idp}-name`} required>
        <Input
          id={`${idp}-name`}
          value={draft.name}
          onChange={(e) => update({ name: e.target.value })}
          autoComplete="off"
          disabled={phase !== 'edit'}
        />
      </Field>
      <Field
        label={t('channel.fields.descriptionField')}
        htmlFor={`${idp}-description`}
      >
        <Textarea
          id={`${idp}-description`}
          value={draft.description}
          onChange={(e) => update({ description: e.target.value })}
          rows={3}
          disabled={phase !== 'edit'}
        />
      </Field>
      <KvList>
        <KvRow label={t('channel.fields.slug')} mono>
          {channel.slug}
        </KvRow>
        <KvRow label={t('channel.fields.kind')}>
          {t(`kind.${channel.kind}`, { defaultValue: channel.kind })}
        </KvRow>
        <KvRow label={t('channel.fields.protectionGeneration')} mono>
          {channel.protection_generation}
        </KvRow>
        <KvRow label={t('channel.fields.version')} mono>
          {channel.version}
        </KvRow>
        <KvRow label={t('channel.fields.etag')} mono>
          <span data-slot="config-etag">{etag}</span>
        </KvRow>
      </KvList>
      <p className="text-xs text-muted-foreground">{t('config.immutable')}</p>

      <Button
        type="button"
        variant="ghost"
        size="sm"
        className="self-start"
        aria-expanded={advanced}
        aria-controls={`${idp}-advanced`}
        onClick={() => setAdvanced((a) => !a)}
      >
        {advanced ? (
          <ChevronDown className="size-4" aria-hidden="true" />
        ) : (
          <ChevronRight className="size-4" aria-hidden="true" />
        )}
        {t('config.advanced')}
      </Button>
      {advanced ? (
        <div
          id={`${idp}-advanced`}
          className="grid gap-3 sm:grid-cols-2"
          data-slot="config-advanced"
        >
          {selectField(
            `${idp}-state`,
            t('channel.fields.state'),
            draft.state,
            (v) => update({ state: v }),
            CHANNEL_PATCH_STATES,
            (v) => t(`channelState.${v}`),
            (v) => archived && v !== 'archived',
            archived ? t('config.stateLocked') : t('config.stateHint'),
            archived,
          )}
          {selectField(
            `${idp}-sensitivity`,
            t('channel.fields.sensitivity'),
            draft.sensitivity,
            (v) => update({ sensitivity: v }),
            CHANNEL_SENSITIVITIES,
            (v) => t(`sensitivity.${v}`),
            (v) => channel.sensitivity === 'restricted' && v !== 'restricted',
            t('config.sensitivityHint'),
          )}
          {selectField(
            `${idp}-protection`,
            t('channel.fields.protection'),
            draft.protection,
            (v) => update({ protection: v }),
            CONTENT_PROTECTIONS,
            (v) => t(`protection.${v}`),
            (v) =>
              channel.content_protection === 'application_sealed' &&
              v !== 'application_sealed',
            t('config.protectionHint'),
          )}
          {selectField(
            `${idp}-ack`,
            t('channel.fields.ackPolicy'),
            draft.ackPolicy,
            (v) =>
              update({
                ackPolicy: v,
                ackTimeout:
                  v === 'none'
                    ? '0'
                    : draft.ackTimeout === '0'
                      ? ''
                      : draft.ackTimeout,
              }),
            ACK_POLICIES,
            (v) => t(`ackPolicy.${v}`),
            undefined,
            t('config.ackHint'),
          )}
          <Field
            label={t('channel.fields.ackTimeout')}
            htmlFor={`${idp}-ack-timeout`}
          >
            <Input
              id={`${idp}-ack-timeout`}
              value={draft.ackTimeout}
              onChange={(e) => update({ ackTimeout: e.target.value })}
              inputMode="numeric"
              mono
              disabled={phase !== 'edit'}
            />
          </Field>
          {selectField(
            `${idp}-wake`,
            t('channel.fields.wake'),
            draft.wake,
            (v) => update({ wake: v }),
            CHANNEL_WAKE_POLICIES,
            (v) => t(`wake.${v}`),
            undefined,
            t('config.wakeHint'),
          )}
          <Field
            label={t('channel.fields.retention')}
            htmlFor={`${idp}-retention`}
          >
            <Input
              id={`${idp}-retention`}
              value={draft.retention}
              onChange={(e) => update({ retention: e.target.value })}
              autoComplete="off"
              mono
              disabled={phase !== 'edit'}
            />
          </Field>
          <Field
            label={t('channel.fields.maxFanout')}
            htmlFor={`${idp}-fanout`}
          >
            <Input
              id={`${idp}-fanout`}
              value={draft.maxFanout}
              onChange={(e) => update({ maxFanout: e.target.value })}
              inputMode="numeric"
              mono
              disabled={phase !== 'edit'}
            />
          </Field>
          <Field
            label={t('channel.fields.maxAutomationDepth')}
            htmlFor={`${idp}-depth`}
          >
            <Input
              id={`${idp}-depth`}
              value={draft.maxDepth}
              onChange={(e) => update({ maxDepth: e.target.value })}
              inputMode="numeric"
              mono
              disabled={phase !== 'edit'}
            />
          </Field>
        </div>
      ) : null}

      {(phase === 'confirm' || phase === 'submitting') && intent ? (
        <div
          className={
            archiving
              ? 'flex flex-col gap-2 rounded-md border border-danger-line bg-danger-soft p-3'
              : 'flex flex-col gap-2 rounded-md border border-border bg-muted p-3'
          }
          data-slot="config-confirm"
          role="region"
          aria-label={t('config.confirm.title')}
        >
          <p className="text-sm font-medium">{t('config.confirm.title')}</p>
          <p className="text-sm text-muted-foreground">
            {t('config.confirm.body', { etag: intent.etag })}
          </p>
          {archiving ? (
            <p className="text-sm font-medium text-danger" role="alert">
              {t('config.confirm.archiving')}
            </p>
          ) : null}
          <KvList>
            {changedRows.map((r) => (
              <KvRow
                key={r.key}
                label={t(`config.fieldNames.${r.key}`, { defaultValue: r.key })}
                mono
                align="start"
              >
                <span className="break-words">{r.value || '—'}</span>
              </KvRow>
            ))}
            <KvRow label={t('config.confirm.ifMatch')} mono>
              {intent.etag}
            </KvRow>
          </KvList>
          {reading ? (
            <p className="text-xs text-muted-foreground" role="status">
              {t('states.reading')}
            </p>
          ) : null}
        </div>
      ) : null}
      {phase === 'conflict' && failure ? (
        <div data-slot="config-conflict">
          <FailureNotice failure={failure} title={t('config.conflictTitle')} />
          <p className="mt-1 text-sm text-muted-foreground">
            {t('config.conflictBody')}
          </p>
        </div>
      ) : null}
      {phase === 'unknown' && failure && intent ? (
        <div data-slot="config-unknown" className="flex flex-col gap-2">
          <FailureNotice failure={failure} title={t('config.unknownTitle')} />
          <p className="text-sm text-muted-foreground">
            {t('config.unknownBody')}
          </p>
          <KvList>
            {changedRows.map((r) => (
              <KvRow
                key={r.key}
                label={t(`config.fieldNames.${r.key}`, { defaultValue: r.key })}
                mono
                align="start"
              >
                <span className="break-words">{r.value || '—'}</span>
              </KvRow>
            ))}
            <KvRow label={t('config.confirm.ifMatch')} mono>
              {intent.etag}
            </KvRow>
          </KvList>
        </div>
      ) : null}
      {phase === 'refused' && failure ? (
        <FailureNotice failure={failure} title={t('config.refusedTitle')} />
      ) : null}
      {phase === 'applied' && outcome ? (
        <section
          aria-label={t('receipt.title')}
          className="flex flex-col gap-2"
          data-slot="config-receipt"
        >
          <div
            role="status"
            className="rounded-md border border-success-line bg-success-soft px-3 py-2 text-sm text-success"
          >
            <p className="font-medium">{t('config.appliedTitle')}</p>
            <p>{t('config.appliedBody')}</p>
          </div>
          <KvList>
            <KvRow label={t('receipt.version')} mono>
              {outcome.result.channel.version}
            </KvRow>
            <KvRow label={t('receipt.etag')} mono>
              {outcome.etag ?? outcome.result.etag}
            </KvRow>
            <KvRow label={t('receipt.auditSeq')} mono>
              {outcome.result.audit_seq}
            </KvRow>
            <KvRow label={t('channel.fields.state')}>
              <Badge variant="neutral">
                {t(`channelState.${outcome.result.channel.state}`, {
                  defaultValue: outcome.result.channel.state,
                })}
              </Badge>
            </KvRow>
          </KvList>
        </section>
      ) : null}
      {/* An established refusal keeps the existing calm line; a concealed non-verdict, a
          pending answer and a local unknown get the neutral state that diagnoses nothing. */}
      {mayPatch ? null : <CapabilityNotice access={patchAccess.access} />}

      <div className="flex flex-wrap justify-end gap-2">
        {phase === 'edit' || phase === 'applied' || phase === 'refused' ? (
          <>
            {dirty ? (
              <Button
                type="button"
                variant="ghost"
                onClick={() => {
                  setDraft(draftOf(channel))
                  setTouched(new Set())
                  setRebased([])
                  setErrors([])
                }}
              >
                {t('actions.revert')}
              </Button>
            ) : null}
            <Button
              type="submit"
              variant="primary"
              disabled={!mayPatch || !workspace || !dirty}
            >
              {t('actions.reviewChanges')}
            </Button>
          </>
        ) : null}
        {phase === 'confirm' ? (
          <>
            <Button type="button" variant="secondary" onClick={backToEdit}>
              {t('actions.back')}
            </Button>
            <Button
              type="button"
              variant={archiving ? 'destructive-solid' : 'primary'}
              onClick={confirm}
              disabled={!mayPatch || reading}
            >
              {archiving
                ? t('actions.confirmArchive')
                : t('actions.confirmChanges')}
            </Button>
          </>
        ) : null}
        {phase === 'submitting' ? (
          <Button type="button" variant="primary" disabled>
            <Spinner className="size-3.5" />
            {t('actions.applying')}
          </Button>
        ) : null}
        {phase === 'conflict' ? (
          <Button type="button" variant="primary" onClick={rereadAfterConflict}>
            {t('actions.rereadAndEdit')}
          </Button>
        ) : null}
        {phase === 'unknown' ? (
          <>
            <Button type="button" variant="ghost" onClick={backToEdit}>
              {t('actions.discardIntent')}
            </Button>
            <Button
              type="button"
              variant="primary"
              onClick={rereadAfterConflict}
            >
              {t('actions.rereadToVerify')}
            </Button>
          </>
        ) : null}
      </div>
    </form>
  )
}
