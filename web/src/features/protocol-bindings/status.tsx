// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { AlertTriangle, CheckCircle2, HelpCircle, XCircle } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import type {
  AssessmentVerdict,
  ProtocolBindingSpecState,
  ProtocolObservationVerdict,
} from './types'

const SPEC_STATE_VARIANT: Record<ProtocolBindingSpecState, BadgeVariant> = {
  draft: 'neutral',
  active: 'success',
  disabled: 'warning',
  superseded: 'outline',
}

export function SpecStateBadge({ state }: { state: ProtocolBindingSpecState }) {
  const { t } = useTranslation('protocolBindings')
  return (
    <Badge variant={SPEC_STATE_VARIANT[state]}>{t(`state.${state}`)}</Badge>
  )
}

type AnyVerdict = ProtocolObservationVerdict | AssessmentVerdict

function verdictVariant(verdict: AnyVerdict): BadgeVariant {
  if (verdict === 'CLEAN' || verdict === 'LIMPIO') return 'success'
  if (verdict === 'BROKEN' || verdict === 'ROTO') return 'danger'
  return 'warning'
}

export function ProtocolVerdictBadge({ verdict }: { verdict: AnyVerdict }) {
  const { t } = useTranslation('protocolBindings')
  const Icon =
    verdict === 'CLEAN' || verdict === 'LIMPIO'
      ? CheckCircle2
      : verdict === 'BROKEN' || verdict === 'ROTO'
        ? XCircle
        : HelpCircle
  return (
    <Badge variant={verdictVariant(verdict)}>
      <Icon className="size-3" aria-hidden="true" />
      {t(`verdict.${verdict}`)}
    </Badge>
  )
}

export function UnknownNotice({
  code,
  children,
}: {
  code?: string | null
  children?: React.ReactNode
}) {
  const { t } = useTranslation('protocolBindings')
  return (
    <div
      role="status"
      className="rounded-md border border-warning-line bg-warning-soft p-3 text-sm text-warning"
    >
      <div className="flex items-start gap-2">
        <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
        <div className="space-y-1">
          <p className="font-medium">{t('outcome.unknownTitle')}</p>
          <p className="text-xs">{children ?? t('outcome.unknownBody')}</p>
          {code ? <p className="font-mono text-xs">{code}</p> : null}
        </div>
      </div>
    </div>
  )
}

export function BrokenNotice({
  code,
  children,
}: {
  code?: string | null
  children?: React.ReactNode
}) {
  const { t } = useTranslation('protocolBindings')
  return (
    <div
      role="alert"
      className="rounded-md border border-danger-line bg-danger-soft p-3 text-sm text-danger"
    >
      <p className="font-medium">{t('outcome.brokenTitle')}</p>
      <p className="mt-1 text-xs">{children ?? t('outcome.brokenBody')}</p>
      {code ? <p className="mt-1 font-mono text-xs">{code}</p> : null}
    </div>
  )
}
