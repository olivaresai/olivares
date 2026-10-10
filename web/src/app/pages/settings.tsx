// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Fragment, useEffect, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { Folder, Play, Server, Sparkles } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Segmented } from '@/components/ui/segmented'
import { Switch } from '@/components/ui/switch'
import { useAuth } from '@/lib/auth/context'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import {
  setLanguage,
  SUPPORTED_LANGUAGES,
  type LanguageCode,
  currentLanguage,
} from '@/lib/i18n'
import { isTypingTarget } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { SignInSettings } from '@/features/settings/sign-in-settings'
import { ModulesSettings } from '@/features/settings/modules-settings'
import {
  SETTINGS_SECTIONS,
  settingsSections,
  type SettingsSectionId,
} from '@/features/settings/sections'
import { useUrlState } from '@/lib/hooks/use-url-state'
import { ReportSigningSettings } from '@/features/settings/report-signing-settings'
import { TracingSettings } from '@/features/settings/tracing-settings'
import { useViewAccess } from '@/features/navigation/authorization'
import { viewById } from '@/features/navigation/model'
import type { FeatureView } from '@/features/registry'
import {
  CLIENT_SETTING_DEFAULTS,
  localTimeZone,
  START_PAGE_IDS,
  useClientSettings,
  type ClockFormat,
  type StartPageId,
} from '@/features/settings/preferences'
import { usePreferencesStore, type Density } from '@/stores/preferences'
import { DEFAULT_THEME, useThemeStore, type Theme } from '@/stores/theme'

const THEMES = ['system', 'light', 'dark'] as const

/** Samples of the two frames. A child cannot escape `.dark` variable inheritance. */
const PREVIEW = {
  light: {
    frame: '#efeee9',
    canvas: '#ffffff',
    bar: '#dcdad5',
    accent: '#f08000',
  },
  dark: {
    frame: '#0a0a0c',
    canvas: '#111113',
    bar: '#2a2a2e',
    accent: '#f08000',
  },
} as const

type SectionId = SettingsSectionId | 'notifications' | 'sessionDefaults'

const SECTIONS: readonly SectionId[] = [
  ...SETTINGS_SECTIONS.map((s) => s.id),
  'notifications',
  'sessionDefaults',
]

function ThemePreview({ kind }: { kind: Theme }) {
  if (kind === 'system') {
    return (
      <span
        aria-hidden
        className="grid h-[86px] grid-cols-2 overflow-hidden rounded-[7px] border border-line"
      >
        <PreviewPane tone="light" />
        <PreviewPane tone="dark" />
      </span>
    )
  }
  return (
    <span
      aria-hidden
      className="block h-[86px] overflow-hidden rounded-[7px] border border-line"
    >
      <PreviewPane tone={kind} />
    </span>
  )
}

function PreviewPane({ tone }: { tone: 'light' | 'dark' }) {
  const paint = PREVIEW[tone]
  return (
    <span className="flex h-full" style={{ background: paint.frame }}>
      <span className="flex w-1/4 flex-col gap-1 p-2">
        <span
          className="h-1.5 w-full rounded-sm"
          style={{ background: paint.bar }}
        />
        <span
          className="h-1.5 w-2/3 rounded-sm"
          style={{ background: paint.bar }}
        />
      </span>
      <span
        className="flex flex-1 flex-col gap-1.5 p-2"
        style={{ background: paint.canvas }}
      >
        <span
          className="h-1.5 w-2/5 rounded-sm"
          style={{ background: paint.accent }}
        />
        <span
          className="h-1.5 w-4/5 rounded-sm"
          style={{ background: paint.bar }}
        />
        <span
          className="h-1.5 w-3/5 rounded-sm"
          style={{ background: paint.bar }}
        />
      </span>
    </span>
  )
}

function ChangedReset({
  show,
  onReset,
}: {
  show: boolean
  onReset: () => void
}) {
  const { t } = useTranslation('settings')
  if (!show) return null
  return (
    <button
      type="button"
      onClick={onReset}
      className="inline-flex items-center gap-1 text-caption font-medium text-accent-text"
    >
      {t('changedReset')}
    </button>
  )
}

