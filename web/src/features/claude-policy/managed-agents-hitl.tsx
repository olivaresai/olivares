// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Managed-Agents HITL (ANT2-14, differentiator → post-v1). The user.tool_confirmation
// case for Managed Agents: a tool call pauses the session via
// stop_reason:requires_action, modelled by the connector as a pending-approval
// Finding. The PRIMARY thread sees only sub-agent start/end, so the concrete
// tool to confirm lives in the thread events.
//
// PROVENANCE (honest): the pending findings are REAL (security findings,
// subject_kind=anthropic.managed_agent). The thread-events read and the allow/deny
// emission are DECLARED — no engine /v1/m route re-exposes them yet (ingest is
//), and the events path differs between the live docs (/v1/sessions/{id}/
// events) and the connector (/threads/:tid/events). Shown with a pending seam.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { Check, X } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import {
  AsyncSection,
  CaveatNotice,
  SectionCard,
  SeamBadge,
  SelfAuditNotice,
  ListTruncationBadge,
} from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { claudePolicyApi, claudePolicyKeys, isContractPending } from './api'
import { ContractPendingNotice, DeclaredSection } from './components'
import type { PolicyDriftFinding, ThreadEvent } from './types'

export function ManagedAgentsHitl({ active }: { active: boolean }) {
  const { t } = useTranslation('claudePolicy')
  const { activeTenant, can } = useAuth()
  const canRead = can('governance:approval:read')

  const query = useQuery({
    queryKey: claudePolicyKeys.hitl(activeTenant),
    queryFn: () => claudePolicyApi.listManagedAgentHitl(),
    enabled: canRead && active,
  })

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <SeamBadge label={t('hitl.postV1')} />
        <span className="text-xs text-muted-foreground">
          {t('hitl.postV1Hint')}
        </span>
      </div>
      <SelfAuditNotice />
      <CaveatNotice tone="warning">{t('hitl.vaultsRisk')}</CaveatNotice>
      <CaveatNotice tone="info">{t('hitl.endpointNote')}</CaveatNotice>

      <SectionCard title={t('hitl.title')} description={t('hitl.subtitle')}>
        <ListTruncationBadge
          query={query}
          label={t('hitl.truncated', { n: query.data?.items?.length ?? 0 })}
          hint={t('hitl.truncatedHint')}
        />
        <AsyncSection query={query}>
          {(data) => {
            const items = (data.items ?? []).filter(
              (f) => f.subject_kind === 'anthropic.managed_agent',
            )
            if (items.length === 0) {
              return (
                <EmptyState
                  title={t('hitl.emptyTitle')}
                  description={t('hitl.emptyBody')}
                />
              )
            }
            return (
              <ul
                className="flex flex-col gap-2"
                aria-label={t('hitl.listLabel')}
              >
                {items.map((f) => (
                  <HitlRow
                    key={f.id}
                    finding={f}
                    canDecide={can('governance:approval:admin')}
                  />
                ))}
              </ul>
            )
          }}
        </AsyncSection>
      </SectionCard>
    </div>
  )
}

