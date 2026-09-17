// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'
import { Plus, RefreshCcw } from 'lucide-react'
import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type FormEvent,
} from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
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
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toaster'
import { AuthorityLostError } from '@/features/agentops/auth-boundary'
import { ListTruncationBadge } from '@/features/_intel'
import {
  isEstablishedRefusal,
  useCapability,
  useCapabilityPreflight,
  type CapabilityAccess,
  type CapabilityPermit,
} from '@/lib/auth/capabilities'
import { formatDateTime } from '@/lib/format'
import {
  communicationsKeys,
  grantChannel,
  listChannelGrants,
  revokeChannelGrant,
  UnadmittedReadError,
  type ChannelAdminOutcome,
  type GrantFilters,
} from './api'
import type { CommunicationsScope } from './boundary'
import {
  grantCreationQuestion,
  grantRevocationQuestion,
  grantSheetQuestion,
} from './capabilities'
import { CapabilityNotice } from './capability-notice'
import { useChannelDraftCapsule } from './channel-admin-continuity'
import { ChannelConfigForm, type ChannelDraftHold } from './channel-config-form'
import { ChannelGrantForm, type GrantDraft } from './channel-grant-form'
import { Mono } from './content-blocks'
import { classifyFailure, type Failure } from './errors'
import { FailureNotice } from './failure-notice'
import {
  buildGrantIntent,
  buildRevokeIntent,
  permittedDispatchGuard,
  StaleIntentError,
  useIntentGuard,
  type GrantIntent,
  type RevokeIntent,
} from './intent'
import { useReturnFocus } from './return-focus'
import type { Me } from './subject-picker'
import {
  GRANT_STATE_FILTERS,
  GRANT_SUBJECT_KINDS,
  PAGE_LIMIT_DEFAULT,
  PAGE_LIMITS,
  type ChannelGrant,
  type GrantAdministrationItem,
  type GrantCreateInput,
  type GrantStateFilter,
  type GrantSubjectFilterKind,
} from './types'

type SheetTab = 'config' | 'grants'
type ActPhase =
  | 'form'
  | 'confirm'
  | 'submitting'
  | 'applied'
  | 'conflict'
  | 'unknown'
  | 'refused'
type Act =
  | {
      kind: 'grant'
      initial?: GrantDraft
      /** The generation this grant SUCCEEDS, when the operator chose revoke → grant. */
      successorOf?: string
    }
  | { kind: 'revoke'; grant: ChannelGrant; thenGrant: boolean }

const NONE = '__none__'

/**
 * WHICH SENTENCE CLOSES AN ACT. Same rule as the configuration form, applied to this
 * sheet's three questions: only an ESTABLISHED refusal may be reported as a lost
 * permission. A concealed non-verdict, an expiry, a movement or a transport that did not
 * answer all arrive at the same branch under the migration, and naming a cause for them
 * states something the engine never published — for `undisclosed`, the exact inference
 * the concealment exists to prevent. A closure that arrives while an act is already
 * SUBMITTING additionally may not promise that nothing was sent: the composed dispatch
 * guard settles that immediately before the bytes and this render does not know its
 * verdict.
 */
function closureLine(access: CapabilityAccess, submitting: boolean): string {
  if (isEstablishedRefusal(access)) return 'authority.confirmationClosed'
  return submitting
    ? 'capability.actInterrupted'
    : 'capability.confirmationClosed'
}

function BitBadges({ grant }: { grant: ChannelGrant }) {
  const { t } = useTranslation('communications')
  const bits = (['read', 'write', 'admin'] as const).filter(
    (b) => grant[`can_${b}`],
  )
  return (
    <span className="inline-flex flex-wrap gap-1">
      {bits.map((b) => (
        <Badge key={b} variant="info">
          {t(`access.${b}`)}
        </Badge>
      ))}
    </span>
  )
}

/**
 * ChannelAdminSheet — the administrative sheet of ONE Channel, opened from the
 * administrable catalog or from the URL (`admin_channel=<id>`). Its read is
 * `GET /channels/{id}/grants` — the administrable Channel, its precondition ETag,
 * `observed_at` and one page of STORED grant generations — and never the read-tier
 * `GET /channels/{id}`: an administrator without a local read bit must reach it.
 * The read is fresh on open, on focus and on every explicit re-read, cancelled and
 * dropped on close; a 403/404/503 replaces every row and closes every act, because
 * nothing on screen was authorized for the next answer.
 *
 * Two tabs. CONFIGURATION is `PATCH /channels` from the Channel of this read, under
 * its ETag. GRANTS is the history — persisted-state and exact-subject filters,
 * pages on the server's own continuation, the stored state beside the temporal
 * state at `observed_at` — and the two administrative acts: a new generation
 * (`POST …/grants`) and the revocation of the exact generation shown
 * (`POST …/grants/{id}/revoke`), each under the Channel ETag on screen. Changing
 * a subject's rights is TWO visible acts: revoke the current generation, read the
 * result, then confirm a successor; the console holds no transaction over them.
 *
 * A 409 `channel_snapshot_changed` between two pages discards every page and
 * restarts the listing without a continuation. A 409/412/428 on an act kills the
 * intention: re-read, confirm again. A lost response is "result pending
 * verification": the request is shown frozen, the history is re-read, and the
 * operator decides — there is no retry and no idempotency key on these routes.
 *
 * While its observation is momentarily `checking` or `unknown` for the SAME channel,
 * the sheet keeps nothing of the engine's answer and one thing of the operator's: the
 * fields they typed, handed back to the configuration form when a new authorized read
 * remounts it. Any confirmation open at that moment closes, and is said out loud.
 */
