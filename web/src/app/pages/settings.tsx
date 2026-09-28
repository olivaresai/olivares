// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import {
  Bell,
  Folder,
  Info,
  Keyboard,
  Play,
  Server,
  Shield,
  SlidersHorizontal,
  Sparkles,
  Zap,
} from 'lucide-react'
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
import { viewById } from '@/features/navigation/model'
import {
  CLIENT_SETTING_DEFAULTS,
  localTimeZone,
  START_PAGE_IDS,
  useClientSettings,
  type ClockFormat,
  type StartPageId,
} from '@/features/settings/preferences'
import { usePreferencesStore, type Density } from '@/stores/preferences'
import { useThemeStore, type Theme } from '@/stores/theme'

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

type SectionId =
  | 'general'
  | 'notifications'
  | 'keyboard'
  | 'sessionDefaults'
  | 'signIn'
  | 'about'

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
    setTheme('dark')
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
  const [section, setSection] = useState<SectionId>('general')
  const [query, setQuery] = useState('')
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
            show={theme !== 'dark'}
            onReset={() => setTheme('dark')}
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
              {START_PAGE_IDS.map((id) => (
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
            <NavButton
              current={section === 'general' && q.length === 0}
              onClick={() => setSection('general')}
              icon={<SlidersHorizontal aria-hidden className="size-3.5" />}
            >
              {t('nav.general')}
            </NavButton>
            <NavButton
              current={section === 'notifications' && q.length === 0}
              onClick={() => setSection('notifications')}
              icon={<Bell aria-hidden className="size-3.5" />}
            >
              {t('nav.notifications')}
            </NavButton>
            <NavButton
              current={section === 'keyboard' && q.length === 0}
              onClick={() => setSection('keyboard')}
              icon={<Keyboard aria-hidden className="size-3.5" />}
            >
              {t('nav.keyboard')}
            </NavButton>
          </NavGroup>
          <NavGroup label={t('groups.work')}>
            <NavButton
              current={section === 'sessionDefaults' && q.length === 0}
              onClick={() => setSection('sessionDefaults')}
              icon={<Zap aria-hidden className="size-3.5" />}
            >
              {t('nav.sessionDefaults')}
            </NavButton>
            <RegistryLink
              id="providers"
              label={t('nav.aiTools')}
              icon={<Sparkles aria-hidden className="size-3.5" />}
            />
            <RegistryLink
              id="workspaceDashboard"
              label={t('nav.workspaces')}
              icon={<Folder aria-hidden className="size-3.5" />}
            />
          </NavGroup>
          <NavGroup label={t('groups.installation')}>
            <RegistryLink
              id="deploy"
              label={t('nav.deploy')}
              icon={<Server aria-hidden className="size-3.5" />}
            />
            <NavButton
              current={section === 'signIn' && q.length === 0}
              onClick={() => setSection('signIn')}
              icon={<Shield aria-hidden className="size-3.5" />}
            >
              {t('nav.signIn')}
            </NavButton>
            <a
              href="/setup"
              className="flex h-8 items-center gap-2.5 rounded-[7px] px-2.5 text-caption font-medium text-text-2 hover:bg-hover hover:text-text"
            >
              <Play aria-hidden className="size-3.5" />
              {t('nav.setup')}
            </a>
            <NavButton
              current={section === 'about' && q.length === 0}
              onClick={() => setSection('about')}
              icon={<Info aria-hidden className="size-3.5" />}
            >
              {t('nav.about')}
            </NavButton>
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
              <Unavailable
                title={t('nav.signIn')}
                reason={t('unavailable.signIn')}
              />
            ) : null}
            {q.length === 0 && section === 'keyboard' ? <KeyboardList /> : null}
            {q.length === 0 && section === 'about' ? (
              <About
                version={serverInfo.data?.version}
                engine={serverInfo.data?.engine}
                license={serverInfo.data?.license.status}
                licensee={serverInfo.data?.license.licensee}
                displayName={principal?.display_name}
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
  id,
  label,
  icon,
}: {
  id: string
  label: string
  icon: ReactNode
}) {
  const view = viewById(id)
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
  return (
    <>
      <PageHeader
        title={t('keyboard.title')}
        description={t('keyboard.scope')}
      />
      <ul className="flex flex-col divide-y divide-line">
        {KEYBINDINGS.map((rule) => (
          <li
            key={`${rule.command}:${rule.keys}`}
            className="flex items-center justify-between gap-3 py-2 text-body"
          >
            <span className="min-w-0 truncate">
              {t(`common:keys.command.${rule.command}`)}
            </span>
            <kbd className="shrink-0 font-mono text-caption text-text-2">
              {rule.keys}
            </kbd>
          </li>
        ))}
      </ul>
    </>
  )
}

function About({
  version,
  engine,
  license,
  licensee,
  displayName,
  actor,
  role,
  memberships,
}: {
  version?: string
  engine?: string
  license?: string
  licensee?: string
  displayName?: string
  actor?: string
  role: string
  memberships: number
}) {
  const { t } = useTranslation('settings')
  const facts = [
    [t('about.version'), version ?? '—'],
    [t('about.engine'), engine ?? '—'],
    [t('about.license'), license ?? '—'],
    [t('about.licensee'), licensee || t('about.none')],
    displayName ? [t('about.displayName'), displayName] : null,
    [t('about.actor'), actor ?? '—'],
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
    </>
  )
}
