// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { EditionSwitch } from '@/features/settings/edition-switch'
import { useEditionSummary } from '@/features/settings/edition-summary'
import type { LicenseStatusDTO } from './api'

export function LicenseBuildGuidance({
  license,
}: {
  license: LicenseStatusDTO
}) {
  const { t } = useTranslation('console')
  const summary = useEditionSummary()
  // The credential's product profile is separate from the build edition and the
  // legacy support relationship. A flat license has no profile, only a free-form
  // plan: anything but "business" or "enterprise" is named generically rather than
  // guessed, and shows the Business list, which every commercial license includes.
  const profile = (license.support_profile || license.plan)
    ?.trim()
    .toLowerCase()
  const isEnterprise = profile === 'enterprise'
  const edition = isEnterprise
    ? 'Enterprise'
    : profile === 'business'
      ? 'Business'
      : 'Business/Enterprise'
  return (
    <section
      className="flex min-w-0 flex-col gap-2 text-body text-muted-foreground"
      aria-labelledby="license-build-guidance"
    >
      <h3 id="license-build-guidance" className="text-body text-foreground">
        {t('license.buildMismatch', { edition })}
      </h3>
      <EditionSwitch />
      <a
        className="w-fit rounded-sm text-accent-text underline underline-offset-4 outline-none focus-visible:ring-2 focus-visible:ring-ring"
        href="https://docs.olivares.ai/how-to/install-a-license/#3--community--business-in-place"
      >
        {t('license.switchDocs')}
      </a>
      <p>{t('license.editionAdds', { edition })}</p>
      <ul className="list-disc space-y-1 pl-5">
        {summary.business.map(({ text, lang }) => (
          <li key={text} lang={lang}>
            {text}
          </li>
        ))}
      </ul>
      {isEnterprise &&
        summary.enterprise.map(({ text, lang }) => (
          <p key={text} lang={lang}>
            {text}
          </p>
        ))}
    </section>
  )
}