export function ChannelAdminSheet({
  open,
  onOpenChange,
  channelId,
  scope,
  canUserRead,
  canAgentRead,
  me,
  onMutated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  channelId: string | null
  scope: CommunicationsScope
  canUserRead: boolean
  canAgentRead: boolean
  me: Me
  /** A 200 was read: the owner invalidates the administrable catalog and the I1
   * reads of the same scope. */
  onMutated: () => void
}) {
  const { t, i18n } = useTranslation('communications')
  const idp = useId()
  const queryClient = useQueryClient()
  const returnFocus = useReturnFocus(open)
  const tenant = scope.tenant
  const workspace = scope.workspace ?? ''

  const [tab, setTab] = useState<SheetTab>('config')
  const [limit, setLimit] = useState<number>(PAGE_LIMIT_DEFAULT)
  const [stateFilter, setStateFilter] = useState<GrantStateFilter>('active')
  const [subjectKind, setSubjectKind] = useState<string>(NONE)
  const [subjectRef, setSubjectRef] = useState('')
  const [applied, setApplied] = useState<{
    kind: GrantSubjectFilterKind
    ref: string
  } | null>(null)
  const [selected, setSelected] = useState<string | null>(null)
  const [act, setAct] = useState<Act | null>(null)
  const [phase, setPhase] = useState<ActPhase>('form')
  const [grantIntent, setGrantIntent] = useState<GrantIntent | null>(null)
  const [revokeIntent, setRevokeIntent] = useState<RevokeIntent | null>(null)
  const [outcome, setOutcome] = useState<ChannelAdminOutcome | null>(null)
  const [failure, setFailure] = useState<Failure | null>(null)
  const [successor, setSuccessor] = useState<GrantDraft | null>(null)
  const [restarts, setRestarts] = useState(0)
  const [closure, setClosure] = useState<{ n: number; line: string } | null>(
    null,
  )

  const resetActs = () => {
    setAct(null)
    setPhase('form')
    setGrantIntent(null)
    setRevokeIntent(null)
    setOutcome(null)
    setFailure(null)
    setSuccessor(null)
  }
  // Another Channel in the same sheet is another sheet: nothing of the previous one
  // survives a paint (adjust-during-render).
  const [seenChannel, setSeenChannel] = useState(channelId)
  if (seenChannel !== channelId) {
    setSeenChannel(channelId)
    setTab('config')
    setSelected(null)
    setApplied(null)
    setSubjectKind(NONE)
    setSubjectRef('')
    setStateFilter('active')
    setRestarts(0)
    resetActs()
  }

  const filters = useMemo<GrantFilters>(
    () =>
      applied
        ? {
            state: stateFilter,
            limit,
            subject_kind: applied.kind,
            subject_ref: applied.ref,
          }
        : { state: stateFilter, limit },
    [applied, stateFilter, limit],
  )
  const queryKey = communicationsKeys.grants(
    tenant,
    scope.epoch,
    workspace,
    channelId ?? '',
    filters,
  )
  // ⛔ THREE QUESTIONS, THREE DECISIONS, AND NOT ONE OF THEM IMPLIES ANOTHER.
  //
  //    · READ THE SHEET — the exact `GET /channels/{id}/grants` on THIS channel. It is
  //      what a deep link needs and all it needs: the administration COLLECTION is a
  //      different route with a different answer, and being refused the list says nothing
  //      about being refused this row. Nothing here consults a read tier or `my_access`.
  //    · ADD A GRANT — the exact `POST …/grants` on this channel.
  //    · REVOKE — the exact `POST …/grants/{grant_id}/revoke` for the SELECTED generation.
  //      Asking once per channel and painting every row with the answer would project one
  //      generation's authority onto all of them, so the question carries the grant id and
  //      changes when the selection does.
  const sheetAccess = useCapability(grantSheetQuestion(workspace, channelId))
  const grantAccess = useCapability(grantCreationQuestion(workspace, channelId))
  const revokeAccess = useCapability(
    grantRevocationQuestion(workspace, channelId, selected),
  )
  const preflight = useCapabilityPreflight()
  const sheetAllowed = sheetAccess.access === 'allowed'
  const mayGrant = grantAccess.access === 'allowed'
  const mayRevoke = revokeAccess.access === 'allowed'

  const enabled = open && channelId !== null && sheetAllowed && workspace !== ''
  const query = useInfiniteQuery({
    queryKey,
    // ⛔ AND THE SHEET'S OWN ADMISSION TRAVELS WITH THE REQUEST. `enabled` is a render's
    //    decision; the bytes leave later — after a filter change, a next page, an
    //    explicit reread, the reread that follows a 409 restart, a focus refetch, and
    //    inside the shared client after an AWAITED credential refresh and before the
    //    single 401 replay. The permit of THIS channel's `GET …/grants` goes down to the
    //    transport so every one of those gaps is covered: expiry, an owner that moved,
    //    or a permit that answers another channel, workspace or operation refuses there
    //    with zero bytes. The same omission the collection had, and the same seam.
    queryFn: ({ signal, pageParam }) =>
      listChannelGrants(
        channelId ?? '',
        { workspace_id: workspace, ...filters, continuation: pageParam },
        { tenant, admission: sheetAccess.permit },
        signal,
      ),
    initialPageParam: undefined as string | undefined,
    // `last?` — a page that is not there is not a page with a successor. React
    // Query asks this again on every render, including renders where the chain was
    // just reset and the last entry is momentarily absent; reading through it
    // threw and took the whole sheet down. "No page" answers "no continuation",
    // which is the deny-closed answer and never invents one.
    getNextPageParam: (last) =>
      last?.has_more && last.continuation ? last.continuation : undefined,
    enabled,
    // FRESH: never painted from memory after the sheet closed or the window
    // returned; a refusal is never replayed from a cache.
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: true,
    retry: false,
  })

  // Closing the sheet (or losing the Channel) cancels the read in flight and drops
  // what it fetched: a late page has nowhere to land.
  const lastKeyRef = useRef<readonly unknown[] | null>(null)
  useEffect(() => {
    if (enabled) {
      lastKeyRef.current = queryKey
      return
    }
    const prev = lastKeyRef.current
    if (prev) {
      void queryClient.cancelQueries({ queryKey: prev })
      queryClient.removeQueries({ queryKey: prev })
      lastKeyRef.current = null
    }
  }, [enabled, queryKey, queryClient])
  useEffect(
    () => () => {
      const prev = lastKeyRef.current
      if (prev) {
        void queryClient.cancelQueries({ queryKey: prev })
        queryClient.removeQueries({ queryKey: prev })
      }
    },
    [queryClient],
  )

  // ⛔ A LOCAL REFUSAL IS NOT AN ANSWER FROM THE ENGINE. `classifyFailure` reports what
  //    the engine said, and when the guard refuses nothing was sent, so there is nothing
  //    it can report. It is not on a retry cadence either — this read already carries
  //    `retry: false` — and only a NEW exact admission resumes it, as a render. The
  //    sheet's reaction is the one it already has for a lost admission: nothing painted.
  const refusedLocally = query.error instanceof UnadmittedReadError
  const readFailure =
    query.error && !refusedLocally ? classifyFailure(query.error) : null
  // A snapshot that moved between two pages: EVERY page collected so far is
  // discarded and the listing restarts with no continuation (a read is safe to
  // restart; nothing is mixed). Keyed on the error object so it runs once per 409.
  const restartedFor = useRef<unknown>(null)
  useEffect(() => {
    if (
      readFailure?.kind === 'snapshot_changed' &&
      restartedFor.current !== query.error
    ) {
      restartedFor.current = query.error
      setRestarts((n) => n + 1)
      setSelected(null)
      void queryClient.resetQueries({ queryKey, exact: true })
    }
  }, [readFailure, query.error, queryClient, queryKey])

  // ⛔ AND NOT ONE PAINT PAST THE POSITIVE THAT AUTHORIZED IT. The effect below cancels
  //    and REMOVES the page as soon as the read stops being enabled, but a removal is not
  //    a render: until the next one this sheet would keep painting a page it no longer
  //    holds — the previous Channel, its ETag and its grant history — through an
  //    admission the console can no longer vouch for. Measured in the component harness:
  //    the whole configuration form, ETag and rows survived the transition to a local
  //    `unknown` and only vanished on the following render. So the admission is part of
  //    the derivation and not only of `enabled`: no current positive, nothing on screen.
  const page0 =
    !sheetAllowed ||
    refusedLocally ||
    (readFailure && readFailure.kind !== 'snapshot_changed')
      ? null
      : (query.data?.pages[0] ?? null)
  const channel = page0?.channel ?? null
  const etag = page0?.etag ?? null
  const observedAt = page0?.observed_at ?? null
  const rows = useMemo<GrantAdministrationItem[]>(
    () => (page0 ? (query.data?.pages.flatMap((p) => p.items) ?? []) : []),
    [page0, query.data],
  )
  const lastPage = page0
    ? query.data?.pages[query.data.pages.length - 1]
    : undefined
  const selectedItem = rows.find((r) => r.grant.id === selected) ?? null

  // ⛔ A TRANSIENT NON-ANSWER IS NOT A REFUSAL, AND IT MAY NOT SPEND THE OPERATOR'S WORK.
  //    `checking` is "not asked yet" and `unknown` is this CLIENT's own verdict on an
  //    expiry, a movement, a malformed body or a transport that did not answer. The engine
  //    decided nothing in either, and this sheet's own reaction is right and stays exactly
  //    as it is: without a current positive the read is not enabled, the page in memory is
  //    cancelled and REMOVED, and no byte of the previous answer is painted again.
  //
  //    What that reaction may not do is spend the operator's typing, and it did: measured
  //    on G1-B, a confirmation prepared over a name they had typed disappeared 4,3 s later
  //    with the engine's name back in the field, the ETag unchanged and nothing sent.
  //
  //    ⛔ AND THE HOLD DOES NOT LIVE HERE ANY MORE, because this sheet does not survive the
  //       event. Measured on a real estate: the ROUTE GATE unmounts the entire
  //       administration view for 34–46 ms at every budget edge, so a hold owned by the
  //       sheet — or by anything under that cut — dies with the very teardown it exists to
  //       cross. It lives in the route's continuity boundary
  //       (`channel-admin-continuity.tsx`), ABOVE the cut; this sheet is one of its
  //       consumers and holds nothing of its own.
  //
  //    This sheet's part is to publish what the form reports, to hand the form back what
  //    belongs to THIS channel, and to end the opening on the events the boundary cannot
  //    see: an explicit close, a failed read, an answer that is not a positive, another
  //    channel.
  const capsule = useChannelDraftCapsule()
  const capsuleGeneration = capsule.generation
  const transientGap =
    sheetAccess.access === 'checking' || sheetAccess.access === 'unknown'
  // Two booleans, and deliberately not the draft: they say whether a confirmation was
  // prepared when this sheet last heard from the form, which is what decides the sentence
  // below. The operator's text is the capsule's and is never mirrored here.
  const marksRef = useRef({ confirming: false, submitting: false })
  const keepDraft = useCallback(
    (hold: ChannelDraftHold, focus: string | null) => {
      marksRef.current = {
        confirming: hold.confirming,
        submitting: hold.submitting,
      }
      if (channelId) capsule.publish(channelId, hold, capsuleGeneration, focus)
    },
    [capsule, capsuleGeneration, channelId],
  )
  const takeHold = useCallback(
    () => (channelId ? capsule.take(channelId, capsuleGeneration) : null),
    [capsule, capsuleGeneration, channelId],
  )
  // Whether THIS mount is a recovery: the capsule already holds something for this
  // channel, so the operator was in this room a moment ago and is being put back into it.
  // Read once, at mount, and never used to enable anything — see the auto-focus below.
  const [recovering] = useState(
    () =>
      channelId !== null && capsule.take(channelId, capsuleGeneration) !== null,
  )

  // ⛔ EVERY END IS A TRANSITION, NEVER A STATE, AND THE DIFFERENCE IS THE WHOLE FEATURE.
  //    An unmount of this sheet is NOT an operator's close: at a budget edge it is exactly
  //    what happens to a sheet whose operator did nothing at all. So an opening ends when
  //    the console ENTERS one of these conditions while mounted to see it — and a
  //    teardown, which renders nothing, ends nothing.
  const mustEnd =
    !open ||
    channelId === null ||
    readFailure !== null ||
    (!sheetAllowed && !transientGap)
  const endedRef = useRef(false)
  useEffect(() => {
    if (mustEnd && !endedRef.current)
      capsule.discard('closed', capsuleGeneration)
    endedRef.current = mustEnd
  }, [mustEnd, capsule, capsuleGeneration])
  // Another channel — or another boundary/workspace — is another opening, and returning to
  // the first resurrects nothing. The capsule fences the same move on `CapabilityContext`,
  // which is the authoritative identity; this is the same fact seen from the surface that
  // owns the channel, and neither defence depends on the other.
  const openingKey = `${channelId ?? ''}|${scope.key}`
  const capsuleOpeningRef = useRef(openingKey)
  useEffect(() => {
    if (capsuleOpeningRef.current === openingKey) return
    capsuleOpeningRef.current = openingKey
    capsule.discard('channel', capsuleGeneration)
  }, [openingKey, capsule, capsuleGeneration])

  // A CONFIRMATION THAT VANISHES IN SILENCE IS THE DEFECT, not the closure. When THIS
  // sheet is the one that lost the answer — its own entity question expired while the
  // route's did not — the form goes away under a mounted sheet, and the sheet says so at
  // once: judged on the SHEET's answer, never as a permission loss, and never promising
  // that nothing left when the act was already being sent.
  //
  // The marks are cleared as they are spoken, so the form does not say it a second time
  // when it comes back. A teardown of this whole sheet reaches neither branch, which is
  // why the capsule carries the marks and the form announces them on recovery.
  const configMounted = channel !== null && etag !== null
  useEffect(() => {
    if (configMounted || !channelId) return
    const marks = marksRef.current
    if (!marks.confirming && !marks.submitting) return
    // Said once: the marks go now, here and in the capsule, so neither this sheet nor a
    // later mount of the form repeats it.
    marksRef.current = { confirming: false, submitting: false }
    const held = capsule.take(channelId, capsuleGeneration)
    if (held)
      capsule.publish(
        channelId,
        { ...held.hold, confirming: false, submitting: false },
        capsuleGeneration,
        held.focus,
      )
    setClosure((c) => ({
      n: (c?.n ?? 0) + 1,
      line: closureLine(sheetAccess.access, marks.submitting),
    }))
  }, [configMounted, channelId, capsule, capsuleGeneration, sheetAccess.access])

  // ⛔ NO `permission` ARGUMENT ANY MORE, AND THAT IS THE MIGRATION. The dispatch-time
  //    RBAC membership re-check is REPLACED — not merely supplemented — by the exact
  //    capability preflight below, which asks about this operation on this resource in
  //    this workspace instead of about a tenant-wide string. `allowed` is this sheet's own
  //    current read admission; each act additionally preflights its own exact operation.
  //    Every unmigrated I1/cursor caller keeps its `permission`/`alsoRequires` branch.
  const guard = useIntentGuard({ allowed: sheetAllowed, boundary: scope.key })
  /**
   * The exact preflight of ONE act, run at CONFIRM and not at render: a fresh observation
   * of that operation, under the confirmation's own signal and the surface's dispatch
   * guard, refused unless it is a current positive. It throws this feature's calm typed
   * refusal, so a "no" here sends zero bytes and closes the confirmation.
   */
  const preflightFor = async (
    question: ReturnType<typeof grantCreationQuestion>,
    signal: AbortSignal,
  ): Promise<CapabilityPermit> => {
    if (!question) throw new StaleIntentError('capability')
    const permit = await preflight.request(question, {
      signal,
      dispatchGuard: guard.check,
    })
    if (!permit) throw new StaleIntentError('capability')
    return permit
  }
  // The act on screen has its OWN authority, and losing it closes the confirmation even
  // while the sheet itself stays administrable: a revoke permit that expired is not
  // covered by a grant permit that has not. An act already SUBMITTING is governed by the
  // composed dispatch guard instead — that is the check that runs immediately before the
  // bytes, and tearing its state down from here would race it.
  const actAllowed =
    act === null ? true : act.kind === 'revoke' ? mayRevoke : mayGrant
  // ⛔ THE DECIDING ANSWER, NOT JUST ANY ANSWER. Three independent questions live in this
  //    sheet, and reporting a cause from the wrong one would be a fabrication of the same
  //    family as reporting a cause nobody published.
  const sheetLost =
    !sheetAllowed && (phase === 'confirm' || phase === 'submitting')
  const actLost = !actAllowed && phase === 'confirm'
  if (sheetLost || actLost) {
    const deciding: CapabilityAccess = sheetLost
      ? sheetAccess.access
      : act?.kind === 'revoke'
        ? revokeAccess.access
        : grantAccess.access
    resetActs()
    setClosure((c) => ({
      n: (c?.n ?? 0) + 1,
      line: closureLine(deciding, phase === 'submitting'),
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

  const onActError = (err: unknown) => {
    if (err instanceof StaleIntentError && err.moved === 'capability') {
      resetActs()
      setUnconfirmedCount((n) => n + 1)
      return
    }
    if (err instanceof AuthorityLostError) {
      resetActs()
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
      setGrantIntent(null)
      setRevokeIntent(null)
      setPhase('conflict')
    } else if (f.kind === 'ambiguous') {
      setPhase('unknown')
    } else {
      setGrantIntent(null)
      setRevokeIntent(null)
      setPhase('refused')
    }
  }
  const afterApplied = (o: ChannelAdminOutcome) => {
    setOutcome(o)
    setPhase('applied')
    onMutated()
    // The durable state, read again: the receipt is the act's, the rows are the
    // history's, and the next act needs the ETag of THIS read.
    void query.refetch()
  }
  const grant = useMutation<ChannelAdminOutcome, unknown, GrantIntent>({
    mutationFn: async (i) => {
      const signal = guard.begin()
      if (!signal) throw new AuthorityLostError()
      const permit = await preflightFor(
        grantCreationQuestion(i.scope.workspace, i.channelId),
        signal,
      )
      return grantChannel(
        i,
        {
          tenant: i.scope.tenant,
          guard: permittedDispatchGuard(guard, permit),
        },
        signal,
      )
    },
    onSuccess: (o) => {
      setGrantIntent(null)
      afterApplied(o)
    },
    onError: onActError,
  })
  const revoke = useMutation<ChannelAdminOutcome, unknown, RevokeIntent>({
    mutationFn: async (i) => {
      const signal = guard.begin()
      if (!signal) throw new AuthorityLostError()
      const permit = await preflightFor(
        grantRevocationQuestion(i.scope.workspace, i.channelId, i.grantId),
        signal,
      )
      return revokeChannelGrant(
        i,
        {
          tenant: i.scope.tenant,
          guard: permittedDispatchGuard(guard, permit),
        },
        signal,
      )
    },
    onSuccess: (o, i) => {
      setRevokeIntent(null)
      if (act?.kind === 'revoke' && act.thenGrant) {
        const g = act.grant
        setSuccessor({
          subject: { kind: g.subject.kind, ref: g.subject.ref },
          read: g.can_read,
          write: g.can_write,
          admin: g.can_admin,
          expiresAt: '',
        })
      }
      afterApplied(o)
      setSelected(i.grantId)
    },
    onError: onActError,
  })

  const beginGrant = (initial?: GrantDraft, successorOf?: string) => {
    resetActs()
    setAct({ kind: 'grant', initial, successorOf })
    setPhase('form')
  }
  const reviewGrant = (body: GrantCreateInput) => {
    if (!channel || !etag || !workspace) return
    setGrantIntent(
      buildGrantIntent(
        { tenant, workspace, boundary: scope.key },
        channel.id,
        etag,
        body,
      ),
    )
    setFailure(null)
    setOutcome(null)
    setPhase('confirm')
  }
  const beginRevoke = (g: ChannelGrant, thenGrant: boolean) => {
    if (!channel || !etag || !workspace) return
    resetActs()
    setAct({ kind: 'revoke', grant: g, thenGrant })
    setRevokeIntent(
      buildRevokeIntent(
        { tenant, workspace, boundary: scope.key },
        channel.id,
        g.id,
        etag,
      ),
    )
    setPhase('confirm')
  }
  const confirmAct = () => {
    if (!sheetAllowed || query.isFetching) return
    if (act?.kind === 'grant' && grantIntent) {
      setPhase('submitting')
      grant.mutate(grantIntent)
    } else if (act?.kind === 'revoke' && revokeIntent) {
      setPhase('submitting')
      revoke.mutate(revokeIntent)
    }
  }
  const cancelAct = () => {
    guard.end()
    resetActs()
  }
  const rereadAndDiscard = () => {
    guard.end()
    resetActs()
    void query.refetch()
  }
  const reread = () => {
    void query.refetch()
  }

  const applyFilter = (e: FormEvent) => {
    e.preventDefault()
    setSelected(null)
    if (subjectKind === NONE || subjectRef.trim() === '') {
      setApplied(null)
      return
    }
    setApplied({
      kind: subjectKind as GrantSubjectFilterKind,
      ref: subjectRef.trim(),
    })
  }

  const columns = useMemo<TableColumn<GrantAdministrationItem>[]>(
    () => [
      {
        id: 'subject',
        accessorFn: (r) => `${r.grant.subject.kind}:${r.grant.subject.ref}`,
        header: t('grants.columns.subject'),
        cell: ({ row }) => (
          <span className="inline-flex flex-wrap items-center gap-1">
            <Badge variant="outline">
              {t(`subject.kinds.${row.original.grant.subject.kind}`)}
            </Badge>
            <Mono>{row.original.grant.subject.ref}</Mono>
          </span>
        ),
      },
      {
        id: 'bits',
        accessorFn: (r) =>
          `${r.grant.can_read ? 'r' : ''}${r.grant.can_write ? 'w' : ''}${r.grant.can_admin ? 'a' : ''}`,
        header: t('grants.columns.bits'),
        cell: ({ row }) => <BitBadges grant={row.original.grant} />,
      },
      {
        id: 'generation',
        accessorFn: (r) => r.grant.generation,
        header: t('grants.columns.generation'),
        cell: ({ row }) => (
          <Mono>
            {row.original.grant.generation}
            {row.original.grant.supersedes_id ? ' ↑' : ''}
          </Mono>
        ),
      },
      {
        id: 'stored',
        accessorFn: (r) => r.grant.state,
        header: t('grants.columns.stored'),
        cell: ({ row }) => (
          <Badge variant="neutral">
            {t(`grantState.${row.original.grant.state}`, {
              defaultValue: row.original.grant.state,
            })}
          </Badge>
        ),
      },
      {
        id: 'temporal',
        accessorFn: (r) => r.temporal_state,
        header: t('grants.columns.temporal'),
        cell: ({ row }) => (
          <Badge
            variant={
              row.original.temporal_state === 'active'
                ? 'success'
                : row.original.temporal_state === 'expired'
                  ? 'warning'
                  : 'neutral'
            }
          >
            {t(`grantState.${row.original.temporal_state}`, {
              defaultValue: row.original.temporal_state,
            })}
          </Badge>
        ),
      },
      {
        id: 'expires',
        accessorFn: (r) => r.grant.expires_at ?? '',
        header: t('grants.columns.expires'),
        cell: ({ row }) =>
          formatDateTime(row.original.grant.expires_at, i18n.language),
      },
      {
        id: 'grantedBy',
        accessorFn: (r) =>
          `${r.grant.granted_by.kind}:${r.grant.granted_by.ref}`,
        header: t('grants.columns.grantedBy'),
        cell: ({ row }) => (
          <Mono>
            {row.original.grant.granted_by.kind}:
            {row.original.grant.granted_by.ref}
          </Mono>
        ),
      },
      {
        id: 'created',
        accessorFn: (r) => r.grant.created_at,
        header: t('grants.columns.created'),
        cell: ({ row }) =>
          formatDateTime(row.original.grant.created_at, i18n.language),
      },
    ],
    [t, i18n.language],
  )

  const busy = phase === 'submitting'
  const ownAdmin = (g: ChannelGrant) =>
    g.subject.kind === 'user' && g.subject.ref === me.userId && g.can_admin
  const actGrant = act?.kind === 'grant' ? act : null
  const actRevoke = act?.kind === 'revoke' ? act : null
  const receiptGrant = outcome?.result.grant ?? null

  return (
    <Sheet
      open={open}
      onOpenChange={(o) => (busy ? undefined : onOpenChange(o))}
    >
      <SheetContent
        className="flex w-full flex-col gap-0 p-0 sm:max-w-3xl"
        data-slot="channel-admin-sheet"
        // ⛔ A ROOM THAT COMES BACK DOES NOT ANNOUNCE ITSELF AGAIN. Opening a sheet moves
        //    focus into it, which is right when the OPERATOR opened it. On a recovery
        //    mount they never left: the room was taken from under them by a refresh, and
        //    grabbing the caret would overwrite wherever they went in the meantime. So the
        //    dialog's own auto-focus stands down for that one case, and the configuration
        //    form puts the caret back only if nobody moved it.
        onOpenAutoFocus={recovering ? (e) => e.preventDefault() : undefined}
        onCloseAutoFocus={returnFocus}
      >
        <SheetHeader className="shrink-0 border-b border-border bg-surface px-6 pt-6 pb-3">
          <SheetTitle className="flex flex-wrap items-center gap-2">
            <span>{channel ? channel.name : t('admin.title')}</span>
            {channel ? (
              <Badge
                variant={channel.state === 'archived' ? 'warning' : 'neutral'}
              >
                {t(`channelState.${channel.state}`, {
                  defaultValue: channel.state,
                })}
              </Badge>
            ) : null}
          </SheetTitle>
          <SheetDescription>
            {channel ? (
              <span className="inline-flex flex-wrap items-center gap-2">
                <Mono>{channel.slug}</Mono>
                <span>·</span>
                <span>{t('admin.description')}</span>
              </span>
            ) : (
              t('admin.description')
            )}
          </SheetDescription>
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            {query.isFetching ? (
              <span role="status" className="inline-flex items-center gap-1">
                <Spinner className="size-3" />
                {t('states.reading')}
              </span>
            ) : observedAt ? (
              <span>
                {t('admin.observedAt')}:{' '}
                {formatDateTime(observedAt, i18n.language)}
              </span>
            ) : null}
            {etag ? (
              <span>
                {t('channel.fields.etag')}:{' '}
                <Mono>
                  <span data-slot="admin-etag">{etag}</span>
                </Mono>
              </span>
            ) : null}
          </div>
        </SheetHeader>

        <div className="flex-1 overflow-y-auto px-6 py-4">
          {/* ⛔ FOUR ANSWERS, NOT TWO. `Forbidden` is reserved for an ESTABLISHED refusal
              of this exact operation; a concealed non-verdict gets the neutral state that
              asserts no denial, no missing target, no outage and no missing read; a
              pending or local-unknown answer says so and keeps retrying. Rendering any of
              the last three as `Forbidden` would tell the operator something the engine
              did not say. */}
          {sheetAllowed ? null : (
            <CapabilityNotice access={sheetAccess.access} />
          )}
          {query.isLoading && !readFailure ? (
            <div role="status" aria-busy="true">
              <span className="sr-only">{t('states.reading')}</span>
              <Skeleton className="h-40 w-full" />
            </div>
          ) : null}
          {readFailure && readFailure.kind !== 'snapshot_changed' ? (
            <div className="flex flex-col gap-2" data-slot="admin-read-failure">
              <FailureNotice
                failure={readFailure}
                title={t('admin.readFailedTitle')}
              />
              <p className="text-sm text-muted-foreground">
                {t('admin.readFailedBody')}
              </p>
            </div>
          ) : null}
          {restarts > 0 ? (
            <div
              role="status"
              className="mb-3 rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-sm text-warning"
              data-slot="admin-snapshot-restarted"
            >
              {t('grants.snapshotChanged', { count: restarts })}
            </div>
          ) : null}
          {channel && etag ? (
            <Tabs value={tab} onValueChange={(v) => setTab(v as SheetTab)}>
              <TabsList>
                <TabsTrigger value="config">
                  {t('admin.tabs.config')}
                </TabsTrigger>
                <TabsTrigger value="grants">
                  {t('admin.tabs.grants')}
                </TabsTrigger>
              </TabsList>
              <TabsContent value="config" className="mt-4">
                <ChannelConfigForm
                  key={`${channel.id}|${scope.key}`}
                  channel={channel}
                  etag={etag}
                  scope={scope}
                  reading={query.isFetching}
                  takeHold={takeHold}
                  onHold={keepDraft}
                  onReread={reread}
                  onApplied={() => {
                    onMutated()
                    void query.refetch()
                  }}
                />
              </TabsContent>
              <TabsContent value="grants" className="mt-4">
                <div className="flex flex-col gap-4" data-slot="admin-grants">
                  <form
                    onSubmit={applyFilter}
                    className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4"
                    aria-label={t('grants.filters.title')}
                  >
                    <Field
                      label={t('grants.filters.state')}
                      htmlFor={`${idp}-gstate`}
                      description={t('grants.filters.stateHint')}
                    >
                      <Select
                        value={stateFilter}
                        onValueChange={(v) => {
                          setSelected(null)
                          setStateFilter(v as GrantStateFilter)
                        }}
                      >
                        <SelectTrigger
                          id={`${idp}-gstate`}
                          aria-label={t('grants.filters.state')}
                        >
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {GRANT_STATE_FILTERS.map((s) => (
                            <SelectItem key={s} value={s}>
                              {t(`grants.stateFilters.${s}`)}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </Field>
                    <Field
                      label={t('grants.filters.subjectKind')}
                      htmlFor={`${idp}-gkind`}
                    >
                      <Select
                        value={subjectKind}
                        onValueChange={setSubjectKind}
                      >
                        <SelectTrigger
                          id={`${idp}-gkind`}
                          aria-label={t('grants.filters.subjectKind')}
                        >
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value={NONE}>
                            {t('grants.filters.anySubject')}
                          </SelectItem>
                          {GRANT_SUBJECT_KINDS.map((k) => (
                            <SelectItem key={k} value={k}>
                              {t(`subject.kinds.${k}`)}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </Field>
                    <Field
                      label={t('grants.filters.subjectRef')}
                      htmlFor={`${idp}-gref`}
                      description={t('grants.filters.subjectHint')}
                    >
                      <Input
                        id={`${idp}-gref`}
                        value={subjectRef}
                        onChange={(e) => setSubjectRef(e.target.value)}
                        autoComplete="off"
                        mono
                        disabled={subjectKind === NONE}
                      />
                    </Field>
                    <div className="flex flex-wrap items-end gap-2">
                      <Select
                        value={String(limit)}
                        onValueChange={(v) => {
                          setSelected(null)
                          setLimit(Number(v))
                        }}
                      >
                        <SelectTrigger
                          className="w-24"
                          aria-label={t('actions.pageSize')}
                          id={`${idp}-glimit`}
                        >
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {PAGE_LIMITS.map((n) => (
                            <SelectItem key={n} value={String(n)}>
                              {n}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                      <Button type="submit" variant="outline" size="sm">
                        {t('actions.applyFilter')}
                      </Button>
                    </div>
                  </form>
                  {applied ? (
                    <p className="text-xs text-muted-foreground" role="status">
                      {t('grants.filters.applied', {
                        kind: t(`subject.kinds.${applied.kind}`),
                        ref: applied.ref,
                      })}
                    </p>
                  ) : null}
                  <ListTruncationBadge
                    query={{ data: lastPage, error: undefined }}
                    label={t('grants.truncated')}
                    hint={t('grants.truncatedHint')}
                    filas={rows.length}
                  />
                  <div className="overflow-x-auto">
                    <DataTable
                      columns={columns}
                      data={rows}
                      isLoading={query.isLoading}
                      getRowId={(r) => r.grant.id}
                      onRowClick={(r) => setSelected(r.grant.id)}
                      label={t('admin.tabs.grants')}
                      // Every loaded generation stays in the DOM: the sheet scrolls
                      // locally, the grid is walkable by keyboard end to end, and a
                      // page count is a count of rows, not of a viewport.
                      virtualized={false}
                      hasMore={query.hasNextPage}
                      onLoadMore={() => void query.fetchNextPage()}
                      isFetchingMore={query.isFetchingNextPage}
                      empty={
                        <EmptyState
                          title={t('grants.emptyTitle')}
                          description={t('grants.emptyBody')}
                        />
                      }
                    />
                  </div>
                  <p className="text-xs text-muted-foreground">
                    {t('grants.temporalHint')}
                  </p>

                  {selectedItem ? (
                    <section
                      aria-label={t('grants.detail.title')}
                      className="flex flex-col gap-2 rounded-md border border-border p-3"
                      data-slot="grant-detail"
                    >
                      <p className="text-sm font-medium">
                        {t('grants.detail.title')}
                      </p>
                      <KvList>
                        <KvRow label={t('grants.detail.id')} mono align="start">
                          <span className="break-all">
                            {selectedItem.grant.id}
                          </span>
                        </KvRow>
                        <KvRow label={t('grants.columns.subject')} mono>
                          {selectedItem.grant.subject.kind}:
                          {selectedItem.grant.subject.ref}
                        </KvRow>
                        <KvRow label={t('grants.columns.bits')}>
                          <BitBadges grant={selectedItem.grant} />
                        </KvRow>
                        <KvRow label={t('grants.columns.generation')} mono>
                          {selectedItem.grant.generation}
                        </KvRow>
                        <KvRow label={t('grants.detail.supersedes')} mono>
                          {selectedItem.grant.supersedes_id ?? '—'}
                        </KvRow>
                        <KvRow label={t('grants.columns.stored')} mono>
                          {selectedItem.grant.state}
                        </KvRow>
                        <KvRow label={t('grants.columns.temporal')} mono>
                          {selectedItem.temporal_state}
                        </KvRow>
                        <KvRow label={t('grants.columns.expires')}>
                          {formatDateTime(
                            selectedItem.grant.expires_at,
                            i18n.language,
                          )}
                        </KvRow>
                        <KvRow label={t('grants.columns.grantedBy')} mono>
                          {selectedItem.grant.granted_by.kind}:
                          {selectedItem.grant.granted_by.ref}
                        </KvRow>
                        <KvRow label={t('grants.detail.revokedBy')} mono>
                          {selectedItem.grant.revoked_by
                            ? `${selectedItem.grant.revoked_by.kind}:${selectedItem.grant.revoked_by.ref}`
                            : '—'}
                        </KvRow>
                        <KvRow label={t('grants.detail.version')} mono>
                          {selectedItem.grant.version}
                        </KvRow>
                        <KvRow label={t('grants.columns.created')}>
                          {formatDateTime(
                            selectedItem.grant.created_at,
                            i18n.language,
                          )}
                        </KvRow>
                        <KvRow label={t('grants.detail.updated')}>
                          {formatDateTime(
                            selectedItem.grant.updated_at,
                            i18n.language,
                          )}
                        </KvRow>
                      </KvList>
                      {selectedItem.grant.state === 'active' &&
                      selectedItem.temporal_state === 'expired' ? (
                        <p className="text-xs text-warning" role="status">
                          {t('grants.detail.expiredActive')}
                        </p>
                      ) : null}
                      {selectedItem.grant.state === 'active' ? (
                        <div className="flex flex-wrap gap-2">
                          <Button
                            type="button"
                            variant="destructive"
                            size="sm"
                            disabled={!mayRevoke || busy}
                            onClick={() =>
                              beginRevoke(selectedItem.grant, false)
                            }
                          >
                            {t('actions.revokeGeneration')}
                          </Button>
                          <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            disabled={!mayRevoke || !mayGrant || busy}
                            onClick={() =>
                              beginRevoke(selectedItem.grant, true)
                            }
                          >
                            {t('actions.revokeThenGrant')}
                          </Button>
                        </div>
                      ) : (
                        <p className="text-xs text-muted-foreground">
                          {t('grants.detail.notActive')}
                        </p>
                      )}
                    </section>
                  ) : null}

                  {act === null ? (
                    <div className="flex flex-wrap gap-2">
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        disabled={!mayGrant || busy}
                        onClick={() => beginGrant()}
                      >
                        <Plus className="size-4" aria-hidden="true" />
                        {t('actions.addGrant')}
                      </Button>
                    </div>
                  ) : null}

                  {actGrant && phase === 'form' ? (
                    <ChannelGrantForm
                      key={actGrant.successorOf ?? 'new'}
                      initial={actGrant.initial}
                      scope={scope}
                      canUserRead={canUserRead}
                      canAgentRead={canAgentRead}
                      me={me}
                      disabled={!mayGrant}
                      onReview={reviewGrant}
                      onCancel={cancelAct}
                    />
                  ) : null}
                  {actGrant &&
                  (phase === 'confirm' || phase === 'submitting') &&
                  grantIntent ? (
                    <div
                      className="flex flex-col gap-2 rounded-md border border-border bg-muted p-3"
                      data-slot="grant-confirm"
                      role="region"
                      aria-label={t('grants.confirm.title')}
                    >
                      <p className="text-sm font-medium">
                        {t('grants.confirm.title')}
                      </p>
                      <p className="text-sm text-muted-foreground">
                        {t('grants.confirm.body')}
                      </p>
                      {actGrant.successorOf ? (
                        <p className="text-sm text-muted-foreground">
                          {t('grants.confirm.successorOf', {
                            id: actGrant.successorOf,
                          })}
                        </p>
                      ) : null}
                      <KvList>
                        <KvRow label={t('grants.columns.subject')} mono>
                          {grantIntent.body.subject.kind}:
                          {grantIntent.body.subject.ref}
                        </KvRow>
                        <KvRow label={t('grants.columns.bits')}>
                          <span className="inline-flex flex-wrap gap-1">
                            {grantIntent.body.can_read ? (
                              <Badge variant="info">{t('access.read')}</Badge>
                            ) : null}
                            {grantIntent.body.can_write ? (
                              <Badge variant="info">{t('access.write')}</Badge>
                            ) : null}
                            {grantIntent.body.can_admin ? (
                              <Badge variant="info">{t('access.admin')}</Badge>
                            ) : null}
                          </span>
                        </KvRow>
                        <KvRow label={t('create.grant.expiresAt')} mono>
                          {grantIntent.body.expires_at ?? '—'}
                        </KvRow>
                        <KvRow label={t('config.confirm.ifMatch')} mono>
                          {grantIntent.etag}
                        </KvRow>
                      </KvList>
                    </div>
                  ) : null}
                  {actRevoke &&
                  (phase === 'confirm' || phase === 'submitting') &&
                  revokeIntent ? (
                    <div
                      className="flex flex-col gap-2 rounded-md border border-danger-line bg-danger-soft p-3"
                      data-slot="revoke-confirm"
                      role="region"
                      aria-label={t('grants.revoke.title')}
                    >
                      <p className="text-sm font-medium">
                        {t('grants.revoke.title')}
                      </p>
                      <p className="text-sm text-muted-foreground">
                        {actRevoke.thenGrant
                          ? t('grants.revoke.bodyThenGrant')
                          : t('grants.revoke.body')}
                      </p>
                      {ownAdmin(actRevoke.grant) ? (
                        <p
                          className="text-sm font-medium text-danger"
                          role="alert"
                          data-slot="revoke-own-admin"
                        >
                          {t('grants.revoke.ownAdminWarning')}
                        </p>
                      ) : null}
                      <KvList>
                        <KvRow label={t('grants.detail.id')} mono align="start">
                          <span className="break-all">
                            {revokeIntent.grantId}
                          </span>
                        </KvRow>
                        <KvRow label={t('grants.columns.subject')} mono>
                          {actRevoke.grant.subject.kind}:
                          {actRevoke.grant.subject.ref}
                        </KvRow>
                        <KvRow label={t('grants.columns.generation')} mono>
                          {actRevoke.grant.generation}
                        </KvRow>
                        <KvRow label={t('config.confirm.ifMatch')} mono>
                          {revokeIntent.etag}
                        </KvRow>
                      </KvList>
                    </div>
                  ) : null}
                  {(phase === 'confirm' || phase === 'submitting') &&
                  (grantIntent || revokeIntent) ? (
                    <div className="flex flex-wrap justify-end gap-2">
                      <Button
                        type="button"
                        variant="secondary"
                        onClick={cancelAct}
                        disabled={busy}
                      >
                        {t('actions.cancel')}
                      </Button>
                      <Button
                        type="button"
                        variant={
                          act?.kind === 'revoke'
                            ? 'destructive-solid'
                            : 'primary'
                        }
                        onClick={confirmAct}
                        disabled={
                          (act?.kind === 'revoke' ? !mayRevoke : !mayGrant) ||
                          busy ||
                          query.isFetching
                        }
                      >
                        {busy ? <Spinner className="size-3.5" /> : null}
                        {act?.kind === 'revoke'
                          ? t('actions.confirmRevoke')
                          : t('actions.confirmGrant')}
                      </Button>
                    </div>
                  ) : null}
                  {phase === 'conflict' && failure ? (
                    <div
                      data-slot="act-conflict"
                      className="flex flex-col gap-2"
                    >
                      <FailureNotice
                        failure={failure}
                        title={t('grants.conflictTitle')}
                      />
                      <p className="text-sm text-muted-foreground">
                        {t('grants.conflictBody')}
                      </p>
                      <div className="flex justify-end">
                        <Button
                          type="button"
                          variant="primary"
                          onClick={rereadAndDiscard}
                        >
                          {t('actions.rereadHistory')}
                        </Button>
                      </div>
                    </div>
                  ) : null}
                  {phase === 'unknown' && failure ? (
                    <div
                      data-slot="act-unknown"
                      className="flex flex-col gap-2"
                    >
                      <FailureNotice
                        failure={failure}
                        title={t('grants.unknownTitle')}
                      />
                      <p className="text-sm text-muted-foreground">
                        {t('grants.unknownBody')}
                      </p>
                      <KvList>
                        {grantIntent ? (
                          <>
                            <KvRow label={t('grants.unknownAct')}>
                              {t('grants.unknownGrant')}
                            </KvRow>
                            <KvRow label={t('grants.columns.subject')} mono>
                              {grantIntent.body.subject.kind}:
                              {grantIntent.body.subject.ref}
                            </KvRow>
                            <KvRow label={t('config.confirm.ifMatch')} mono>
                              {grantIntent.etag}
                            </KvRow>
                          </>
                        ) : null}
                        {revokeIntent ? (
                          <>
                            <KvRow label={t('grants.unknownAct')}>
                              {t('grants.unknownRevoke')}
                            </KvRow>
                            <KvRow
                              label={t('grants.detail.id')}
                              mono
                              align="start"
                            >
                              <span className="break-all">
                                {revokeIntent.grantId}
                              </span>
                            </KvRow>
                            <KvRow label={t('config.confirm.ifMatch')} mono>
                              {revokeIntent.etag}
                            </KvRow>
                          </>
                        ) : null}
                      </KvList>
                      <div className="flex flex-wrap justify-end gap-2">
                        <Button
                          type="button"
                          variant="ghost"
                          onClick={cancelAct}
                        >
                          {t('actions.discardIntent')}
                        </Button>
                        <Button
                          type="button"
                          variant="primary"
                          onClick={rereadAndDiscard}
                        >
                          {t('actions.rereadToVerify')}
                        </Button>
                      </div>
                    </div>
                  ) : null}
                  {phase === 'refused' && failure ? (
                    <div
                      data-slot="act-refused"
                      className="flex flex-col gap-2"
                    >
                      <FailureNotice
                        failure={failure}
                        title={
                          act?.kind === 'revoke'
                            ? t('grants.revoke.refusedTitle')
                            : t('grants.refusedTitle')
                        }
                      />
                      <div className="flex justify-end">
                        <Button
                          type="button"
                          variant="ghost"
                          onClick={cancelAct}
                        >
                          {t('actions.dismiss')}
                        </Button>
                      </div>
                    </div>
                  ) : null}
                  {phase === 'applied' && outcome ? (
                    <section
                      aria-label={t('receipt.title')}
                      className="flex flex-col gap-2"
                      data-slot="act-receipt"
                    >
                      <div
                        role="status"
                        className="rounded-md border border-success-line bg-success-soft px-3 py-2 text-sm text-success"
                      >
                        <p className="font-medium">
                          {act?.kind === 'revoke'
                            ? t('grants.revoke.appliedTitle')
                            : t('grants.appliedTitle')}
                        </p>
                        <p>
                          {act?.kind === 'revoke'
                            ? t('grants.revoke.appliedBody')
                            : t('grants.appliedBody')}
                        </p>
                      </div>
                      <KvList>
                        {receiptGrant ? (
                          <>
                            <KvRow
                              label={t('grants.detail.id')}
                              mono
                              align="start"
                            >
                              <span className="break-all">
                                {receiptGrant.id}
                              </span>
                            </KvRow>
                            <KvRow label={t('grants.columns.generation')} mono>
                              {receiptGrant.generation}
                            </KvRow>
                            <KvRow label={t('grants.detail.supersedes')} mono>
                              {receiptGrant.supersedes_id ?? '—'}
                            </KvRow>
                            <KvRow label={t('grants.columns.stored')} mono>
                              {receiptGrant.state}
                            </KvRow>
                          </>
                        ) : null}
                        <KvRow label={t('receipt.etag')} mono>
                          {outcome.etag ?? outcome.result.etag}
                        </KvRow>
                        <KvRow label={t('receipt.version')} mono>
                          {outcome.result.channel.version}
                        </KvRow>
                        <KvRow label={t('receipt.auditSeq')} mono>
                          {outcome.result.audit_seq}
                        </KvRow>
                      </KvList>
                      <div className="flex flex-wrap justify-end gap-2">
                        {successor ? (
                          <Button
                            type="button"
                            variant="primary"
                            disabled={query.isFetching || !etag || !mayGrant}
                            onClick={() =>
                              beginGrant(
                                successor,
                                act?.kind === 'revoke'
                                  ? act.grant.id
                                  : undefined,
                              )
                            }
                          >
                            {t('actions.grantSuccessor')}
                          </Button>
                        ) : null}
                        <Button
                          type="button"
                          variant="ghost"
                          onClick={cancelAct}
                        >
                          {t('actions.dismiss')}
                        </Button>
                      </div>
                    </section>
                  ) : null}
                </div>
              </TabsContent>
            </Tabs>
          ) : null}
        </div>

        <SheetFooter className="shrink-0 border-t border-border bg-surface px-6 py-3">
          <Button
            type="button"
            variant="outline"
            onClick={reread}
            disabled={!sheetAllowed || !channelId || busy}
          >
            <RefreshCcw className="size-4" aria-hidden="true" />
            {t('actions.reread')}
          </Button>
          <Button
            type="button"
            variant="secondary"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {t('actions.close')}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
