// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// SSE-connected job progress display for backup and restore operations. Subscribes
// to the engine's job stream and renders the phase, a progress bar, and the status
// badge. LiveDot shows the honest connection state. The web adds no logic — the
// engine owns progress computation (ARCHITECTURE.md SS8).
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { useLiveStream } from '@/features/shared/sse'
import { LiveDot } from '@/features/shared/live-dot'
import { drApi } from './api'
import type { DRJob } from './types'

const STATUS_VARIANT: Record<string, 'neutral' | 'success' | 'danger'> = {
  running: 'neutral',
  completed: 'success',
  failed: 'danger',
}

export interface JobProgressProps {
  jobId: string
  /** Called when the job reaches a terminal state (completed or failed). */
  onFinished?: (job: DRJob) => void
}

export function JobProgress({ jobId, onFinished }: JobProgressProps) {
  const { t } = useTranslation('backups')
  const [job, setJob] = useState<DRJob | null>(null)

  const onSnapshot = useCallback(
    (snapshot: DRJob) => {
      setJob(snapshot)
      if (snapshot.status === 'completed' || snapshot.status === 'failed') {
        onFinished?.(snapshot)
      }
    },
    [onFinished],
  )

  const { status: streamStatus } = useLiveStream<DRJob>({
    path: drApi.jobStreamPath(jobId),
    onSnapshot,
    events: ['job'],
    enabled: !!jobId,
  })

  const progress = job?.progress ?? 0
  const phase = job?.phase ?? t('job.initializing')
  const jobStatus = job?.status ?? 'running'

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium">{phase}</span>
        <div className="flex items-center gap-2">
          <Badge variant={STATUS_VARIANT[jobStatus] ?? 'neutral'}>
            {t(`job.status.${jobStatus}`)}
          </Badge>
          <LiveDot status={streamStatus} />
        </div>
      </div>

      {/* Progress bar */}
      <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
        <div
          className="h-full rounded-full bg-accent-text transition-[width] duration-300 ease-out"
          style={{ width: `${Math.min(100, Math.max(0, progress))}%` }}
          role="progressbar"
          aria-valuenow={progress}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-label={t('job.progressAria', { phase, progress })}
        />
      </div>

      <p className="text-xs tabular-nums text-muted-foreground">
        {t('job.complete', { progress })}
      </p>

      {job?.error && (
        <p className="break-words text-sm text-danger">{job.error}</p>
      )}
    </div>
  )
}
