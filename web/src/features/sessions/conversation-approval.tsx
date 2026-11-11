// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE REQUEST A SESSION WAITS ON, IN ITS CONVERSATION (the regression design). The person reads
// what will run, why, the rule that asked, what it reaches and who asks, and answers it
// where they read it: Approve once, or Deny. The reply field below stays usable, so a
// typed message can tell the agent to do something else instead.
//
// Every fact is the approval queue's own (features/governance/approval-preview.tsx) and
// every decision goes through the queue's own mutation: this is another place to decide,
// not another way to.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Loader2, ShieldCheck } from 'lucide-react'
import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { PathText } from '@/components/ui/path-text'
import { KvList, KvRow } from '@/components/ui/kv'
import { agentOpsKeys } from '@/features/agentops/api'
import { toolName } from '@/features/agentops/engine-tools'
import type { RunDTO } from '@/features/agentops/types'
import { governanceApi, governanceKeys } from '@/features/governance/api'
import {
  approvalReaches,
  approvalWhy,
  ApprovalWhatRuns,
  useAskedBy,
  useRuleName,
} from '@/features/governance/approval-preview'
import {
  DenyDialog,
  useDecision,
  useMayDecide,
} from '@/features/governance/decision-dialog'
import { APPROVAL_READ } from '@/features/governance/use-pending-approvals'
import type { ApprovalDTO } from '@/features/governance/types'
import { useAuth } from '@/lib/auth/context'
import {
  isTypingTarget,
  modalIsOpen,
  resolveBinding,
} from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { runAwaitedApproval } from './provenance'
import '@/features/governance/i18n'
import './i18n'

/** An open menu has the keys too (its letters are typeahead). */
const OPEN_MENU = '[role="menu"][data-state="open"]'
/** Of two conversations side by side, the column that does not take the keys. */
const OTHER_COLUMN = '[data-focused="false"]'

/** The request this run waits on, when the person may read it; nothing otherwise. */
export function ConversationApproval({ run }: { run: RunDTO }) {
  const { can } = useAuth()
  const id = runAwaitedApproval(run)
  if (!id || !can(APPROVAL_READ)) return null
  return <ApprovalCard key={id} id={id} run={run} />
}

