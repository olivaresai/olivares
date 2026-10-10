// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { formatDateTime, formatShortDateTime } from '@/lib/format'
import { useQuery } from '@tanstack/react-query'
import {
  Check,
  ListChecks,
  Plus,
  RefreshCw,
  ShieldCheck,
  X,
} from 'lucide-react'
import { useRouter } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { RecordingNotice } from '@/features/recordings/recording-notice'
import { RelTimeLabel } from '@/features/shared'
import { governanceApi, governanceKeys } from './api'
import { AgentRiskView } from './agent-risk-view'
import { ApprovalDetailSheet } from './approval-detail'
import { PANEL_EXTENSIONS } from '@/features/extensions'
import { useOfferedPanels, type TabExtension } from '@/features/panels'
import { DecisionDialog } from './decision-dialog'
import { ApprovalRequestCell, AskedBy } from './approval-preview'
import { IdentitiesView } from './identities-view'
import { NewRequestDialog } from './new-request-dialog'
import { PoliciesView } from './policies-view'
import './i18n'
import { APPROVAL_STATUSES, canDecideOnRequest } from './types'
import type { ApprovalDTO, DecisionVerb, SweepReport } from './types'
import {
  PagePrimaryAction,
  PageSecondaryActions,
} from '@/components/ui/page-actions'

type TabKey = string

const TAB_KEYS: readonly TabKey[] = [
  'approvals',
  'policies',
  'identities',
  'agent-risk',
]

/** The section a `?tab=` link names (the sidebar's Approvals entry and Now link to
 *  `?tab=approvals`), or the queue when the link names nothing this principal may open. */
function initialTab(
  canReadApprovals: boolean,
  panels: readonly TabExtension[],
): TabKey {
  const fallback: TabKey = canReadApprovals ? 'approvals' : 'policies'
  if (typeof window === 'undefined') return fallback
  const want = new URLSearchParams(window.location.search).get('tab')
  if (!want || (!TAB_KEYS.includes(want) && !panels.some((p) => p.id === want)))
    return fallback
  if (want === 'approvals' && !canReadApprovals) return fallback
  return want as TabKey
}

// The HITL approval queue polls on a 12s interval — there is NO SSE; all freshness is
// poll-based, and status is computed at read (so an expired item surfaces before a
// sweep persists it). See spec liveData.
const APPROVALS_POLL_MS = 12_000

/**
 * GovernanceView (registry id 'permissions') is the control-plane governance surface:
 * the human-in-the-loop APPROVAL QUEUE (default, richest), RBAC/ABAC policy, and the
 * identities / agent↔NHI bindings behind every action. The web is a thin client — it
 * renders the engine's DTOs and dispatches privileged, audited, confirmed operations;
 * it never reimplements authorization (the backend is the authority, every action is
 * mirrored by can() purely to hide/disable, and self-audited server-side).
 */
export default function GovernanceView() {
  const { t } = useTranslation(['governance', 'common'])
  const { can } = useAuth()
  const canReadApprovals = can('governance:approval:read')
  const panels = useOfferedPanels(PANEL_EXTENSIONS.governanceTabs ?? [])
  //the AgentCore Cedar export is NOT a tab here. It has its own route
  // (registry.tsx, id `agentcoreExport`) because governance:agentcore-export:admin
  // is independently grantable (governance.go:397), and behind this identity-gated
  // view an operator holding only that delegated permission could not reach it —
  // the same reasoning that gave routine policies their own route.

  // Outside a RouterProvider (component tests) the hook answers undefined and the section
  // is simply not written to the URL — the installed hook's own no-provider contract.
  const router = useRouter({ warn: false }) as
    { navigate: (opts: unknown) => Promise<void> } | undefined
  const [tab, setTabState] = useState<TabKey>(() =>
    initialTab(canReadApprovals, panels),
  )
  function setTab(value: TabKey) {
    setTabState(value)
    // `replace` and out of scroll restoration, like the other tabbed screens: a section is
    // a facet of this page, not a place Back returns to.
    void router?.navigate({
      search: (prev: Record<string, unknown>) => ({ ...prev, tab: value }),
      replace: true,
      resetScroll: false,
    })
  }

  return (
    <div className="flex flex-col gap-5 pb-10">
      <PageHeader
        title={t('title')}
        description={t('subtitle')}
        icon={ShieldCheck}
      />

      <RecordingNotice namespace="governance" />

      <Tabs value={tab} onValueChange={(v) => setTab(v as TabKey)}>
        <TabsList>
          {canReadApprovals && (
            <TabsTrigger value="approvals">{t('tabs.approvals')}</TabsTrigger>
          )}
          <TabsTrigger value="policies">{t('tabs.policies')}</TabsTrigger>
          <TabsTrigger value="identities">{t('tabs.identities')}</TabsTrigger>
          <TabsTrigger value="agent-risk">{t('tabs.agentRisk')}</TabsTrigger>
          {panels.map((p) => (
            <TabsTrigger key={p.id} value={p.id}>
              {p.label()}
            </TabsTrigger>
          ))}
        </TabsList>

        {canReadApprovals && (
          <TabsContent value="approvals">
            <ApprovalsTab active={tab === 'approvals'} />
          </TabsContent>
        )}

        <TabsContent value="policies">
          <PoliciesView />
        </TabsContent>

        <TabsContent value="identities">
          <IdentitiesView />
        </TabsContent>

        <TabsContent value="agent-risk">
          <AgentRiskView />
        </TabsContent>

        {panels.map((p) => (
          <TabsContent key={p.id} value={p.id}>
            <p.Component />
          </TabsContent>
        ))}
      </Tabs>
    </div>
  )
}

