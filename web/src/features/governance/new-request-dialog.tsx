// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ScrollText } from 'lucide-react'
import { useState } from 'react'
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
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { governanceApi, governanceKeys } from './api'
import './i18n'
import type { ApprovalDTO, CreateApprovalRequest } from './types'

export interface NewRequestDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * NewRequestDialog opens an approval request (POST /approvals). Privileged + audited:
 * the form is the deliberate confirmation surface (audit notice + explicit submit),
 * then the privileged mutation invalidates the queue, toasts, and closes. A matching
 * approval POLICY is authoritative for the threshold/windows — the requester cannot
 * lower their own bar, which the hint makes explicit.
 *
 * The form mounts fresh each time the dialog opens (Radix unmounts closed content),
 * so plain useState initializers seed it — no resetting effect.
 */
export function NewRequestDialog({
  open,
  onOpenChange,
}: NewRequestDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-xl overflow-y-auto">
        {open && <RequestForm onClose={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function RequestForm({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant } = useAuth()

  const [action, setAction] = useState('')
  const [subjectKind, setSubjectKind] = useState('')
  const [subjectRef, setSubjectRef] = useState('')
  const [reason, setReason] = useState('')
  const [requiredApprovals, setRequiredApprovals] = useState('')
  const [expiresIn, setExpiresIn] = useState('')
  const [escalateIn, setEscalateIn] = useState('')

  const valid = action.trim().length > 0

  const mutation = usePrivilegedMutation<CreateApprovalRequest, ApprovalDTO>({
    mutationFn: (input) => governanceApi.createApproval(input),
    invalidateKeys: () => [governanceKeys.approvals(activeTenant)],
    successMessage: t('newRequest.created'),
    onDone: onClose,
  })

  function submit() {
    if (!valid) return
    const payload: CreateApprovalRequest = {
      action: action.trim(),
      ...(subjectKind.trim() ? { subject_kind: subjectKind.trim() } : {}),
      ...(subjectRef.trim() ? { subject_ref: subjectRef.trim() } : {}),
      ...(reason.trim() ? { reason: reason.trim() } : {}),
      ...(requiredApprovals.trim()
        ? { required_approvals: Number(requiredApprovals) || 0 }
        : {}),
      ...(expiresIn.trim()
        ? { expires_in_seconds: Number(expiresIn) || 0 }
        : {}),
      ...(escalateIn.trim()
        ? { escalate_in_seconds: Number(escalateIn) || 0 }
        : {}),
    }
    mutation.mutate(payload)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('newRequest.title')}</DialogTitle>
        <DialogDescription>{t('newRequest.body')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('newRequest.action')}
          htmlFor="req-action"
          description={t('newRequest.actionHint')}
          required
        >
          <Input
            id="req-action"
            value={action}
            onChange={(e) => setAction(e.target.value)}
            mono
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={t('newRequest.subjectKind')}
            htmlFor="req-subject-kind"
            description={t('newRequest.subjectKindHint')}
          >
            <Input
              id="req-subject-kind"
              value={subjectKind}
              onChange={(e) => setSubjectKind(e.target.value)}
              mono
            />
          </Field>
          <Field
            label={t('newRequest.subjectRef')}
            htmlFor="req-subject-ref"
            description={t('newRequest.subjectRefHint')}
          >
            <Input
              id="req-subject-ref"
              value={subjectRef}
              onChange={(e) => setSubjectRef(e.target.value)}
              mono
            />
          </Field>
        </div>

        <Field
          label={t('newRequest.reason')}
          htmlFor="req-reason"
          description={t('newRequest.reasonHint')}
        >
          <Textarea
            id="req-reason"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            rows={2}
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-3">
          <Field
            label={t('newRequest.requiredApprovals')}
            htmlFor="req-required"
            description={t('newRequest.requiredApprovalsHint')}
          >
            <Input
              id="req-required"
              type="number"
              min={0}
              max={64}
              value={requiredApprovals}
              onChange={(e) => setRequiredApprovals(e.target.value)}
              mono
            />
          </Field>
          <Field label={t('newRequest.expiresInSeconds')} htmlFor="req-expires">
            <Input
              id="req-expires"
              type="number"
              min={0}
              max={31536000}
              value={expiresIn}
              onChange={(e) => setExpiresIn(e.target.value)}
              mono
            />
          </Field>
          <Field
            label={t('newRequest.escalateInSeconds')}
            htmlFor="req-escalate"
          >
            <Input
              id="req-escalate"
              type="number"
              min={0}
              max={31536000}
              value={escalateIn}
              onChange={(e) => setEscalateIn(e.target.value)}
              mono
            />
          </Field>
        </div>
      </div>

      <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <ScrollText className="size-3.5 shrink-0" aria-hidden />
        {t('common:privileged.auditedNotice')}
      </p>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={submit}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('newRequest.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}
