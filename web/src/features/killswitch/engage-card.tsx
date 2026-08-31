// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The one-click emergency stop (POST /killswitch). Engage is deliberately CHEAP
// server-side (admin-tier + mandatory reason; no approval gate, no AAL bar — see
// modules/governance/killswitch.go header), so the console's job is deliberation,
// not friction: scope selection, a mandatory justification and ONE danger-toned
// confirm step. A 409 means the scope is already stopped — the existing stop is
// surfaced (state refetch + the engine's message naming it), never swallowed.
import { OctagonAlert } from 'lucide-react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { killswitchApi, killswitchKeys } from './api'
import './i18n'
import type { EngageKillSwitchRequest, KillSwitchScopeKind } from './types'

export function EmergencyStopCard() {
  const { t } = useTranslation(['killswitch', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()

  const [scope, setScope] = useState<KillSwitchScopeKind>('estate')
  const [agentRef, setAgentRef] = useState('')
  const [reason, setReason] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)

  // The reason is MANDATORY (the engine 400s without it) and an agent stop needs
  // its ref — the button stays disabled until the operator supplied both.
  const valid =
    reason.trim().length > 0 &&
    (scope === 'estate' || agentRef.trim().length > 0)

  const invalidate = () =>
    queryClient.invalidateQueries({
      queryKey: killswitchKeys.all(activeTenant),
    })

  const engage = useMutation({
    mutationFn: (input: EngageKillSwitchRequest) => killswitchApi.engage(input),
    onSuccess: async (dto) => {
      await invalidate()
      toast.success(t('engage.done'), {
        description: t('engage.doneBody', { id: dto.id }),
      })
      setConfirmOpen(false)
      setReason('')
      setAgentRef('')
    },
    onError: async (err, input) => {
      setConfirmOpen(false)
      if (err instanceof ApiError && err.status === 409) {
        // The scope is already stopped: refetch the live posture so the existing
        // stop shows in the banner/table, and relay the engine's message (it
        // names the stop id and the dual-control path out).
        await invalidate()
        toast.warning(t('engage.alreadyStopped'), { description: err.message })
        return
      }
      report(err, () => engage.mutate(input))
    },
  })

  function payload(): EngageKillSwitchRequest {
    return {
      scope_kind: scope,
      ...(scope === 'agent' ? { scope_ref: agentRef.trim() } : {}),
      reason: reason.trim(),
    }
  }

  const isEstate = scope === 'estate'

  return (
    <Card className="border-danger-line">
      <CardHeader>
        <div className="flex flex-col gap-1">
          <CardTitle className="flex items-center gap-2 text-danger">
            <OctagonAlert className="size-4 shrink-0" aria-hidden />
            {t('engage.title')}
          </CardTitle>
          <CardDescription>{t('engage.caption')}</CardDescription>
        </div>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('engage.scope')} htmlFor="ks-scope">
            <Select
              value={scope}
              onValueChange={(v) => setScope(v as KillSwitchScopeKind)}
            >
              <SelectTrigger id="ks-scope">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="estate">
                  {t('engage.scopeEstate')}
                </SelectItem>
                <SelectItem value="agent">{t('engage.scopeAgent')}</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          {!isEstate && (
            <Field
              label={t('engage.agentRef')}
              htmlFor="ks-agent-ref"
              description={t('engage.agentRefHint')}
              required
            >
              <Input
                id="ks-agent-ref"
                value={agentRef}
                onChange={(e) => setAgentRef(e.target.value)}
                mono
              />
            </Field>
          )}
        </div>

        <Field
          label={t('engage.reason')}
          htmlFor="ks-reason"
          description={t('engage.reasonHint')}
          required
        >
          <Textarea
            id="ks-reason"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            rows={2}
          />
        </Field>

        <div className="flex justify-end">
          <Button
            variant="destructive-solid"
            disabled={!valid || engage.isPending}
            onClick={() => setConfirmOpen(true)}
          >
            <OctagonAlert aria-hidden />
            {t('engage.button')}
          </Button>
        </div>
      </CardContent>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={(o) => !engage.isPending && setConfirmOpen(o)}
        title={
          isEstate
            ? t('engage.confirmTitleEstate')
            : t('engage.confirmTitleAgent')
        }
        description={
          isEstate
            ? t('engage.confirmBodyEstate')
            : t('engage.confirmBodyAgent')
        }
        tone="danger"
        confirmLabel={t('engage.confirm')}
        pending={engage.isPending}
        onConfirm={() => engage.mutate(payload())}
      />
    </Card>
  )
}
