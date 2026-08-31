// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The printed-report chrome for the leadership PDF. Export is CLIENT-SIDE by design
// (decision documented in ESTADO): there is no server-side reporting endpoint for the
// cross-module rollup, and printing what is on screen is faithful to RBAC/tenant BY
// CONSTRUCTION — the report can only ever contain the sections the role actually
// rendered (docs/SECURITY-HARDENING.md,§4). `window.print()` → the browser's Save-as-PDF; no heavy
// client dependency to ship inside the go:embed binary.
//
// These bits are hidden on screen and only laid out under @media print (the header is
// the report cover; the footer is the standing disclaimer). The rest of the dashboard
// is shared verbatim — charts are SVG and print cleanly.
import { useTranslation } from 'react-i18next'
import { Wordmark } from '@/components/layout/brand'
import { formatDateTime } from '@/lib/format'

/** The print-only report cover header: brand, title, organization, range, timestamp. */
export function ReportHeader({
  tenantLabel,
  rangeLabel,
}: {
  tenantLabel: string
  rangeLabel: string
}) {
  const { t, i18n } = useTranslation('executive')
  const now = formatDateTime(new Date().toISOString(), i18n.language)
  return (
    <div className="mb-6 hidden border-b border-border pb-4 print:block">
      <div className="flex items-center justify-between">
        <Wordmark />
        <span className="font-display text-sm font-semibold text-foreground">
          {t('report.title')}
        </span>
      </div>
      <dl className="mt-3 grid grid-cols-3 gap-2 text-xs text-muted-foreground">
        <div>
          <dt className="uppercase tracking-wide">{t('report.tenant')}</dt>
          <dd className="font-medium text-foreground">{tenantLabel}</dd>
        </div>
        <div>
          <dt className="uppercase tracking-wide">{t('report.range')}</dt>
          <dd className="font-medium text-foreground">{rangeLabel}</dd>
        </div>
        <div>
          <dt className="uppercase tracking-wide">
            {t('report.generatedLabel')}
          </dt>
          <dd className="font-medium text-foreground">{now}</dd>
        </div>
      </dl>
    </div>
  )
}

/** The print-only standing disclaimer footer. */
export function ReportFooter() {
  const { t } = useTranslation('executive')
  return (
    <p className="mt-6 hidden border-t border-border pt-3 text-xs text-muted-foreground print:block">
      {t('report.footer')}
    </p>
  )
}
