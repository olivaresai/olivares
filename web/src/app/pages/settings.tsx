// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Card } from '@/components/ui/card'
import { Field } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '@/lib/auth/context'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { SUPPORTED_LANGUAGES, setLanguage, type LanguageCode } from '@/lib/i18n'
import { usePreferencesStore, type Density } from '@/stores/preferences'
import { useThemeStore, type Theme } from '@/stores/theme'

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 py-2.5">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className="min-w-0 truncate text-right text-sm text-foreground">
        {children}
      </dd>
    </div>
  )
}

export function SettingsPage() {
  const { t, i18n } = useTranslation(['settings', 'common', 'auth'])
  const { principal, isSuperadmin, activeRole, grants } = useAuth()
  const serverInfo = useServerInfo()

  const theme = useThemeStore((s) => s.theme)
  const setTheme = useThemeStore((s) => s.setTheme)
  const density = usePreferencesStore((s) => s.density)
  const setDensity = usePreferencesStore((s) => s.setDensity)
  // Resolve the active language to one of the supported codes. i18next's
  // resolvedLanguage already maps region variants (en-US→en, zh-CN→zh) and
  // applies the fallback, so the switcher reflects the real UI language for all
  // six locales — not just en/es.
  const lang: LanguageCode =
    (SUPPORTED_LANGUAGES.find((l) => l.code === i18n.resolvedLanguage)?.code ??
      'en') as LanguageCode

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="font-display text-2xl font-semibold tracking-tight text-foreground">
          {t('settings:title')}
        </h1>
        <p className="text-sm text-muted-foreground">
          {t('settings:subtitle')}
        </p>
      </div>

      <Tabs defaultValue="profile">
        <TabsList>
          <TabsTrigger value="profile">
            {t('settings:tabs.profile')}
          </TabsTrigger>
          <TabsTrigger value="appearance">
            {t('settings:tabs.appearance')}
          </TabsTrigger>
          <TabsTrigger value="about">{t('settings:tabs.about')}</TabsTrigger>
        </TabsList>

        <TabsContent value="profile">
          <Card className="max-w-2xl p-5">
            <dl className="divide-y divide-border">
              {principal?.display_name && (
                <Row label={t('settings:profile.displayName')}>
                  {principal.display_name}
                </Row>
              )}
              <Row label={t('settings:profile.actor')}>
                <span className="font-mono text-xs">{principal?.actor}</span>
              </Row>
              <Row label={t('settings:profile.role')}>
                {isSuperadmin ? (
                  <Badge variant="accent">{t('auth:roles.superadmin')}</Badge>
                ) : activeRole ? (
                  <Badge variant="neutral">
                    {t(`auth:roles.${activeRole}`, {
                      defaultValue: activeRole,
                    })}
                  </Badge>
                ) : (
                  '—'
                )}
              </Row>
              <Row label={t('settings:profile.memberships')}>
                <span className="font-mono tabular-nums">{grants.length}</span>
              </Row>
            </dl>
          </Card>
        </TabsContent>

        <TabsContent value="appearance">
          <Card className="flex max-w-2xl flex-col gap-5 p-5">
            <Field
              label={t('settings:appearance.theme')}
              htmlFor="theme"
              description={t('settings:appearance.themeHint')}
            >
              <Select value={theme} onValueChange={(v) => setTheme(v as Theme)}>
                <SelectTrigger id="theme" className="w-56">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="light">
                    {t('common:theme.light')}
                  </SelectItem>
                  <SelectItem value="dark">{t('common:theme.dark')}</SelectItem>
                  <SelectItem value="system">
                    {t('common:theme.system')}
                  </SelectItem>
                </SelectContent>
              </Select>
            </Field>

            <Field
              label={t('settings:appearance.density')}
              htmlFor="density"
              description={t('settings:appearance.densityHint')}
            >
              <Select
                value={density}
                onValueChange={(v) => setDensity(v as Density)}
              >
                <SelectTrigger id="density" className="w-56">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="comfortable">
                    {t('common:density.comfortable')}
                  </SelectItem>
                  <SelectItem value="compact">
                    {t('common:density.compact')}
                  </SelectItem>
                </SelectContent>
              </Select>
            </Field>

            <Field
              label={t('settings:appearance.language')}
              htmlFor="language"
              description={t('settings:appearance.languageHint')}
            >
              <Select
                value={lang}
                onValueChange={(v) => setLanguage(v as LanguageCode)}
              >
                <SelectTrigger id="language" className="w-56">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {SUPPORTED_LANGUAGES.map((l) => (
                    <SelectItem key={l.code} value={l.code}>
                      {l.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </Card>
        </TabsContent>

        <TabsContent value="about">
          <Card className="max-w-2xl p-5">
            <dl className="divide-y divide-border">
              <Row label={t('settings:about.version')}>
                <span className="font-mono text-xs">
                  {serverInfo.data?.version ?? '—'}
                </span>
              </Row>
              <Row label={t('settings:about.engine')}>
                <span className="font-mono text-xs">
                  {serverInfo.data?.engine ?? '—'}
                </span>
              </Row>
              <Row label={t('settings:about.license')}>
                {serverInfo.data?.license.status ?? '—'}
              </Row>
              <Row label={t('settings:about.licensee')}>
                {serverInfo.data?.license.licensee || t('settings:about.none')}
              </Row>
            </dl>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  )
}