function HitlRow({
  finding,
  canDecide,
}: {
  finding: PolicyDriftFinding
  canDecide: boolean
}) {
  const { t } = useTranslation('claudePolicy')
  const [open, setOpen] = useState(false)
  const sessionRef = finding.subject_ref ?? ''
  const detailId = `hitl-detail-${finding.id}`

  return (
    <li className="flex flex-col gap-1 rounded-md border border-border bg-surface px-3 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="warning">{t('hitl.pending')}</Badge>
        <span className="text-sm text-foreground">
          {finding.title ?? t('hitl.title')}
        </span>
        {finding.occurred_at && (
          <span className="text-xs text-muted-foreground">
            <RelTimeLabel ts={finding.occurred_at} />
          </span>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-x-3 text-xs text-muted-foreground">
        <span>
          {t('hitl.session')}:{' '}
          <code className="font-mono">{sessionRef || '—'}</code>
        </span>
        {finding.detail_hash && (
          <span title={finding.detail_hash}>
            {t('drift.fingerprint')}:{' '}
            <code className="font-mono">
              {finding.detail_hash.slice(0, 16)}…
            </code>
          </span>
        )}
        <Button
          variant="ghost"
          size="sm"
          className="ml-auto"
          aria-expanded={open}
          aria-controls={detailId}
          onClick={() => setOpen((o) => !o)}
        >
          {t('hitl.review')}
        </Button>
      </div>
      {open && (
        <HitlDetail
          id={detailId}
          sessionRef={sessionRef}
          canDecide={canDecide}
        />
      )}
    </li>
  )
}

function HitlDetail({
  id,
  sessionRef,
  canDecide,
}: {
  id: string
  sessionRef: string
  canDecide: boolean
}) {
  const report = useFailedActionReporter()
  const { t } = useTranslation('claudePolicy')
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const [confirm, setConfirm] = useState<{
    ev: ThreadEvent
    result: 'allow' | 'deny'
  } | null>(null)
  const [confirmPending, setConfirmPending] = useState(false)

  const eventsQuery = useQuery({
    queryKey: claudePolicyKeys.threadEvents(activeTenant, sessionRef),
    queryFn: () => claudePolicyApi.threadEvents(sessionRef),
    enabled: !!sessionRef,
  })

  const confirmMutation = useMutation({
    mutationFn: ({
      ev,
      result,
    }: {
      ev: ThreadEvent
      result: 'allow' | 'deny'
    }) =>
      claudePolicyApi.confirmTool(sessionRef, {
        tool_use_id: ev.tool_use_id ?? ev.id ?? '',
        result,
      }),
    onSuccess: async (_d, vars) => {
      setConfirm(null)
      await queryClient.invalidateQueries({
        queryKey: claudePolicyKeys.hitl(activeTenant),
      })
      toast.success(
        vars.result === 'allow' ? t('hitl.allowed') : t('hitl.denied'),
      )
    },
    onError: (e) => {
      if (isContractPending(e)) {
        setConfirmPending(true)
        setConfirm(null)
        return
      }
      // La ceremonia DELANTE, y la copy propia de esta pantalla se queda: cambiarla por la
      // genérica sería perder un mensaje exacto. La limpieza se repite en las dos ramas,
      // porque el diálogo debe cerrarse en cualquiera de las dos negativas.
      if (e instanceof ApiError && e.isStepUpRequired) {
        setConfirm(null)
        report(e)
        return
      }
      if (e instanceof ApiError && e.isForbidden) {
        toast.warning(t('hitl.notAuthorized'))
        setConfirm(null)
        return
      }
      toast.error(t('hitl.confirmError'))
    },
  })

  return (
    <div
      id={id}
      className="mt-2 rounded-md border border-border bg-muted/30 p-2"
    >
      <DeclaredSection
        query={eventsQuery}
        what={t('hitl.threadEventsWhat')}
        skeletonHeight={80}
      >
        {(data) => {
          const toolEvents = (data.items ?? []).filter((e) => e.tool_name)
          if (toolEvents.length === 0) {
            return (
              <p className="text-xs text-muted-foreground">
                {t('hitl.noToolEvents')}
              </p>
            )
          }
          return (
            <ul className="flex flex-col gap-1.5">
              {toolEvents.map((ev, i) => (
                <li
                  key={ev.id ?? i}
                  className="flex flex-wrap items-center gap-2 text-xs"
                >
                  <span>
                    {t('hitl.tool')}:{' '}
                    <code className="font-mono text-foreground">
                      {ev.tool_name}
                    </code>
                  </span>
                  {ev.agent_ref && (
                    <code className="font-mono text-muted-foreground">
                      {ev.agent_ref}
                    </code>
                  )}
                  {canDecide && (
                    <span className="ml-auto flex gap-1">
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => setConfirm({ ev, result: 'allow' })}
                      >
                        <Check /> {t('hitl.allow')}
                      </Button>
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => setConfirm({ ev, result: 'deny' })}
                      >
                        <X /> {t('hitl.deny')}
                      </Button>
                    </span>
                  )}
                </li>
              ))}
            </ul>
          )
        }}
      </DeclaredSection>

      {confirmPending && <ContractPendingNotice what={t('hitl.confirmWhat')} />}

      <ConfirmDialog
        open={!!confirm}
        onOpenChange={(o) => {
          if (!o && !confirmMutation.isPending) setConfirm(null)
        }}
        tone="danger"
        title={
          confirm?.result === 'deny'
            ? t('hitl.confirmDenyTitle')
            : t('hitl.confirmAllowTitle')
        }
        description={t('hitl.confirmBody', {
          tool: confirm?.ev.tool_name ?? '',
        })}
        pending={confirmMutation.isPending}
        onConfirm={() => confirm && confirmMutation.mutate(confirm)}
      />
    </div>
  )
}
