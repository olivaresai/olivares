// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The dual-control re-enable (POST /killswitch/{id}/reenable). NEVER unilateral:
// the first POST opens the approval (security.killswitch.reenable — CRITICAL,
// two distinct humans, anti-self-approval, AAL3 per decision) and answers 202 with
// the pending envelope; the two decisions happen in the approvals view
// (/permissions); re-POSTing reports progress and the call that finds the approval
// satisfied flips the stop (200). This dialog drives that loop: request → show the
// approval id + progress + the link where the humans decide → check again → done
// ("post-review due"). 409s (dual_control_required, prior unreviewed incident)
// surface with the engine's message — they are the design, not failures.
import { Link } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
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
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { StatusBadge } from '@/components/data/badges'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { killswitchApi, killswitchKeys } from './api'
import './i18n'
import { isReenablePending } from './types'
import type { KillSwitchDTO, ReenablePendingDTO } from './types'

export interface ReenableDialogProps {
  stop: KillSwitchDTO
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function ReenableDialog({
  stop,
  open,
  onOpenChange,
}: ReenableDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        {open && (
          <ReenableBody stop={stop} onClose={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function ReenableBody({
  stop,
  onClose,
}: {
  stop: KillSwitchDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['killswitch', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()

  const [reason, setReason] = useState('')
  // The 202 envelope of the LAST POST — while set, the dialog shows the
  // dual-control progress instead of the request form.
  const [pending, setPending] = useState<ReenablePendingDTO | null>(null)

  const reenable = useMutation({
    mutationFn: () =>
      killswitchApi.reenable(
        stop.id,
        reason.trim() ? { reason: reason.trim() } : {},
      ),
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({
        queryKey: killswitchKeys.all(activeTenant),
      })
      if (isReenablePending(res)) {
        setPending(res)
        return
      }
      toast.success(t('reenable.done'), {
        description: t('reenable.reviewDue'),
      })
      onClose()
    },
    onError: (err) => {
      // ⛔ ASEGURAMIENTO ANTES QUE ROL. `isForbidden` es SÓLO el status (lib/api/errors.ts:59)
      // y un `step_up_required` lo satisface también, así que leerlo primero acusaba al
      // operador de no tener un permiso que SÍ tiene, justo en el diálogo que reactiva un
      // kill-switch. Aquí NO se delega la rama de rol como en sus dos hermanas: esta lleva
      // `description: err.message`, que es el mensaje del motor explicando la salida, y
      // `report` sólo pone la advertencia pelada. Perder ese texto sería cambiar un mensaje
      // exacto por uno genérico, que es la otra forma de mentirle al operador.
      if (err instanceof ApiError && err.isStepUpRequired) {
        report(err, () => reenable.mutate())
        return
      }
      if (err instanceof ApiError && err.isForbidden) {
        toast.warning(t('common:privileged.notAuthorizedToast'), {
          description: err.message,
        })
        return
      }
      if (err instanceof ApiError && err.status === 409) {
        // dual_control_required / prior unreviewed incident / no longer active:
        // the engine's message explains the path out — relay it calmly.
        toast.warning(
          err.code === 'dual_control_required'
            ? t('reenable.dualControl')
            : t('reenable.blocked'),
          { description: err.message },
        )
        return
      }
      const description =
        err instanceof Error && err.message ? err.message : undefined
      toast.error(
        t('errors:generic'),
        description ? { description } : undefined,
      )
    },
  })

  // An approval may already be bound to the stop from an earlier request (this
  // session or another operator's) — say so, and let the POST report its state.
  const existingApproval = pending?.approval.id ?? stop.reenable_approval

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('reenable.title')}</DialogTitle>
        <DialogDescription>{t('reenable.body')}</DialogDescription>
      </DialogHeader>

      {pending ? (
        <div className="flex flex-col gap-3">
          <p className="text-sm font-medium">{t('reenable.pendingTitle')}</p>
          <p className="text-xs text-muted-foreground">
            {t('reenable.pendingBody')}
          </p>
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">
              {t('reenable.approvalId')}
            </span>
            <span className="font-mono text-xs">{pending.approval.id}</span>
            <StatusBadge status={pending.approval.status} />
            <Badge variant="info">
              {t('reenable.progress', {
                approved: pending.approval.approve_count,
                required: pending.approval.required_approvals,
              })}
            </Badge>
          </div>
          <Link
            to={'/permissions' as never}
            className="text-sm text-accent-text underline-offset-4 hover:underline"
          >
            {t('reenable.goToApprovals')}
          </Link>
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          {existingApproval && (
            <p className="text-xs text-muted-foreground">
              {t('reenable.existingApproval', { id: existingApproval })}
            </p>
          )}
          <Field
            label={t('reenable.reason')}
            htmlFor="ks-reenable-reason"
            description={t('reenable.reasonHint')}
          >
            <Textarea
              id="ks-reenable-reason"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              rows={2}
            />
          </Field>
        </div>
      )}

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={reenable.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={() => reenable.mutate()}
          disabled={reenable.isPending}
        >
          {reenable.isPending && <Spinner size="sm" aria-hidden />}
          {pending || existingApproval
            ? t('reenable.check')
            : t('reenable.request')}
        </Button>
      </DialogFooter>
    </>
  )
}