function Row({
  name,
  description,
  changed,
  onReset,
  control,
}: {
  name: string
  description: string
  changed: boolean
  onReset: () => void
  control: ReactNode
}) {
  return (
    <div className="grid grid-cols-1 items-center gap-3 border-t border-line py-3.5 sm:grid-cols-[minmax(0,1fr)_auto]">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2 text-body font-medium text-text">
          <span>{name}</span>
          <ChangedReset show={changed} onReset={onReset} />
        </div>
        <p className="text-caption text-text-2">{description}</p>
      </div>
      <div className="justify-self-start sm:justify-self-end">{control}</div>
    </div>
  )
}

function RestoreAll() {
  const { t } = useTranslation('settings')
  const setTheme = useThemeStore((s) => s.setTheme)
  const setDensity = usePreferencesStore((s) => s.setDensity)
  const setReduceMotion = usePreferencesStore((s) => s.setReduceMotion)
  const restoreClient = useClientSettings((s) => s.restore)
  const onClick = () => {
    setTheme(DEFAULT_THEME)
    setDensity('comfortable')
    setReduceMotion(false)
    setLanguage('en')
    restoreClient()
  }
  const button = (
    <Button type="button" variant="ghost" size="sm" onClick={onClick}>
      {t('restoreAll')}
    </Button>
  )
  // The shell top bar is already in the document when this page renders. Tests
  // that render the page alone have no slot, so the button stays in the header.
  const host = document.querySelector(
    'header[data-slot="topbar"] [data-slot="page-actions"]',
  )
  if (host) return createPortal(button, host)
  return button
}

