// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Authority citations for the identity & NHI console. Every standard the
// panel reflects is cited verbatim to its primary source. The panel
// REFLECTS these standards; it does NOT claim conformance the backend does not
// guarantee (AAL3/PIV/FIPS are target standards, not certifications —).
import { useTranslation } from 'react-i18next'
import { SectionCard } from '@/features/_intel'
import { KvList, KvRow } from '@/components/ui/kv'
import { AuthorityLink } from './components'

/** id → authority URL, verbatim from/7.6/7.9/7.10.*/
export const AUTHORITY = {
  scim: 'https://datatracker.ietf.org/doc/html/rfc7644',
  prm: 'https://datatracker.ietf.org/doc/html/rfc9728',
  asMetadata: 'https://datatracker.ietf.org/doc/html/rfc8414',
  resourceIndicators: 'https://datatracker.ietf.org/doc/html/rfc8707',
  pkce: 'https://datatracker.ietf.org/doc/html/rfc7636',
  oauthSecurityBcp: 'https://datatracker.ietf.org/doc/html/rfc9700',
  webauthn: 'https://pages.nist.gov/800-63-4',
  piv: 'https://csrc.nist.gov/pubs/fips/201-3',
  certManager: 'https://cert-manager.io',
  pqc: 'https://csrc.nist.gov/pubs/sp/800-57/part-1/r6/ipd',
  wifReference:
    'https://platform.claude.com/docs/en/manage-claude/wif-reference',
  workloadIdentity:
    'https://platform.claude.com/docs/en/manage-claude/workload-identity-federation',
  externalKeys: 'https://platform.claude.com/docs/en/api/admin/external_keys',
  workspaceUpdate:
    'https://platform.claude.com/docs/en/admin-api/workspaces/update-workspace',
} as const

/** A panel of standards relevant to a given area; `keys` selects which to show. */
export function AuthorityReferences({
  area,
  keys,
}: {
  /** i18n title key fragment. */
  area: string
  keys: (keyof typeof AUTHORITY)[]
}) {
  const { t } = useTranslation('identity')
  return (
    <SectionCard
      title={t(`references.${area}.title`)}
      description={t('references.note')}
    >
      <KvList>
        {keys.map((k) => (
          <KvRow key={k} label={t(`references.labels.${k}`)} mono align="start">
            <AuthorityLink href={AUTHORITY[k]}>{AUTHORITY[k]}</AuthorityLink>
          </KvRow>
        ))}
      </KvList>
    </SectionCard>
  )
}
