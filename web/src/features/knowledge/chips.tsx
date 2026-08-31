// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Domain chips for the knowledge module. These render AUTHORITATIVE governance
// metadata (classification / residency / embed policy / egress) and minimum-data
// references (ACL handles, hashes). None of these is a secret VALUE:
//   - AclRefs renders permission references as mono ref chips (Badge), never values.
//   - HashChip renders an opaque hash (content/query/template/provider), never the
//     underlying text — it is truncated and carries a "one-way hash" title.
//   - EmbedModelBadge highlights 'local-hash' as LEXICAL (not semantic) — an
//     authoritative degraded state, not an untrusted hint.
import { KeyRound, Lock } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { LOCAL_HASH_MODEL } from './types'
import './i18n'

const CLASSIFICATION_VARIANT: Record<string, BadgeVariant> = {
  public: 'neutral',
  internal: 'info',
  confidential: 'warning',
  secret: 'danger',
}

export function ClassificationBadge({ value }: { value: string }) {
  const { t } = useTranslation('knowledge')
  const key = (value ?? '').toLowerCase()
  return (
    <Badge variant={CLASSIFICATION_VARIANT[key] ?? 'neutral'}>
      {t(`common.classifications.${key}`, { defaultValue: value })}
    </Badge>
  )
}

export function ResidencyBadge({ value }: { value: string }) {
  const { t } = useTranslation('knowledge')
  const key = (value ?? '').toLowerCase()
  return (
    <Badge variant="outline">
      {t(`common.residencies.${key}`, { defaultValue: value })}
    </Badge>
  )
}

export function EmbedPolicyBadge({ value }: { value: string }) {
  const { t } = useTranslation('knowledge')
  const key = (value ?? '').toLowerCase()
  const isLocalOnly = key === 'local_only'
  return (
    <Badge
      variant={isLocalOnly ? 'success' : 'neutral'}
      title={isLocalOnly ? t('common.localOnlyHint') : undefined}
    >
      {isLocalOnly && <Lock className="size-3 shrink-0" aria-hidden />}
      {t(`common.embedPolicies.${key}`, { defaultValue: value })}
    </Badge>
  )
}

/** The wired embedder ref. 'local-hash' is flagged LEXICAL (not semantic). */
export function EmbedModelBadge({ value }: { value: string }) {
  const { t } = useTranslation('knowledge')
  if (value === LOCAL_HASH_MODEL) {
    return (
      <Badge variant="warning" title={t('common.localHashHint')}>
        <span className="font-mono">{value}</span>
        <span>·</span>
        <span>{t('common.localHashLabel')}</span>
      </Badge>
    )
  }
  return (
    <span className="font-mono text-xs text-muted-foreground">
      {value || '—'}
    </span>
  )
}

/** Egress state. false = data stayed in the perimeter (a sales argument). */
export function EgressBadge({ value }: { value: boolean }) {
  const { t } = useTranslation('knowledge')
  return value ? (
    <Badge variant="warning" title={t('common.egressYesHint')}>
      {t('common.egressYes')}
    </Badge>
  ) : (
    <Badge variant="success" title={t('common.egressNoHint')}>
      {t('common.egressNo')}
    </Badge>
  )
}

/**
 * An opaque one-way hash (content/query/template/provider). Truncated, mono, with a
 * "the underlying text is never shown" title. NEVER expand it to plaintext.
 */
export function HashChip({
  value,
  title,
  className,
}: {
  value?: string
  title?: string
  className?: string
}) {
  const { t } = useTranslation('knowledge')
  if (!value) {
    return <span className="text-xs text-muted-foreground">—</span>
  }
  return (
    <span
      data-slot="hash-chip"
      title={title ?? t('common.hashHint')}
      className={cn(
        'inline-flex items-center rounded-sm border border-border bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground',
        className,
      )}
    >
      {value.length > 16 ? `${value.slice(0, 16)}…` : value}
    </span>
  )
}

/**
 * Permission references (ACL handles) — group/role/principal handles, NEVER
 * credentials. Rendered as mono ref chips. SecretRef is reserved for credential
 * references; ACLs are permission references, so a plain mono badge is correct.
 */
export function AclRefs({
  acl,
  className,
}: {
  acl?: string[] | null
  className?: string
}) {
  const { t } = useTranslation('knowledge')
  if (!acl || acl.length === 0) {
    return (
      <span className="text-xs text-muted-foreground">
        {t('common.aclNone')}
      </span>
    )
  }
  return (
    <div className={cn('flex flex-wrap gap-1', className)}>
      {acl.map((ref) => (
        <span
          key={ref}
          data-slot="acl-ref"
          title={t('common.aclHint')}
          className="inline-flex items-center gap-1 rounded-sm border border-border bg-muted px-1.5 py-0.5 font-mono text-xs"
        >
          <KeyRound className="size-3 shrink-0 text-accent-text" aria-hidden />
          <span className="text-foreground">{ref}</span>
        </span>
      ))}
    </div>
  )
}