function ApprovalCard({ id, run }: { id: string; run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const query = useQuery({
    queryKey: governanceKeys.approval(activeTenant, id),
    queryFn: () => governanceApi.getApproval(id),
    // As the queue's open request: a decision made elsewhere shows without a reload.
    refetchInterval: 12_000,
  })
  const titleId = useId()
  const approval = query.data
  return (
    <section
      data-trust-chrome=""
      aria-labelledby={titleId}
      data-testid="conversation-approval"
      className="my-3 flex min-w-0 flex-col gap-3 rounded-card border border-warn/60 bg-warn-soft p-4 text-foreground"
    >
      <h3
        id={titleId}
        className="flex items-center gap-2 text-body font-semibold text-text"
      >
        <ShieldCheck aria-hidden className="size-4 shrink-0" />
        {t('approval.title')}
      </h3>
      {approval ? (
        <ApprovalBody approval={approval} run={run} />
      ) : query.isError ? (
        <p role="status" className="text-body text-danger">
          {t('approval.unreadable')}
        </p>
      ) : null}
    </section>
  )
}

/** A decision moves the session too: its run record and runs list are read again now,
 * so the composer and the session list leave the waiting state without a reload. */
function useRefreshRuns() {
  const queryClient = useQueryClient()
  const { activeTenant } = useAuth()
  return useCallback(() => {
    void queryClient.invalidateQueries({
      queryKey: agentOpsKeys.all(activeTenant),
    })
  }, [queryClient, activeTenant])
}

function ApprovalBody({
  approval,
  run,
}: {
  approval: ApprovalDTO
  run: RunDTO
}) {
  const { t } = useTranslation('sessions')
  const { t: tg } = useTranslation('governance')
  const rule = useRuleName(approval)
  const reach = approvalReaches(approval, run.reaches, tg)
  const why = approvalWhy(approval)
  const tool = run.provider_driver ? toolName(run.provider_driver) : null
  const asker = useAskedBy(approval)
  const pending = approval.status === 'pending'
  const { mayDecide, decidedByMe } = useMayDecide(approval)
  const refreshRuns = useRefreshRuns()
  const approve = useDecision(approval.id, 'approve', refreshRuns)
  const [denyOpen, setDenyOpen] = useState(false)
  const card = useRef<HTMLDivElement>(null)
  // One decision per action: a second key or click before the first one's render is dropped.
  const inFlight = useRef(false)
  const deciding = approve.isPending
  const approvingSession = deciding && approve.variables?.scope === 'session'
  useEffect(() => {
    if (!deciding) inFlight.current = false
  }, [deciding])
  const { mutate } = approve
  const approveRequest = useCallback(
    (scope?: 'session') => {
      if (inFlight.current) return
      inFlight.current = true
      mutate({ decision: 'approve', ...(scope ? { scope } : {}) })
    },
    [mutate],
  )

  // ALT+A, ONLY WHILE THE WHOLE REQUEST IS IN VIEW: a person approves what they can read,
  // never a card half scrolled away, never past an open dialog or menu, and never in the
  // column beside the one they work in.
  const [inView, setInView] = useState(false)
  useEffect(() => {
    const el = card.current
    if (!el || typeof IntersectionObserver !== 'function') return
    const observer = new IntersectionObserver(
      ([entry]) => setInView(!!entry && entry.intersectionRatio >= 1),
      { threshold: [0, 1] },
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [])
  useEffect(() => {
    if (!mayDecide || !inView) return
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.isComposing || e.repeat) return
      if (
        resolveBinding(KEYBINDINGS, e, { workSurface: true }) !==
        'surface.approve'
      )
        return
      if (modalIsOpen() || document.querySelector(OPEN_MENU)) return
      if (card.current?.closest(OTHER_COLUMN)) return
      // In a field, Option+A types a letter on a Mac: that letter is the person's.
      if (isTypingTarget(e.target) && e.key.toLowerCase() !== 'a') return
      e.preventDefault()
      approveRequest()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [mayDecide, inView, approveRequest])

  return (
    <div ref={card} className="flex min-w-0 flex-col gap-3">
      <ApprovalWhatRuns approval={approval} />
      {/* One column of labels, as the request reads: Why, Rule, Reach, Requested by. */}
      <KvList className="[&_dt]:w-28 [&_dt]:shrink-0">
        {why ? (
          <KvRow label={t('approval.why')} align="start">
            <span className="break-words">{why}</span>
          </KvRow>
        ) : null}
        {rule ? (
          <KvRow label={t('approval.rule')} align="start">
            <span className="break-words">{rule}</span>
          </KvRow>
        ) : null}
        <KvRow label={t('approval.reach')} align="start">
          {reach.length > 0 ? (
            <ul className="flex min-w-0 flex-col">
              {reach.map((r, index) => (
                <li key={index} className="min-w-0 break-words">
                  {'path' in r ? (
                    <Trans
                      i18nKey={`preview.reachFolder.${r.access}`}
                      ns="governance"
                      components={{
                        path: <PathText value={r.path} copy={false} wrap />,
                      }}
                    />
                  ) : (
                    r.text
                  )}
                </li>
              ))}
            </ul>
          ) : (
            t('approval.reachUnknown')
          )}
        </KvRow>
        <KvRow label={t('approval.requestedBy')} align="start">
          <span className="break-words">
            {[tool, asker].filter(Boolean).join(' · ')}
          </span>
        </KvRow>
      </KvList>
      {pending && approval.subject_kind !== 'claude.tool' ? (
        <p className="text-caption text-text-2">
          {t('approval.feedbackUnsupported')}
        </p>
      ) : null}
      {pending ? (
        mayDecide ? (
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="primary"
              disabled={deciding}
              aria-keyshortcuts="Alt+A"
              onClick={() => approveRequest()}
            >
              {deciding && !approvingSession ? (
                <Loader2 aria-hidden className="animate-spin" />
              ) : (
                <Check aria-hidden />
              )}
              {deciding && !approvingSession
                ? tg('approvals.approving')
                : tg('approvals.approveOnce')}
            </Button>
            {!approval.session_scope_allowed &&
            (approval.policy_ref || approval.subject_kind === 'claude.tool') ? (
              <p className="text-caption text-text-2">
                {tg('approvals.sessionPolicy')}
              </p>
            ) : null}
            {approval.session_scope_allowed && (
              <Button
                variant="secondary"
                disabled={deciding}
                onClick={() => approveRequest('session')}
              >
                {approvingSession ? (
                  <Loader2 aria-hidden className="animate-spin" />
                ) : (
                  <Check aria-hidden />
                )}
                {approvingSession
                  ? tg('approvals.approving')
                  : tg('approvals.approveForSession')}
              </Button>
            )}
            <Button
              variant="ghost"
              className="text-danger"
              onClick={() => setDenyOpen(true)}
            >
              {tg('approvals.deny')}
            </Button>
            <DenyDialog
              open={denyOpen}
              onOpenChange={setDenyOpen}
              approval={approval}
              onDecided={refreshRuns}
            />
          </div>
        ) : (
          <p className="text-caption text-text-2">
            {decidedByMe ? tg('detail.decidedByYou') : t('approval.notYours')}
          </p>
        )
      ) : (
        // THE DECISION AS IT WAS MADE: a denial reads as a denial, never as a success.
        <p
          role="status"
          data-status={approval.status}
          className={
            approval.status === 'approved'
              ? 'text-body font-medium text-success'
              : 'text-body font-medium text-danger'
          }
        >
          {approval.status === 'approved'
            ? t('approval.approved')
            : approval.status === 'rejected'
              ? t('approval.denied')
              : t('approval.ended')}
        </p>
      )}
    </div>
  )
}

/** The original decisions are the allowance list; revocation keeps their audit history. */
export function SessionAllowances({
  sessionRef,
  active,
}: {
  sessionRef?: string
  active: boolean
}) {
  const { activeTenant, can } = useAuth()
  const { t } = useTranslation('governance')
  const client = useQueryClient()
  const query = useQuery({
    queryKey: [
      ...governanceKeys.approvals(activeTenant),
      'session-allowances',
      sessionRef,
    ],
    enabled: active && !!sessionRef && can(APPROVAL_READ),
    refetchInterval: 12_000,
    queryFn: async () => {
      const allowances: ApprovalDTO[] = []
      let cursor: string | undefined
      do {
        const page = await governanceApi.listApprovals({
          status: 'approved',
          cursor,
        })
        for (const approval of page.items) {
          if (
            approval.session_ref !== sessionRef ||
            !approval.session_scope_allowed
          )
            continue
          const decisions = await governanceApi.listDecisions(approval.id)
          if (
            decisions.items.length &&
            decisions.items.every(
              (d) => d.decision === 'approve' && d.scope === 'session',
            )
          )
            allowances.push(approval)
        }
        cursor = page.has_more ? page.cursor : undefined
      } while (cursor)
      return allowances
    },
  })
  const revoke = useMutation({
    mutationFn: (id: string) => governanceApi.cancelApproval(id),
    onSuccess: () =>
      client.invalidateQueries({ queryKey: governanceKeys.all(activeTenant) }),
  })
  if (!active || !sessionRef || !can(APPROVAL_READ)) return null
  if (query.isError) return <p role="alert">{t('approvals.allowancesError')}</p>
  if (!query.data?.length) return null
  return (
    <section
      aria-label={t('approvals.allowances')}
      className="my-3 flex flex-col gap-2"
    >
      <h3 className="text-caption font-medium">{t('approvals.allowances')}</h3>
      {query.data.map((approval) => (
        <div key={approval.id} className="flex min-w-0 items-start gap-2">
          <div className="min-w-0 flex-1">
            <ApprovalWhatRuns approval={approval} />
          </div>
          {can('governance:approval:admin') && (
            <Button
              variant="ghost"
              size="sm"
              disabled={revoke.isPending}
              onClick={() => revoke.mutate(approval.id)}
            >
              {t('approvals.revokeAllowance')}
            </Button>
          )}
        </div>
      ))}
      {revoke.isError && <p role="alert">{t('approvals.allowancesError')}</p>}
    </section>
  )
}
