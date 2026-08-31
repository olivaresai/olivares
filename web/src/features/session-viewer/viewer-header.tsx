// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ViewerHeader — session metadata header for the recording viewer. Shows status
// badge, subject, chain anchors, timestamps, and verification verdict via
// KvList. All recording actions are reachable from this header.
import { Link } from '@tanstack/react-router'
import {
  ArrowLeft,
  Disc3,
  Download,
  FileText,
  Lock,
  ShieldCheck,
  Sparkles,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { StatusBadge } from '@/components/data/badges'
import { Badge, badgeVariants } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { KvList, KvRow } from '@/components/ui/kv'
import { PageHeader } from '@/components/ui/page-header'
import { HashChip } from '@/features/_intel'
import { RelTimeLabel, humanDurationSeconds } from '@/features/shared'
import { formatInt } from '@/lib/format'
import type { SessionDTO } from '@/features/recordings/types'
import type { LiveCorrelation, VerifyResult } from './types'
import './i18n'

export interface ViewerHeaderProps {
  session: SessionDTO
  live: LiveCorrelation | null
  verify: VerifyResult | null
  /** Callback for the Export JSON action. */
  onExportJSON: () => void
  /** Callback for the Export Summary action. */
  onExportSummary: () => void
  /** Callback for the Verify Chain action. */
  onVerify: () => void
  /** Callback that opens the irreversible seal confirmation. */
  onSeal: () => void
  /** Callback for the optional AI-derived summary action. */
  onSummarize: () => void
  /** True while an export or recording action is pending. */
  exporting?: boolean
  verifying?: boolean
  sealing?: boolean
  summarizing?: boolean
  /** Fresh verdict from GET /verify; passive is the inline /unified verdict. */
  freshVerify?: VerifyResult | null
}

export function ViewerHeader({
  session,
  live,
  verify,
  onExportJSON,
  onExportSummary,
  onVerify,
  onSeal,
  onSummarize,
  exporting,
  verifying,
  sealing,
  summarizing,
  freshVerify,
}: ViewerHeaderProps) {
  const { t, i18n } = useTranslation('session-viewer')
  const lang = i18n.language

  // Compute duration if we have both timestamps.
  const durationSec =
    session.opened_at && session.last_at
      ? Math.max(
          0,
          Math.floor(
            (new Date(session.last_at).getTime() -
              new Date(session.opened_at).getTime()) /
              1000,
          ),
        )
      : null

  return (
    <div className="flex flex-col gap-4">
      <Button variant="ghost" size="sm" asChild className="self-start">
        <Link to={'/recordings' as never}>
          <ArrowLeft className="size-3.5" />
          {t('back')}
        </Link>
      </Button>

      <PageHeader
        icon={Disc3}
        title={t('title')}
        description={t('subtitle')}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="secondary"
              size="sm"
              onClick={onVerify}
              disabled={verifying}
            >
              <ShieldCheck className="size-3.5" />
              {t('export.verify')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={onSummarize}
              disabled={summarizing}
            >
              <Sparkles className="size-3.5" />
              {t('summarize.action')}
            </Button>
            {session.status === 'active' && (
              <Button
                variant="destructive"
                size="sm"
                onClick={onSeal}
                disabled={sealing}
              >
                <Lock className="size-3.5" />
                {t('seal.action')}
              </Button>
            )}
            <Button
              variant="secondary"
              size="sm"
              onClick={onExportJSON}
              disabled={exporting}
            >
              <Download className="size-3.5" />
              {t('export.json')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={onExportSummary}
              disabled={exporting}
            >
              <FileText className="size-3.5" />
              {t('export.summary')}
            </Button>
          </div>
        }
      />

      <KvList>
        <KvRow label={t('header.status')}>
          <span className="inline-flex items-center gap-1.5">
            <StatusBadge status={session.status} />
            {live && (
              <Link
                to={'/sessions' as never}
                title={live.session_ref}
                className={badgeVariants({ variant: 'success' })}
              >
                {t('header.live')}
              </Link>
            )}
          </span>
        </KvRow>
        <KvRow label={t('header.subject')} mono align="start">
          {session.subject}
        </KvRow>
        <KvRow label={t('header.opened')}>
          <RelTimeLabel ts={session.opened_at} />
        </KvRow>
        {durationSec !== null && (
          <KvRow label={t('header.duration')} mono>
            {humanDurationSeconds(durationSec)}
          </KvRow>
        )}
        <KvRow label={t('header.frames')} mono>
          {formatInt(session.frames_written, lang)} /{' '}
          {formatInt(session.frames_reserved, lang)}
        </KvRow>
        <KvRow label={t('header.tipHash')} align="start">
          <HashChip hash={session.tip_hash} label="tip_hash" />
        </KvRow>
        <KvRow label={t('header.openSeq')} mono>
          {(session.open_seq ?? 0) > 0 ? session.open_seq : '—'}
        </KvRow>
        <KvRow label={t('header.anchorSeq')} mono>
          {(session.anchor_seq ?? 0) > 0 ? session.anchor_seq : '—'}
        </KvRow>
        <KvRow label={t('header.sealSeq')} mono>
          {(session.seal_seq ?? 0) > 0 ? session.seal_seq : '—'}
        </KvRow>
      </KvList>

      {(freshVerify ?? verify) && (
        <VerificationVerdict
          result={(freshVerify ?? verify)!}
          fresh={!!freshVerify}
        />
      )}
    </div>
  )
}

function VerificationVerdict({
  result,
  fresh,
}: {
  result: VerifyResult
  fresh: boolean
}) {
  const { t } = useTranslation('session-viewer')

  return (
    <section
      className="flex flex-col gap-2 rounded-lg border border-border bg-background p-3"
      aria-label={t(fresh ? 'verify.fresh' : 'verify.passive')}
    >
      <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {t(fresh ? 'verify.fresh' : 'verify.passive')}
      </p>
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant={result.ok ? 'success' : 'danger'}>
          {result.ok ? t('verify.intact') : t('verify.broken')}
        </Badge>
        <Badge variant={result.tip_match ? 'success' : 'danger'}>
          {result.tip_match ? t('verify.tipMatch') : t('verify.tipMismatch')}
        </Badge>
        <Badge variant={result.anchors_ok ? 'success' : 'danger'}>
          {result.anchors_ok
            ? t('verify.anchorsOk', { count: result.anchors_checked })
            : t('verify.anchorsBroken', { count: result.anchors_checked })}
        </Badge>
        <Badge variant="outline">
          {t('verify.frames', { count: result.frames_checked })}
        </Badge>
        {result.gap && <Badge variant="danger">{t('verify.gap')}</Badge>}
      </div>
      <p className="text-xs text-muted-foreground">
        {t('verify.anchoredThrough', { idx: result.anchored_through })}
      </p>
      {!result.ok && (result.reason || result.break_at != null) && (
        <p className="text-xs font-medium text-danger">
          {t('verify.breakAt', {
            idx: result.break_at ?? '—',
            reason: result.reason ?? '—',
          })}
        </p>
      )}
      {(result.anchor_failures?.length ?? 0) > 0 && (
        <ol className="flex flex-col gap-1" aria-label={t('verify.failures')}>
          {result.anchor_failures!.map((failure, index) => (
            <li
              key={`${failure.kind}-${failure.seq}-${failure.at_idx ?? 0}-${index}`}
              className="font-mono text-xs text-danger"
            >
              {t('verify.failure', {
                kind: failure.kind,
                seq: failure.seq,
                idx: failure.at_idx ?? '—',
                reason: failure.reason,
              })}
            </li>
          ))}
        </ol>
      )}
    </section>
  )
}
