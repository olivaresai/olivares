// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { useTranslation } from 'react-i18next'
import { KvList, KvRow } from '@/components/ui/kv'
import { QueryErrorState } from '@/components/layout/query-error-state'
import {
  AssuranceStatusSection,
  PasskeysManagementSection,
} from '@/features/identity/privileged-login'
import { StepUpPolicySetting } from '@/features/identity/step-up-policy'
import { TOTPTab } from '@/features/identity/totp'
import { useAuth } from '@/lib/auth/context'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import '@/features/identity/i18n'

export function SignInSettings() {
  const { t } = useTranslation(['settings', 'identity', 'common'])
  const { principal, can } = useAuth()
  const info = useServerInfo()
  const admin = can('system:admin')
  const sso = useQuery({
    queryKey: consoleKeys.sso(),
    queryFn: () => consoleApi.getSSO(),
    enabled: admin,
  })
  const lifetime = principal?.session_ttl_seconds
  return (
    <div className="flex flex-col gap-6">
      <section aria-label={t('settings:signIn.methods')}>
        <h2 className="mb-3 text-body font-semibold">
          {t('settings:signIn.methods')}
        </h2>
        <p className="text-body">{t('settings:signIn.password')}</p>
        {admin ? (
          sso.isError ? (
            <QueryErrorState
              error={sso.error}
              retry={() => void sso.refetch()}
            />
          ) : sso.data ? (
            <p className="text-caption text-text-2">
              {t(
                sso.data.require_sso
                  ? sso.data.status !== 'active' || !sso.data.protocol
                    ? 'settings:signIn.ssoInactive'
                    : sso.data.enforced_by === 'enterprise'
                      ? 'settings:signIn.passwordBlocked'
                      : 'settings:signIn.ssoNotEnforced'
                  : 'settings:signIn.passwordAvailable',
              )}
            </p>
          ) : (
            <p role="status">{t('common:states.loading')}</p>
          )
        ) : (
          <p className="text-caption text-text-2">
            {t('settings:signIn.passwordManaged')}
          </p>
        )}
        {info.isError ? (
          <QueryErrorState
            error={info.error}
            retry={() => void info.refetch()}
          />
        ) : info.data ? (
          <p className="mt-2 text-body">
            {info.data.sso_providers?.length
              ? t('settings:signIn.sso', {
                  providers: info.data.sso_providers
                    .map((p) => p.label)
                    .join(', '),
                })
              : t('settings:signIn.noSso')}
          </p>
        ) : (
          <p role="status">{t('common:states.loading')}</p>
        )}
        {can('system:admin') ? (
          <a
            className="text-body text-primary underline"
            href="/identity?tab=sso"
          >
            {t('settings:signIn.manageSso')}
          </a>
        ) : null}
      </section>
      <section aria-label={t('settings:signIn.session')}>
        <KvList>
          <KvRow label={t('settings:signIn.session')}>
            {typeof lifetime === 'number' &&
            Number.isFinite(lifetime) &&
            lifetime > 0
              ? t('settings:signIn.hours', { count: lifetime / 3600 })
              : t('settings:edition.unknown')}
          </KvRow>
        </KvList>
        <p className="text-caption text-text-2">
          {t('settings:signIn.sessionHint')}
        </p>
      </section>
      {can('system:admin') ? (
        <StepUpPolicySetting />
      ) : (
        <KvList>
          <KvRow label={t('identity:stepUpPolicy.title')}>
            {principal?.admin_step_up
              ? t(`identity:stepUpPolicy.options.${principal.admin_step_up}`)
              : t('settings:edition.unknown')}
          </KvRow>
        </KvList>
      )}
      <AssuranceStatusSection />
      <PasskeysManagementSection />
      <TOTPTab />
    </div>
  )
}