export function SettingsPage() {
  const { t } = useTranslation(['settings', 'common', 'auth', 'nav'])
  const { principal, isSuperadmin, activeRole, grants } = useAuth()
  const serverInfo = useServerInfo()
  const theme = useThemeStore((s) => s.theme)
  const setTheme = useThemeStore((s) => s.setTheme)
  const density = usePreferencesStore((s) => s.density)
  const setDensity = usePreferencesStore((s) => s.setDensity)
  const reduceMotion = usePreferencesStore((s) => s.reduceMotion)
  const setReduceMotion = usePreferencesStore((s) => s.setReduceMotion)
  const clock = useClientSettings((s) => s.clock)
  const setClock = useClientSettings((s) => s.setClock)
  const startPage = useClientSettings((s) => s.startPage)
  const setStartPage = useClientSettings((s) => s.setStartPage)
  const confirmStop = useClientSettings((s) => s.confirmStop)
  const setConfirmStop = useClientSettings((s) => s.setConfirmStop)
  const [url, patchUrl] = useUrlState(['section', 'q'])
  const section = SECTIONS.find((s) => s === url.section) ?? 'general'
  const query = url.q ?? ''
  const setQuery = (q: string) => patchUrl({ q })
  const setSection = (section: SectionId) => patchUrl({ section, q: undefined })
  const sectionButton = (id: SettingsSectionId) => {
    const entry = settingsSections(isSuperadmin).find((s) => s.id === id)
    if (!entry) return null
    const Icon = entry.icon
    return (
      <NavButton
        current={section === id && query.trim().length === 0}
        onClick={() => setSection(id)}
        icon={<Icon aria-hidden className="size-3.5" />}
      >
        {t(`nav.${id}`)}
      </NavButton>
    )
  }
  // A page this installation does not run, or this principal may not open, is neither
  // linked nor offered as a start page: the same gate the sidebar uses (#474).
  const { navigable } = useViewAccess()
  const linked = (id: string) => {
    const view = viewById(id)
    return view && navigable(view) ? view : undefined
  }
  const aiTools = linked('providers')
  const workspaces = linked('workspaceDashboard')
  const deploy = linked('deploy')
  const lang = currentLanguage()

  useEffect(() => {
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key !== '/' || event.metaKey || event.ctrlKey || event.altKey)
        return
      if (isTypingTarget(event.target)) return
      event.preventDefault()
      event.stopPropagation()
      document.getElementById('settings-search')?.focus()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [])

  const zone = localTimeZone()
  const q = query.trim().toLocaleLowerCase()
  const rowMatches = (name: string, description: string) =>
    q.length === 0 || `${name} ${description}`.toLocaleLowerCase().includes(q)

  const themeName = t('general.theme')
  const themeHint = t('general.themeHint')
  const densityName = t('general.density')
  const densityHint = t('general.densityHint')
  const languageName = t('general.language')
  const languageHint = t('general.languageHint')
  const timeName = t('general.time')
  const timeHint = t('general.timeHint', { zone })
  const startName = t('general.startPage')
  const startHint = t('general.startPageHint')
  const confirmName = t('general.confirmStop')
  const confirmHint = t('general.confirmStopHint')
  const motionName = t('general.reduceMotion')
  const motionHint = t('general.reduceMotionHint')

  const onThemeKey = (
    event: React.KeyboardEvent<HTMLButtonElement>,
    index: number,
  ) => {
    const step =
      event.key === 'ArrowRight' || event.key === 'ArrowDown'
        ? 1
        : event.key === 'ArrowLeft' || event.key === 'ArrowUp'
          ? -1
          : event.key === 'Home'
            ? -index
            : event.key === 'End'
              ? THEMES.length - 1 - index
              : 0
    if (step === 0) return
    event.preventDefault()
    const next = (index + step + THEMES.length) % THEMES.length
    setTheme(THEMES[next]!)
    document.getElementById(`theme-${THEMES[next]}`)?.focus()
  }

  const generalRows = [
    rowMatches(themeName, themeHint) ? (
      <div key="theme" className="flex flex-col gap-2 py-1">
        <div className="flex flex-wrap items-center gap-2 text-body font-medium text-text">
          <span>{themeName}</span>
          <ChangedReset
            show={theme !== DEFAULT_THEME}
            onReset={() => setTheme(DEFAULT_THEME)}
          />
        </div>
        <p className="text-caption text-text-2">{themeHint}</p>
        <div
          role="radiogroup"
          aria-label={themeName}
          className="grid grid-cols-3 gap-3"
        >
          {THEMES.map((value, index) => (
            <button
              key={value}
              id={`theme-${value}`}
              type="button"
              role="radio"
              aria-checked={theme === value}
              tabIndex={theme === value ? 0 : -1}
              onClick={() => setTheme(value)}
              onKeyDown={(event) => onThemeKey(event, index)}
              className={
                theme === value
                  ? 'flex flex-col gap-2 rounded-card border border-accent-border p-2 text-start outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus'
                  : 'flex flex-col gap-2 rounded-card border border-line-strong p-2 text-start outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus'
              }
            >
              <ThemePreview kind={value} />
              <span className="text-caption font-medium text-text">
                {t(`common:theme.${value}`)}
              </span>
            </button>
          ))}
        </div>
      </div>
    ) : null,
    rowMatches(densityName, densityHint) ? (
      <Row
        key="density"
        name={densityName}
        description={densityHint}
        changed={density !== 'comfortable'}
        onReset={() => setDensity('comfortable')}
        control={
          <Segmented<Density>
            aria-label={densityName}
            size="sm"
            value={density}
            onValueChange={setDensity}
            options={[
              { value: 'comfortable', label: t('common:density.comfortable') },
              { value: 'compact', label: t('common:density.compact') },
            ]}
          />
        }
      />
    ) : null,
    rowMatches(languageName, languageHint) ? (
      <Row
        key="language"
        name={languageName}
        description={languageHint}
        changed={lang !== 'en'}
        onReset={() => setLanguage('en')}
        control={
          <Select
            value={lang}
            onValueChange={(value) => setLanguage(value as LanguageCode)}
          >
            <SelectTrigger
              id="language"
              aria-label={languageName}
              className="w-56"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SUPPORTED_LANGUAGES.map((item) => (
                <SelectItem key={item.code} value={item.code}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
      />
    ) : null,
    rowMatches(timeName, timeHint) ? (
      <Row
        key="time"
        name={timeName}
        description={timeHint}
        changed={clock !== CLIENT_SETTING_DEFAULTS.clock}
        onReset={() => setClock(CLIENT_SETTING_DEFAULTS.clock)}
        control={
          <Segmented<ClockFormat>
            aria-label={timeName}
            size="sm"
            value={clock}
            onValueChange={setClock}
            options={[
              { value: '24', label: t('general.clock24') },
              { value: '12', label: t('general.clock12') },
            ]}
          />
        }
      />
    ) : null,
    rowMatches(startName, startHint) ? (
      <Row
        key="start"
        name={startName}
        description={startHint}
        changed={startPage !== CLIENT_SETTING_DEFAULTS.startPage}
        onReset={() => setStartPage(CLIENT_SETTING_DEFAULTS.startPage)}
        control={
          <Select
            value={startPage}
            onValueChange={(value) => setStartPage(value as StartPageId)}
          >
            <SelectTrigger aria-label={startName} className="w-56">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {START_PAGE_IDS.filter(
                (id) => id === startPage || linked(id),
              ).map((id) => (
                <SelectItem key={id} value={id}>
                  {t(`nav:items.${id}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
      />
    ) : null,
    rowMatches(confirmName, confirmHint) ? (
      <Row
        key="confirm"
        name={confirmName}
        description={confirmHint}
        changed={confirmStop !== CLIENT_SETTING_DEFAULTS.confirmStop}
        onReset={() => setConfirmStop(CLIENT_SETTING_DEFAULTS.confirmStop)}
        control={
          <Switch
            checked={confirmStop}
            onCheckedChange={setConfirmStop}
            aria-label={confirmName}
          />
        }
      />
    ) : null,
    rowMatches(motionName, motionHint) ? (
      <Row
        key="motion"
        name={motionName}
        description={motionHint}
        changed={reduceMotion !== false}
        onReset={() => setReduceMotion(false)}
        control={
          <Switch
            checked={reduceMotion}
            onCheckedChange={setReduceMotion}
            aria-label={motionName}
          />
        }
      />
    ) : null,
  ].filter(Boolean)

  const showGeneral = q.length > 0 || section === 'general'

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="grid min-h-0 flex-1 grid-cols-1 md:grid-cols-[240px_minmax(0,1fr)]">
        <nav
          aria-label={t('nav.sections')}
          className="flex flex-col gap-0.5 border-b border-line p-2.5 md:border-r md:border-b-0"
        >
          <label className="mb-2 flex h-8 items-center gap-2 rounded-ctl border border-ctl-border bg-canvas px-2">
            <span className="sr-only">{t('search')}</span>
            <input
              id="settings-search"
              type="search"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t('search')}
              className="w-full bg-transparent text-caption text-text outline-none placeholder:text-text-3"
            />
            <kbd className="text-caption text-text-3">/</kbd>
          </label>
          <NavGroup label={t('groups.you')}>
            {sectionButton('general')}
            {/* Notifications and Session defaults are not stored yet: no section is
                offered until it does something (HU-14). */}
            {sectionButton('keyboard')}
          </NavGroup>
          {aiTools || workspaces ? (
            <NavGroup label={t('groups.work')}>
              <RegistryLink
                view={aiTools}
                label={t('nav.aiTools')}
                icon={<Sparkles aria-hidden className="size-3.5" />}
              />
              <RegistryLink
                view={workspaces}
                label={t('nav.workspaces')}
                icon={<Folder aria-hidden className="size-3.5" />}
              />
            </NavGroup>
          ) : null}
          <NavGroup label={t('groups.installation')}>
            <RegistryLink
              view={deploy}
              label={t('nav.deploy')}
              icon={<Server aria-hidden className="size-3.5" />}
            />
            {sectionButton('signIn')}
            <a
              href="/onboarding"
              className="flex h-8 items-center gap-2.5 rounded-[7px] px-2.5 text-caption font-medium text-text-2 hover:bg-hover hover:text-text"
            >
              <Play aria-hidden className="size-3.5" />
              {t('nav.setup')}
            </a>
            {sectionButton('edition')}
            {sectionButton('tracing')}
            {sectionButton('signing')}
            {sectionButton('about')}
          </NavGroup>
        </nav>
        <div className="min-w-0 overflow-auto">
          <div className="mx-auto flex w-full max-w-[880px] flex-col gap-2 px-7 py-6">
            {showGeneral ? (
              <>
                <PageHeader
                  title={q.length > 0 ? t('title') : t('general.title')}
                  description={q.length > 0 ? undefined : t('general.scope')}
                  actions={<RestoreAll />}
                />
                {generalRows.length > 0 ? (
                  <div className="flex flex-col">{generalRows}</div>
                ) : (
                  <p className="text-body text-text-2">{t('noMatches')}</p>
                )}
              </>
            ) : null}
            {q.length === 0 && section === 'notifications' ? (
              <Unavailable
                title={t('nav.notifications')}
                reason={t('unavailable.notifications')}
              />
            ) : null}
            {q.length === 0 && section === 'sessionDefaults' ? (
              <Unavailable
                title={t('nav.sessionDefaults')}
                reason={t('unavailable.sessionDefaults')}
              />
            ) : null}
            {q.length === 0 && section === 'signIn' ? (
              <>
                <PageHeader title={t('nav.signIn')} />
                <SignInSettings key={principal?.user_id} />
              </>
            ) : null}
            {q.length === 0 && section === 'keyboard' ? <KeyboardList /> : null}
            {q.length === 0 && section === 'edition' ? (
              <Edition
                edition={serverInfo.data?.edition}
                manageModules={isSuperadmin}
              />
            ) : null}
            {q.length === 0 && section === 'tracing' && isSuperadmin ? (
              <TracingSettings key={principal?.user_id} />
            ) : null}
            {q.length === 0 && section === 'signing' && isSuperadmin ? (
              <>
                <PageHeader
                  title={t('nav.signing')}
                  description={t('signing.description')}
                />
                <ReportSigningSettings heading={false} />
              </>
            ) : null}
            {q.length === 0 && section === 'about' ? (
              <About
                version={serverInfo.data?.version}
                engine={serverInfo.data?.engine}
                license={serverInfo.data?.license.status}
                licensee={serverInfo.data?.license.licensee}
                displayName={principal?.display_name}
                email={principal?.email}
                actor={principal?.actor}
                role={
                  isSuperadmin
                    ? t('auth:roles.superadmin')
                    : activeRole
                      ? t(`auth:roles.${activeRole}`, {
                          defaultValue: activeRole,
                        })
                      : '—'
                }
                memberships={grants.length}
              />
            ) : null}
          </div>
        </div>
      </div>
    </div>
  )
}

function NavGroup({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col">
      <p className="px-2.5 pt-3 pb-1 text-caption font-medium text-text-3">
        {label}
      </p>
      {children}
    </div>
  )
}

function NavButton({
  current,
  onClick,
  icon,
  children,
}: {
  current: boolean
  onClick: () => void
  icon: ReactNode
  children: ReactNode
}) {
  return (
    <button
      type="button"
      aria-current={current ? 'page' : undefined}
      onClick={onClick}
      className={
        current
          ? 'flex h-8 items-center gap-2.5 rounded-[7px] bg-active px-2.5 text-start text-caption font-medium text-text'
          : 'flex h-8 items-center gap-2.5 rounded-[7px] px-2.5 text-start text-caption font-medium text-text-2 hover:bg-hover hover:text-text'
      }
    >
      {icon}
      {children}
    </button>
  )
}

function RegistryLink({
  view,
  label,
  icon,
}: {
  view: FeatureView | undefined
  label: string
  icon: ReactNode
}) {
  if (!view) return null
  return (
    <a
      href={view.path}
      className="flex h-8 items-center gap-2.5 rounded-[7px] px-2.5 text-caption font-medium text-text-2 hover:bg-hover hover:text-text"
    >
      {icon}
      {label}
    </a>
  )
}

function Unavailable({ title, reason }: { title: string; reason: string }) {
  return (
    <>
      <PageHeader title={title} description={reason} />
    </>
  )
}

function KeyboardList() {
  const { t } = useTranslation(['settings', 'common'])
  const commands = new Map<string, { command: string; keys: string[] }>()
  for (const rule of KEYBINDINGS) {
    const id = `${rule.command}:${rule.when ?? ''}`
    const row = commands.get(id)
    if (row) row.keys.push(rule.keys)
    else commands.set(id, { command: rule.command, keys: [rule.keys] })
  }
  return (
    <>
      <PageHeader
        title={t('keyboard.title')}
        description={t('keyboard.scope')}
      />
      <ul className="flex flex-col divide-y divide-line">
        {[...commands].map(([id, rule]) => (
          <li
            key={id}
            className="flex items-center justify-between gap-3 py-2 text-body"
          >
            <span className="min-w-0 truncate">
              {t(`common:keys.command.${rule.command}`)}
            </span>
            <span className="flex shrink-0 gap-2">
              {rule.keys.map((keys, index) => (
                <Fragment key={keys}>
                  {index > 0 ? <span className="text-text-3">/</span> : null}
                  <kbd className="font-mono text-caption text-text-2">
                    {keys}
                  </kbd>
                </Fragment>
              ))}
            </span>
          </li>
        ))}
      </ul>
    </>
  )
}

/** What a Business build adds, in the order the console places it. Each entry is a
 * surface a Community build does not offer, because its engine routes answer 501 there
 * or report the control unavailable (WEB slice 5, EDITION-CENSUS.md). */
const BUSINESS_ADDS = [
  'loginEnforcement',
  'identityProviders',
  'groupMapping',
  'regulatory',
  'reports',
  'toolPins',
] as const

/** EDITION & MODULES: the one place that names the build's edition and, on a Community
 * build, what a Business build adds — stated once, with no prompt elsewhere. The edition
 * is the engine's own build fact from server-info, never the license. The engine's paid
 * build string is "enterprise"; the product names that build Business (Enterprise is a
 * negotiated scope, not a build). */
function Edition({
  edition,
  manageModules,
}: {
  edition?: string
  /** A system administrator turns modules on and off here (ARCH C1). */
  manageModules: boolean
}) {
  const { t } = useTranslation('settings')
  const name =
    edition === 'community' || edition === 'enterprise'
      ? t(`edition.editions.${edition}`)
      : (edition ?? t('edition.unknown'))
  return (
    <>
      <PageHeader title={t('edition.title')} description={t('edition.scope')} />
      <dl className="divide-y divide-line">
        <div className="flex items-center justify-between gap-4 py-2">
          <dt className="text-body text-text-2">{t('edition.label')}</dt>
          <dd className="min-w-0 truncate text-body text-text">{name}</dd>
        </div>
      </dl>
      {/* Every optional module can be turned on and off from the product. */}
      {manageModules ? <ModulesSettings /> : null}
      {edition === 'community' ? (
        <section
          aria-labelledby="edition-adds"
          className="mt-4 flex flex-col gap-2"
        >
          <h2 id="edition-adds" className="text-body font-medium text-text">
            {t('edition.businessAdds')}
          </h2>
          <ul className="flex list-disc flex-col gap-1 pl-5 text-body text-text-2">
            {BUSINESS_ADDS.map((key) => (
              <li key={key}>{t(`edition.adds.${key}`)}</li>
            ))}
          </ul>
        </section>
      ) : null}
    </>
  )
}

function About({
  version,
  engine,
  license,
  licensee,
  displayName,
  email,
  actor,
  role,
  memberships,
}: {
  version?: string
  engine?: string
  license?: string
  licensee?: string
  displayName?: string
  email?: string
  actor?: string
  role: string
  memberships: number
}) {
  const { t } = useTranslation('settings')
  const facts = [
    [t('about.version'), version ?? '—'],
    [t('about.engine'), engine ?? '—'],
    // Without a license this is Community: say so, instead of "License status none ·
    // Licensed to None" (HU-27).
    ...(!license || license === 'none'
      ? [[t('about.edition'), t('about.community')]]
      : [
          [t('about.license'), license],
          [t('about.licensee'), licensee || t('about.none')],
        ]),
    displayName ? [t('about.displayName'), displayName] : null,
    email ? [t('about.email'), email] : null,
    [t('about.role'), role],
    [t('about.memberships'), String(memberships)],
  ].filter((row): row is [string, string] => row !== null)
  return (
    <>
      <PageHeader title={t('about.title')} description={t('about.scope')} />
      <dl className="divide-y divide-line">
        {facts.map(([label, value]) => (
          <div
            key={label}
            className="flex items-center justify-between gap-4 py-2"
          >
            <dt className="text-body text-text-2">{label}</dt>
            <dd className="min-w-0 truncate text-body text-text">{value}</dd>
          </div>
        ))}
      </dl>
      {actor ? (
        <details className="mt-4 text-body text-text-2">
          <summary className="cursor-pointer">{t('about.actor')}</summary>
          <p className="mt-2 select-all break-all font-mono text-text">
            {actor}
          </p>
        </details>
      ) : null}
    </>
  )
}
