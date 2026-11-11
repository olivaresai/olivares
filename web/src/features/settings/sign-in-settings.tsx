// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { AppLink } from '@/components/ui/app-link'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { useTranslation } from 'react-i18next'
import { BoxedList } from '@/components/ui/boxed-list'
import { StatusChip } from '@/components/ui/status-chip'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { PanelSlot } from '@/components/ui/trust-chrome'
import { PANEL_EXTENSIONS } from '@/features/extensions'
import { useOfferedPanels } from '@/features/panels'
import { AuthSessionsSection } from '@/features/identity/auth-sessions'
import {
  AssuranceStatusSection,
  PasskeysManagementSection,
} from '@/features/identity/privileged-login'
import { StepUpPolicySetting } from '@/features/identity/step-up-policy'
import { TOTPTab } from '@/features/identity/totp'
import { useAuth } from '@/lib/auth/context'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { ConsoleExposure } from './console-exposure'
import { SettingsRow } from './settings-row'
import '@/features/identity/i18n'

export function SignInSettings() {
  const { t } = useTranslation(['settings', 'identity', 'common'])
  const { principal, can } = useAuth()
  const info = useServerInfo()
  const admin = can('system:admin')
  // The edition's sign-in cards, as the identity page's login tab showed them.
  const cards = useOfferedPanels(PANEL_EXTENSIONS.identityLoginCards ?? [])
  const sso = useQuery({
    queryKey: consoleKeys.sso(),
    queryFn: () => consoleApi.getSSO(),
    enabled: admin,
  })
  const lifetime = principal?.session_ttl_seconds
  const names = (info.data?.sso_providers ?? []).map((p) => p.label).join(', ')
  // The SSO policy's word on a local password; only administrators read it,
  // and only a good read says it (a failed refetch keeps the old data).
  const policy = sso.isSuccess
    ? sso.data.require_sso
      ? sso.data.status !== 'active' || !sso.data.protocol
        ? 'settings:signIn.ssoInactive'
        : sso.data.enforced_by === 'enterprise'
          ? 'settings:signIn.passwordBlocked'
          : 'settings:signIn.ssoNotEnforced'
      : 'settings:signIn.passwordAvailable'
    : null
  const required = policy === 'settings:signIn.passwordBlocked'
  const ssoState = required
    ? 'settings:signIn.required'
    : names
      ? 'settings:signIn.available'
      : 'settings:signIn.notSetUp'
  const password = admin ? policy : 'settings:signIn.passwordManaged'
  const stepUp = principal?.admin_step_up
    ? t(`identity:stepUpPolicy.options.${principal.admin_step_up}`)
    : t('settings:edition.unknown')
  return (
    <div className="flex flex-col gap-6">
      <section
        aria-labelledby="sign-in-methods"
        className="flex flex-col gap-2"
      >
        <h2
          id="sign-in-methods"
          className="text-body-l font-semibold text-text"
        >
          {t('settings:signIn.methods')}
        </h2>
        <BoxedList>
          <SettingsRow
            name={
              names
                ? t('settings:signIn.sso', { providers: names })
                : t('settings:signIn.ssoName')
            }
            description={info.data && !names ? t('settings:signIn.noSso') : ''}
            control={
              info.data && (!admin || sso.isSuccess) ? (
                <StatusChip tone={names || required ? 'ok' : 'neutral'}>
                  {t(ssoState)}
                </StatusChip>
              ) : null
            }
          />
          <SettingsRow
            name={t('settings:signIn.password')}
            description={password ? t(password) : ''}
            control={
              policy ? (
                <StatusChip tone={required ? 'neutral' : 'ok'}>
                  {t(
                    required
                      ? 'settings:signIn.blocked'
                      : 'settings:signIn.available',
                  )}
                </StatusChip>
              ) : null
            }
          />
        </BoxedList>
        {admin && sso.isError ? (
          <QueryErrorState error={sso.error} retry={() => void sso.refetch()} />
        ) : null}
        {info.isError ? (
          <QueryErrorState
            error={info.error}
            retry={() => void info.refetch()}
          />
        ) : null}
        {(admin && sso.isPending) || (!info.data && !info.isError) ? (
          <p role="status">{t('common:states.loading')}</p>
        ) : null}
        {can('system:admin') ? (
          <AppLink
            className="text-body text-primary underline"
            href="/identity?tab=sso"
          >
            {t('settings:signIn.manageSso')}
          </AppLink>
        ) : null}
      </section>
      <section
        aria-labelledby="sign-in-session"
        className="flex flex-col gap-2"
      >
        <h2
          id="sign-in-session"
          className="text-body-l font-semibold text-text"
        >
          {t('settings:signIn.sessionGroup')}
        </h2>
        <BoxedList>
          <SettingsRow
            name={t('settings:signIn.session')}
            description={t('settings:signIn.sessionHint')}
            control={
              <span className="text-body text-text-2">
                {typeof lifetime === 'number' &&
                Number.isFinite(lifetime) &&
                lifetime > 0
                  ? t('settings:signIn.hours', { count: lifetime / 3600 })
                  : t('settings:edition.unknown')}
              </span>
            }
          />
        </BoxedList>
      </section>
      {can('system:admin') ? (
        <StepUpPolicySetting />
      ) : (
        <BoxedList>
          <SettingsRow
            name={t('identity:stepUpPolicy.title')}
            description=""
            control={<span className="text-body text-text-2">{stepUp}</span>}
          />
        </BoxedList>
      )}
      {admin ? <ConsoleExposure /> : null}
      <AssuranceStatusSection />
      <AuthSessionsSection />
      <PasskeysManagementSection />
      {cards.map(({ id, Component }) => (
        <PanelSlot key={id}>
          <Component />
        </PanelSlot>
      ))}
      <TOTPTab />
    </div>
  )
}
