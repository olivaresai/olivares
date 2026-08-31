// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import {
  RefreshCw,
  ShieldAlert,
  ShieldCheck,
  ShieldQuestion,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { KvList, KvRow } from '@/components/ui/kv'
import { Skeleton } from '@/components/ui/skeleton'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import { catalogApi, catalogKeys } from './api'
import './i18n'
import type { EntryDTO, VerifyDTO } from './types'

/**
 * EntryVerifyPanel renders the HONEST verification posture of an entry. It recomputes
 * the hash + checks the signature via GET /entries/{id}/verify (read-only, not
 * audited) and shows the catalog signing posture from GET /pubkey. The rule is
 * absolute: the `verified` flag and `reason` string are server-computed truth, so:
 *   - verified && signed  → a calm "signature verified" state (NOT an authoritative
 *     green security claim, just the honest signal);
 *   - verified && !signed → "hash-pinned, unsigned" (amber, plainly stated);
 *   - !verified           → "not verified" (treated plainly, never as safe/approved);
 *   - not approved yet    → "nothing pinned yet".
 */
export function EntryVerifyPanel({ entry }: { entry: EntryDTO }) {
  const { t } = useTranslation('catalog')
  const { activeTenant } = useAuth()
  const id = entry.id ?? ''
  const pinned = entry.status === 'approved' || entry.status === 'deprecated'

  const verify = useQuery({
    queryKey: catalogKeys.verify(activeTenant, id),
    queryFn: () => catalogApi.verifyEntry(id),
    enabled: !!id,
  })
  const pubkey = useQuery({
    queryKey: catalogKeys.pubkey(activeTenant),
    queryFn: () => catalogApi.pubkey(),
    // /pubkey is effectively constant per node — fetch once and keep it.
    staleTime: Infinity,
  })

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium text-foreground">
          {t('verify.title')}
        </h3>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => verify.refetch()}
          disabled={verify.isFetching || !id}
        >
          <RefreshCw />
          {t('verify.reverify')}
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">{t('verify.caption')}</p>

      {verify.isLoading ? (
        <Skeleton className="h-20 w-full" />
      ) : verify.data ? (
        <VerifyState data={verify.data} pinned={pinned} />
      ) : null}

      {/* Catalog-wide signing posture from /pubkey. */}
      <div className="mt-1 rounded-md border border-border bg-muted/40 p-3">
        <p className="mb-1.5 text-xs font-medium text-foreground">
          {t('posture.title')}
        </p>
        {pubkey.isLoading ? (
          <Skeleton className="h-12 w-full" />
        ) : pubkey.data ? (
          pubkey.data.signing_enabled ? (
            <KvList>
              <KvRow label={t('posture.fingerprint')} mono>
                {pubkey.data.fingerprint ?? '—'}
              </KvRow>
              <KvRow label={t('posture.algorithm')}>
                {pubkey.data.algorithm ?? '—'}
              </KvRow>
              {pubkey.data.public_key && (
                <KvRow label={t('posture.publicKey')} align="start" mono>
                  <span
                    className="block break-all"
                    title={t('posture.publicKeyHint')}
                  >
                    {pubkey.data.public_key}
                  </span>
                </KvRow>
              )}
            </KvList>
          ) : (
            <p className="text-xs text-muted-foreground">
              {pubkey.data.note ?? t('posture.disabledNote')}
            </p>
          )
        ) : null}
      </div>
    </section>
  )
}

type VerifyTone = 'success' | 'warning' | 'danger' | 'neutral'

/** Resolve the honest posture. Never present an unverified entry as safe. */
function resolvePosture(
  data: VerifyDTO,
  pinned: boolean,
): {
  labelKey: string
  hintKey: string
  Icon: typeof ShieldQuestion
  tone: VerifyTone
} {
  if (!pinned) {
    return {
      labelKey: 'verify.pending',
      hintKey: 'verify.pendingHint',
      Icon: ShieldQuestion,
      tone: 'neutral',
    }
  }
  if (!data.verified) {
    return {
      labelKey: 'verify.notVerified',
      hintKey: 'verify.notVerifiedHint',
      Icon: ShieldAlert,
      tone: 'danger',
    }
  }
  if (data.signed) {
    return {
      labelKey: 'verify.verified',
      hintKey: 'verify.verifiedHint',
      Icon: ShieldCheck,
      tone: 'success',
    }
  }
  return {
    labelKey: 'verify.verifiedHashOnly',
    hintKey: 'verify.verifiedHashOnlyHint',
    Icon: ShieldQuestion,
    tone: 'warning',
  }
}

function VerifyState({ data, pinned }: { data: VerifyDTO; pinned: boolean }) {
  const { t } = useTranslation('catalog')

  const { labelKey, hintKey, Icon, tone } = resolvePosture(data, pinned)
  const label = t(labelKey)
  const hint = t(hintKey)

  const toneClass = {
    success: 'border-success-line bg-success-soft text-success',
    warning: 'border-warning-line bg-warning-soft text-warning',
    danger: 'border-danger-line bg-danger-soft text-danger',
    neutral: 'border-border bg-muted text-muted-foreground',
  }[tone]

  return (
    <div className="flex flex-col gap-3">
      <div
        role="status"
        className={cn(
          'flex items-start gap-2 rounded-md border px-3 py-2 text-sm',
          toneClass,
        )}
      >
        <Icon className="mt-0.5 size-4 shrink-0" aria-hidden />
        <div className="flex flex-col gap-0.5">
          <span className="font-medium">{label}</span>
          <span className="text-xs opacity-90">{hint}</span>
        </div>
      </div>

      <KvList>
        {data.content_hash && (
          <KvRow label={t('verify.contentHash')} align="start" mono>
            <span className="block break-all">{data.content_hash}</span>
          </KvRow>
        )}
        {data.recomputed_hash && (
          <KvRow label={t('verify.recomputedHash')} align="start" mono>
            <span className="block break-all">{data.recomputed_hash}</span>
          </KvRow>
        )}
        {data.signed && data.signed_by && (
          <KvRow label={t('verify.signedBy')} mono>
            {data.signed_by}
          </KvRow>
        )}
        {/* The engine's human reason string, verbatim. */}
        <KvRow label={t('verify.reason')} align="start">
          {data.reason}
        </KvRow>
      </KvList>
    </div>
  )
}