/** The approval named by ?approval= in the address, if any. */
function linkedApproval(): string | null {
  const v = new URLSearchParams(window.location.search).get('approval')
  return v || null
}

function ApprovalsTab({ active }: { active: boolean }) {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant, can, principal } = useAuth()
  const canRead = can('governance:approval:read')
  const canDecide = can('governance:approval:admin')
  const canWrite = can('governance:approval:write')

  const [statusFilter, setStatusFilter] = useState<string>('pending')
  // ?approval=<id> opens that request: the notification bell links a waiting session's
  // approval here, one click from the bell to the decision (Warp concept).
  const [selected, setSelected] = useState<string | null>(linkedApproval)
  const [detailOpen, setDetailOpen] = useState(() => linkedApproval() !== null)
  const [decisionVerb, setDecisionVerb] = useState<DecisionVerb | null>(null)
  const [decisionApproval, setDecisionApproval] = useState<ApprovalDTO | null>(
    null,
  )
  const [decisionOpen, setDecisionOpen] = useState(false)
  const [confirmCancel, setConfirmCancel] = useState<ApprovalDTO | null>(null)
  const [confirmSweep, setConfirmSweep] = useState(false)
  const [newOpen, setNewOpen] = useState(false)

  const params = statusFilter === '__all__' ? {} : { status: statusFilter }
  const approvals = useQuery({
    queryKey: governanceKeys.approvals(activeTenant, params),
    queryFn: () => governanceApi.listApprovals(params),
    enabled: canRead && active,
    // Poll the live queue (no SSE). Only when this tab is active.
    refetchInterval: active ? APPROVALS_POLL_MS : false,
  })

  const cancel = usePrivilegedMutation({
    mutationFn: () => governanceApi.cancelApproval(confirmCancel!.id),
    invalidateKeys: () => [governanceKeys.approvals(activeTenant)],
    successMessage: t('cancel.done'),
    onDone: () => setConfirmCancel(null),
  })

  const sweep = usePrivilegedMutation<void, SweepReport>({
    mutationFn: () => governanceApi.sweepApprovals(),
    invalidateKeys: () => [governanceKeys.approvals(activeTenant)],
    // Surface the sweepReport counts in the success toast (it is a single report,
    // not a list); flag if more pending requests remain unscanned (re-run).
    successMessage: (r) =>
      `${t('sweep.done')} — ${t('sweep.result', {
        scanned: r.scanned,
        escalated: r.escalated,
        expired: r.expired,
      })}${r.more ? ` ${t('sweep.more')}` : ''}`,
    onDone: () => setConfirmSweep(false),
  })

  if (!canRead) return <ForbiddenState />

  function openDecision(approval: ApprovalDTO, verb: DecisionVerb) {
    setDecisionApproval(approval)
    setDecisionVerb(verb)
    setDecisionOpen(true)
  }

  const columns: TableColumn<ApprovalDTO, unknown>[] = [
    {
      // What will run comes first (SC sweep, 09b): session, tool, the reviewed text and
      // the folder. The action and the references are in the review's Details.
      id: 'request',
      header: t('approvals.request'),
      cell: ({ row }) => <ApprovalRequestCell approval={row.original} />,
    },
    {
      accessorKey: 'requested_by',
      header: t('approvals.requestedBy'),
      cell: ({ row }) => <AskedBy approval={row.original} />,
    },
    {
      id: 'progress',
      header: t('approvals.progress'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">
          {t('approvals.progressOf', {
            approved: row.original.approve_count,
            required: row.original.required_approvals,
          })}
          {row.original.reject_count > 0 && (
            <span className="ml-1.5 text-danger">
              ·{' '}
              {t('approvals.rejectedCount', {
                count: row.original.reject_count,
              })}
            </span>
          )}
        </span>
      ),
    },
    {
      accessorKey: 'status',
      header: t('approvals.status'),
      cell: ({ row }) => (
        <span className="flex items-center gap-1.5">
          <StatusBadge status={row.original.status} />
          {row.original.escalated && (
            <Badge variant="warning" title={t('approvals.escalatedHint')}>
              {t('approvals.escalatedBadge')}
            </Badge>
          )}
        </span>
      ),
    },
    {
      id: 'age',
      header: t('approvals.expires'),
      cell: ({ row }) =>
        // An expired request says so with the time the engine stored (HU 039: a
        // no-expiry policy has no expires_at, but the bounded wait still ends it).
        // The shortest absolute time that fits the column (09b capture: the full date and
        // time was cut at 1280); the whole of it is the title.
        row.original.status === 'expired' && row.original.decided_at ? (
          <span title={formatDateTime(row.original.decided_at)}>
            {t('approvals.expiredAt', {
              time: formatShortDateTime(row.original.decided_at),
            })}
          </span>
        ) : row.original.expires_at ? (
          <RelTimeLabel ts={row.original.expires_at} />
        ) : (
          '—'
        ),
    },
    {
      id: 'actions',
      header: '',
      cell: ({ row }) => {
        const isPending = row.original.status === 'pending'
        if (!isPending) return null
        // Hide Approve/Reject the engine would 403 (separation of duties): the
        // requester cannot decide their own, and a token has no stable user id.
        const mayDecide =
          canDecide &&
          canDecideOnRequest(
            row.original.requested_by,
            principal?.actor,
            principal?.kind,
          )
        return (
          <div className="flex items-center justify-end gap-1">
            {canWrite && (
              <Button
                variant="ghost"
                size="sm"
                onClick={(e) => {
                  e.stopPropagation()
                  setConfirmCancel(row.original)
                }}
              >
                {t('approvals.cancel')}
              </Button>
            )}
            {mayDecide && (
              <Button
                variant="ghost"
                size="icon"
                aria-label={t('approvals.reject')}
                onClick={(e) => {
                  e.stopPropagation()
                  openDecision(row.original, 'reject')
                }}
              >
                <X />
              </Button>
            )}
            {mayDecide && (
              <Button
                variant="ghost"
                size="icon"
                aria-label={t('approvals.approve')}
                onClick={(e) => {
                  e.stopPropagation()
                  openDecision(row.original, 'approve')
                }}
              >
                <Check />
              </Button>
            )}
          </div>
        )
      },
    },
  ]

  const emptyNode =
    statusFilter === 'pending' ? (
      // ⛔ AN EMPTY QUEUE IS NOT A TASK. Nothing is owed here and nothing is
      // wrong: the reader is told so in one line, with no icon panel and no
      // manufactured button. `quiet` makes the second one impossible.
      <EmptyState
        quiet
        title={t('approvals.emptyPending')}
        description={t('approvals.emptyPendingHint')}
      />
    ) : (
      <EmptyState
        icon={<ListChecks />}
        title={t('approvals.empty')}
        description={t('approvals.emptyHint')}
      />
    )

  return (
    <div className="flex flex-col gap-3">
      {canWrite && (
        <PagePrimaryAction>
          <Button variant="primary" size="sm" onClick={() => setNewOpen(true)}>
            <Plus />
            {t('approvals.newRequest')}
          </Button>
        </PagePrimaryAction>
      )}
      {canDecide && (
        <PageSecondaryActions>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setConfirmSweep(true)}
          >
            <RefreshCw />
            {t('approvals.runSweep')}
          </Button>
        </PageSecondaryActions>
      )}
      <p className="text-caption text-muted-foreground">
        {t('approvals.caption')}
      </p>

      <DataTable
        columns={columns}
        data={approvals.data?.items ?? []}
        isLoading={approvals.isLoading}
        error={approvals.error}
        onRetry={() => approvals.refetch()}
        searchable
        searchPlaceholder={t('approvals.search')}
        getRowId={(r) => r.id}
        onRowClick={(r) => {
          setSelected(r.id)
          setDetailOpen(true)
        }}
        empty={emptyNode}
        toolbar={
          <>
            <Select value={statusFilter} onValueChange={setStatusFilter}>
              <SelectTrigger
                className="w-40"
                aria-label={t('approvals.filterStatus')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="__all__">
                  {t('approvals.statusAll')}
                </SelectItem>
                {APPROVAL_STATUSES.map((s) => (
                  <SelectItem key={s} value={s}>
                    {t(`status.${s}`, { defaultValue: s })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </>
        }
      />

      <ApprovalDetailSheet
        approvalId={selected}
        open={detailOpen}
        onOpenChange={setDetailOpen}
      />

      {decisionApproval && (
        <DecisionDialog
          open={decisionOpen}
          onOpenChange={setDecisionOpen}
          approvalId={decisionApproval.id}
          approval={decisionApproval}
          verb={decisionVerb}
        />
      )}

      {confirmCancel && (
        <ConfirmDialog
          open={!!confirmCancel}
          onOpenChange={(o) => !o && setConfirmCancel(null)}
          title={t('cancel.title')}
          description={t('cancel.body')}
          tone="danger"
          confirmLabel={t('cancel.confirm')}
          pending={cancel.isPending}
          onConfirm={() => cancel.mutate(undefined)}
        />
      )}

      {confirmSweep && (
        <ConfirmDialog
          open={confirmSweep}
          onOpenChange={setConfirmSweep}
          title={t('sweep.title')}
          description={t('sweep.body')}
          confirmLabel={t('sweep.confirm')}
          pending={sweep.isPending}
          onConfirm={() => sweep.mutate()}
        />
      )}

      <NewRequestDialog open={newOpen} onOpenChange={setNewOpen} />
    </div>
  )
}
