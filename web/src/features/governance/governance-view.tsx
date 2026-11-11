// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { PanelSlot } from '@/components/ui/trust-chrome'
import { useQuery } from '@tanstack/react-query'
import { ListChecks, Plus, RefreshCw, ShieldCheck } from 'lucide-react'
import { useRef, useState } from 'react'
import { useTabParam } from '@/lib/hooks/use-tab-param'
import { useUrlState } from '@/lib/hooks/use-url-state'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '@/lib/auth/context'
import { useMinWidth } from '@/lib/hooks/use-min-width'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { RecordingNoticeSlot } from '@/features/recording-notice-slot'
import { governanceApi, governanceKeys } from './api'
import { AgentRiskView } from './agent-risk-view'
import { APPROVAL_QUEUE_REGION, ApprovalDetailPanel } from './approval-detail'
import { ApprovalQueue, flaggedApprovalId } from './approval-queue'
import { PANEL_EXTENSIONS } from '@/features/extensions'
import { useOfferedPanels } from '@/features/panels'
import { IdentitiesView } from './identities-view'
import { NewRequestDialog } from './new-request-dialog'
import { PoliciesView } from './policies-view'
import { VaultsView } from './vaults-view'
import './i18n'
import { APPROVAL_STATUSES, canDecideOnRequest } from './types'
import type { ApprovalDTO, SweepReport } from './types'
import {
  PagePrimaryAction,
  PageSecondaryActions,
} from '@/components/ui/page-actions'

type TabKey = string

const TAB_KEYS: readonly TabKey[] = [
  'approvals',
  'policies',
  'identities',
  'vaults',
  'agent-risk',
]

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

  // `?tab=` names the section (the sidebar's Approvals entry and Now link to
  // `?tab=approvals`). Approvals is first only where this principal may read it, so
  // anything else falls back to the first section on offer.
  const [tab, setTab] = useTabParam([
    ...(canReadApprovals ? ['approvals'] : []),
    ...TAB_KEYS.filter((k) => k !== 'approvals'),
    ...panels.map((p) => p.id),
  ])

  return (
    <div className="flex flex-col gap-5 pb-10">
      {/* The page says where the person is: the queue is Rules' Approvals. */}
      <PageHeader
        title={tab === 'approvals' ? t('tabs.approvals') : t('title')}
        description={t('subtitle')}
        icon={ShieldCheck}
      />

      <RecordingNoticeSlot namespace="governance" />

      {/* The queue is a page of its own, not a tab: no tab list, no tab panel.
          The tab is 'approvals' only where the person may read the queue. */}
      {tab === 'approvals' ? (
        <ApprovalsTab />
      ) : (
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="policies">{t('tabs.policies')}</TabsTrigger>
            <TabsTrigger value="identities">{t('tabs.identities')}</TabsTrigger>
            <TabsTrigger value="vaults">{t('tabs.vaults')}</TabsTrigger>
            <TabsTrigger value="agent-risk">{t('tabs.agentRisk')}</TabsTrigger>
            {panels.map((p) => (
              <TabsTrigger key={p.id} value={p.id}>
                {p.label()}
              </TabsTrigger>
            ))}
          </TabsList>

          <TabsContent value="policies">
            <PoliciesView />
          </TabsContent>

          <TabsContent value="identities">
            <IdentitiesView />
          </TabsContent>

          <TabsContent value="vaults">
            <VaultsView />
          </TabsContent>

          <TabsContent value="agent-risk">
            <AgentRiskView />
          </TabsContent>

          {panels.map((p) => (
            <TabsContent key={p.id} value={p.id}>
              <PanelSlot>
                <p.Component />
              </PanelSlot>
            </TabsContent>
          ))}
        </Tabs>
      )}
    </div>
  )
}

