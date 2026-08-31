// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Dual-control second step: restores awaiting a second approver. A restore
// requested by one admin (when require_dual_control_restore is on) appears here for a
// DIFFERENT admin to approve, supplying the passphrase. The server enforces the
// distinct-ACCOUNT rule — it compares the stable user behind each credential, not the
// credential's actor string, so a requester cannot approve their own restore through a
// token they minted; this panel only surfaces the queue and the approve action.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { UserCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
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
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { formatDateTime } from '@/lib/format'
import { drApi, drKeys } from './api'
import { JobProgress } from './job-progress'
import type { PendingRestore } from './types'

export function PendingRestores() {
  const { t } = useTranslation('backups')
  const pendingQ = useQuery({
    queryKey: drKeys.pending(),
    queryFn: () => drApi.listPendingRestores(),
  })
  const [approve, setApprove] = useState<PendingRestore | null>(null)

  const items = pendingQ.data?.items ?? []
  if (items.length === 0) return null

  return (
    <div className="mb-5 rounded-lg border border-amber-500/40 bg-amber-500/5 p-4">
      <div className="mb-3 flex items-center gap-2">
        <UserCheck className="size-4 text-amber-500" />
        <h3 className="text-sm font-medium">{t('pending.title')}</h3>
      </div>
      <ul className="flex flex-col gap-2">
        {items.map((p) => (
          <li
            key={p.request_id}
            className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border bg-surface px-3 py-2 text-sm"
          >
            <div className="flex flex-col">
              <span>
                {/* Show the ACCOUNT, falling back to the credential actor for a
                    request recorded before the server stored one. The approver is
                    being asked "are you someone else?", and "token:<id>" does not
                    let them answer that. */}
                {t('pending.requestedByPrefix')}{' '}
                <strong>{p.initiator_user || p.initiator}</strong>{' '}
                · {formatDateTime(p.created_at)}
              </span>
              <span className="font-mono text-xs text-muted-foreground">
                {p.initiator_user ? `${p.initiator} · ` : ''}
                {p.request_id}
              </span>
            </div>
            <Button variant="primary" size="sm" onClick={() => setApprove(p)}>
              <UserCheck className="size-4" />
              {t('pending.approveRestore')}
            </Button>
          </li>
        ))}
      </ul>

      {approve && (
        <ApproveDialog pending={approve} onClose={() => setApprove(null)} />
      )}
    </div>
  )
}

function ApproveDialog({
  pending,
  onClose,
}: {
  pending: PendingRestore
  onClose: () => void
}) {
  const { t } = useTranslation('backups')
  const [passphrase, setPassphrase] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)

  const approveMutation = usePrivilegedMutation({
    mutationFn: () =>
      drApi.approveRestore(pending.upload_id, {
        request_id: pending.request_id,
        passphrase: passphrase.trim(),
      }),
    invalidateKeys: [drKeys.pending(), drKeys.backups(), drKeys.jobs()],
    successMessage: t('pending.approved'),
    onDone: (data) => {
      setConfirmOpen(false)
      if (data.job_id) setJobId(data.job_id)
    },
  })

  return (
    <>
      <Dialog open onOpenChange={(o) => !o && onClose()}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{t('pending.dialogTitle')}</DialogTitle>
            <DialogDescription>
              {/* The account, not the credential: this sentence tells the approver
                  they must be someone different. */}
              {t('pending.dialogDescription', {
                initiator: pending.initiator_user || pending.initiator,
              })}
            </DialogDescription>
          </DialogHeader>

          {jobId ? (
            <JobProgress jobId={jobId} onFinished={() => {}} />
          ) : (
            <Field
              label={t('pending.passphrase')}
              htmlFor="approve-passphrase"
              description={t('pending.passphraseDescription')}
            >
              <Input
                id="approve-passphrase"
                type="password"
                value={passphrase}
                onChange={(e) => setPassphrase(e.target.value)}
                autoComplete="off"
              />
            </Field>
          )}

          <DialogFooter>
            <Button variant="secondary" onClick={onClose}>
              {jobId ? t('actions.close') : t('actions.cancel')}
            </Button>
            {!jobId && (
              <Button
                variant="primary"
                onClick={() => setConfirmOpen(true)}
                disabled={!passphrase.trim()}
              >
                <UserCheck className="size-4" />
                {t('pending.approveRestore')}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('pending.confirmTitle')}
        description={t('pending.confirmDescription')}
        tone="danger"
        confirmPhrase={t('restore.confirmPhrase')}
        confirmLabel={t('pending.confirmLabel')}
        pending={approveMutation.isPending}
        onConfirm={() => approveMutation.mutate()}
      />
    </>
  )
}
