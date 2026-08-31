// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// RecordingNotice — the AC-8 monitoring notice for recorded privileged surfaces
//. Privileged views mount it under their PageHeader with their module
// namespace; it queries /v1/m/recording/notice and renders a quiet IntelNotice
// strip when that namespace is recorded (the SelfAuditNotice precedent). When the
// tenant's consent mode is "required" and the operator has not acknowledged yet,
// it renders a BLOCKING dialog — the backend is the authority (privileged routes
// 403 `recording_consent_required` until /ack), this dialog is how the operator
// answers it. "Acknowledge and continue" POSTs /ack and invalidates the recording
// queries; "Cancel" navigates back.
import { useQuery } from '@tanstack/react-query'
import { CircleDot } from 'lucide-react'
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
import { Spinner } from '@/components/ui/spinner'
import { IntelNotice } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { recordingApi, recordingKeys } from './api'
import './i18n'

export function RecordingNotice({
  namespace,
  always = false,
  className,
}: {
  /** The mounting view's module namespace (matched against recorded_namespaces). */
  namespace: string
  /** Render the strip regardless of the recorded set (the recordings view itself). */
  always?: boolean
  className?: string
}) {
  const { t } = useTranslation('recordings')
  const { activeTenant } = useAuth()

  const noticeQuery = useQuery({
    queryKey: recordingKeys.notice(activeTenant),
    queryFn: () => recordingApi.notice(),
  })

  const ack = usePrivilegedMutation({
    mutationFn: () => recordingApi.acknowledge(),
    invalidateKeys: [recordingKeys.all(activeTenant)],
    successMessage: t('consent.ackDone'),
  })

  // Quiet while loading or on a failed read — the strip is informational; the
  // backend stays deny-closed on its own (403 recording_consent_required). A
  // malformed/empty body (no recorded_namespaces) is treated as "not recorded",
  // never a crash, so a transient bad read can't take down a recorded surface.
  const notice = noticeQuery.data
  if (!notice) return null
  if (!always && !notice.recorded_namespaces?.includes(namespace)) return null

  return (
    <>
      <IntelNotice tone="info" icon={<CircleDot />} className={className}>
        <span className="text-muted-foreground">{t('notice.strip')}</span>
      </IntelNotice>

      {/* Blocking AC-8 acknowledgement: controlled open with no onOpenChange and
          no X close, so the only exits are the two explicit actions below. */}
      <Dialog open={notice.consent_required}>
        <DialogContent hideClose>
          <DialogHeader>
            <DialogTitle>{t('consent.title')}</DialogTitle>
            <DialogDescription>{t('consent.body')}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="secondary"
              onClick={() => window.history.back()}
              disabled={ack.isPending}
            >
              {t('consent.cancel')}
            </Button>
            <Button
              variant="primary"
              onClick={() => ack.mutate(undefined)}
              disabled={ack.isPending}
            >
              {ack.isPending && <Spinner size="sm" aria-hidden />}
              {t('consent.ack')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