/** The text a search matches in a request: what runs, why, and who asks. */
function searchText(a: ApprovalDTO): string {
  return [
    a.review?.tool,
    a.review?.text,
    a.reason,
    a.action,
    a.subject_ref,
    a.requested_by,
    a.launched_by,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
}

function ApprovalsTab() {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant, can, principal } = useAuth()
  const canRead = can('governance:approval:read')
  const canDecide = can('governance:approval:admin')
  const canWrite = can('governance:approval:write')
  // The panel sits beside the queue from `lg`; below it, under the queue, so picking a
  // row brings the panel into view (the row says a tap opens the decision).
  const wide = useMinWidth(1024)
  const panelRef = useRef<HTMLDivElement>(null)

  const [statusFilter, setStatusFilter] = useState<string>('pending')
  const [search, setSearch] = useState('')
  // ?approval=<id> opens that request: the notification bell and Estate's Needs you link
  // a waiting request here, one click from there to the decision.
  // `?approval=` is the request on screen, so a link, a reload and Back all show it. The
  // default (first row) is not written: a pristine queue keeps a clean address.
  const [{ approval: linked }, patchUrl] = useUrlState(['approval'])
  const [pinned, setPinned] = useState<string | null>(null)
  // The address leads: when it names another request (or none, after Back), the pin follows.
  const [seenLinked, setSeenLinked] = useState(linked)
  if (linked !== seenLinked) {
    setSeenLinked(linked)
    setPinned(linked ?? null)
  }
  const picked = linked ?? pinned
  const [confirmSweep, setConfirmSweep] = useState(false)
  const [newOpen, setNewOpen] = useState(false)

  const params = statusFilter === '__all__' ? {} : { status: statusFilter }
  const approvals = useQuery({
    queryKey: governanceKeys.approvals(activeTenant, params),
    queryFn: () => governanceApi.listApprovals(params),
    enabled: canRead,
    // Poll the live queue (no SSE) while its page is open.
    refetchInterval: APPROVALS_POLL_MS,
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

  const query = search.trim().toLowerCase()
  const items = (approvals.data?.items ?? []).filter(
    (a) => !query || searchText(a).includes(query),
  )
  // The engine answers who may decide each request (`may_decide`), and the flag reads it
  // by the rule the open request's buttons use (useMayDecide): it never points at a
  // request the engine refuses this person, nor passes one it offers them.
  const mayDecide = (a: ApprovalDTO) =>
    a.may_decide ??
    (canDecide &&
      canDecideOnRequest(a.requested_by, principal?.actor, principal?.kind))
  const flaggedId = flaggedApprovalId(items, mayDecide)
  // THE REQUEST ON SCREEN STAYS ON SCREEN. The first one shown is the flagged one (or the
  // first row), and from then on the panel shows what the person picked: a decision, a
  // poll or another approver never swaps a different request in under their hand, where
  // a click or `A` would decide what they have not read. A request the list no longer
  // holds (decided, expired, or linked from elsewhere) is read by its id.
  const defaultId = flaggedId ?? items[0]?.id
  if (picked === null && defaultId) setPinned(defaultId)
  const shownId = picked ?? defaultId

  function select(id: string) {
    setPinned(id)
    // A place, not a filter: Back returns to the request read before this one.
    patchUrl({ approval: id }, { history: 'push' })
    if (!wide) panelRef.current?.scrollIntoView?.({ block: 'start' })
  }

  const emptyNode = query ? (
    <EmptyState
      quiet
      title={t('approvals.noMatch')}
      description={t('approvals.noMatchHint')}
    />
  ) : statusFilter === 'pending' ? (
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

  const newRequest = (
    <Button
      variant={flaggedId ? 'secondary' : 'primary'}
      size="sm"
      onClick={() => setNewOpen(true)}
    >
      <Plus />
      {t('approvals.newRequest')}
    </Button>
  )

  return (
    <div className="flex flex-col gap-3">
      {/* ONE ORANGE: while a request waits for this person, deciding it is the verb of
          the screen (Approve once), and New request steps aside to a secondary control. */}
      {canWrite && !flaggedId && (
        <PagePrimaryAction>{newRequest}</PagePrimaryAction>
      )}
      {(canDecide || (canWrite && flaggedId)) && (
        <PageSecondaryActions>
          {canWrite && flaggedId ? newRequest : null}
          {canDecide && (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setConfirmSweep(true)}
            >
              <RefreshCw />
              {t('approvals.runSweep')}
            </Button>
          )}
        </PageSecondaryActions>
      )}
      <p className="text-caption text-muted-foreground">
        {t('approvals.caption')}
      </p>

      <div className="flex flex-wrap items-center gap-2">
        <Input
          type="search"
          className="w-64"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t('approvals.search')}
          aria-label={t('approvals.search')}
        />
        <Select value={statusFilter} onValueChange={setStatusFilter}>
          <SelectTrigger
            className="w-40"
            aria-label={t('approvals.filterStatus')}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__all__">{t('approvals.statusAll')}</SelectItem>
            {APPROVAL_STATUSES.map((s) => (
              <SelectItem key={s} value={s}>
                {t(`status.${s}`, { defaultValue: s })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {approvals.isLoading ? (
        <div className="flex flex-col gap-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-20 w-full" />
          ))}
        </div>
      ) : approvals.error ? (
        <QueryErrorState
          error={approvals.error}
          retry={() => approvals.refetch()}
        />
      ) : !shownId ? (
        emptyNode
      ) : (
        <div
          className="grid min-w-0 gap-4 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)] lg:items-start"
          {...{ [APPROVAL_QUEUE_REGION]: '' }}
        >
          {items.length > 0 ? (
            <ApprovalQueue
              items={items}
              flaggedId={flaggedId}
              selectedId={shownId}
              onSelect={select}
            />
          ) : (
            emptyNode
          )}
          <div ref={panelRef} className="min-w-0 scroll-mt-4">
            <ApprovalDetailPanel
              key={shownId}
              approvalId={shownId}
              approval={items.find((a) => a.id === shownId)}
            />
          </div>
        </div>
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
