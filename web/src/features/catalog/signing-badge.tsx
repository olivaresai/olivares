// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ShieldCheck, ShieldOff } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import './i18n'
import type { EntryDTO } from './types'

/**
 * SigningBadge reflects an entry's signing posture HONESTLY from the entry itself:
 * `signed=true` → a calm "Signed" chip with the signer fingerprint in the tooltip;
 * `signed=false` on an approved/deprecated entry → an "Unsigned" chip explaining it
 * is hash-pinned and ledger-attested but not signed. For entries that are not yet
 * approved (nothing pinned), it renders a quiet dash. This is a posture signal, NOT
 * an authoritative security claim — the full verification widget is the source of
 * truth (see EntryVerifyPanel).
 */
export function SigningBadge({ entry }: { entry: EntryDTO }) {
  const { t } = useTranslation('catalog')
  const pinned = entry.status === 'approved' || entry.status === 'deprecated'

  if (!pinned) {
    return (
      <span className="text-muted-foreground">{t('signing.notPinned')}</span>
    )
  }
  if (entry.signed) {
    return (
      <Badge
        variant="success"
        title={t('signing.signedHint', {
          fingerprint: entry.signed_by ?? '—',
        })}
      >
        <ShieldCheck className="size-3 shrink-0" aria-hidden />
        {t('signing.signed')}
      </Badge>
    )
  }
  return (
    <Badge variant="warning" title={t('signing.unsignedHint')}>
      <ShieldOff className="size-3 shrink-0" aria-hidden />
      {t('signing.unsigned')}
    </Badge>
  )
}
