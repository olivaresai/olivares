// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADM-IDN-02 (part 2) — auth-MCP console (consumes). Surfaces the REAL mcp_auth
// Findings (the `token-binding-verified` dimension and the "OAuth-protected, not
// introspected" finding) and documents the audience-bound PRM (RFC 9728) model:
// resource indicator → concrete server, PRM → AS discovery (RFC 8414) → PKCE S256
// (RFC 7636) + resource-bound token (RFC 8707). DEFENSIVE: the panel documents the
// posture; it NEVER exposes token passthrough (prohibited by design).
import { useQuery } from '@tanstack/react-query'
import { ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { SectionCard, SelfAuditNotice } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { identityApi, identityKeys } from './api'
import { DeclaredSection, FindingList } from './components'
import { AuthorityReferences } from './references'

export function McpAuthTab() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant } = useAuth()
  const findings = useQuery({
    queryKey: identityKeys.findings(activeTenant, 'mcp_auth'),
    queryFn: () => identityApi.findings({ kind: 'mcp_auth' }),
    retry: false,
  })

  // The RFC 9728 audience-bound flow, documented step-by-step (defensive copy).
  const flowSteps = [
    'prm',
    'asDiscovery',
    'pkce',
    'resourceBound',
    'introspect',
  ] as const

  return (
    <div className="flex flex-col gap-6">
      <SectionCard title={t('mcp.title')} description={t('mcp.description')}>
        <div className="flex flex-col gap-4">
          <SelfAuditNotice />
          <DeclaredSection
            query={findings}
            what={t('mcp.seamWhat')}
            skeletonHeight={80}
          >
            {(data) => (
              <FindingList
                findings={data.items}
                label={t('mcp.title')}
                emptyTitle={t('mcp.noFindings')}
                emptyDescription={t('mcp.noFindingsHint')}
              />
            )}
          </DeclaredSection>
        </div>
      </SectionCard>

      <SectionCard
        title={t('mcp.prmTitle')}
        description={t('mcp.prmDescription')}
      >
        <ol className="flex flex-col gap-2">
          {flowSteps.map((step, i) => (
            <li
              key={step}
              className="flex items-start gap-3 rounded-md border border-border bg-surface px-3 py-2"
            >
              <span
                className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-medium text-muted-foreground"
                aria-hidden
              >
                {i + 1}
              </span>
              <div>
                <p className="text-sm font-medium text-foreground">
                  {t(`mcp.flow.${step}.title`)}
                </p>
                <p className="text-xs text-muted-foreground">
                  {t(`mcp.flow.${step}.body`)}
                </p>
              </div>
            </li>
          ))}
        </ol>
        <div className="mt-3 flex items-start gap-2 rounded-md border border-success/30 bg-success/5 px-3 py-2">
          <ShieldCheck
            className="mt-0.5 size-4 shrink-0 text-success"
            aria-hidden
          />
          <p className="text-xs text-foreground">{t('mcp.noPassthrough')}</p>
        </div>
      </SectionCard>

      <AuthorityReferences
        area="mcpAuth"
        keys={['prm', 'asMetadata', 'pkce', 'resourceIndicators']}
      />
    </div>
  )
}
