// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { BoxedList, BoxedListRow } from '@/components/ui/boxed-list'
import { Fragment, useEffect } from 'react'
import { SystemAdminOnly } from '@/components/layout/system-admin-only'
import { AppLink } from '@/components/ui/app-link'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Kbd } from '@/components/ui/kbd'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Segmented } from '@/components/ui/segmented'
import { EditionBadgeSetting } from '@/features/console/edition-badge-setting'
import { Switch } from '@/components/ui/switch'
import { useAuth } from '@/lib/auth/context'
import { editionName } from '@/lib/edition'
import { EDITOR_IDS, EDITORS, type EditorId } from '@/lib/editor-links'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import {
  setLanguage,
  SUPPORTED_LANGUAGES,
  type LanguageCode,
  currentLanguage,
} from '@/lib/i18n'
import { chordHint, chordKeys, isTypingTarget } from '@/lib/keybindings/model'
import { COMMAND_GROUP, KEY_GROUPS, KEYBINDINGS } from '@/lib/keybindings/table'
import { SignInSettings } from '@/features/settings/sign-in-settings'
import { LimitResumePreference } from '@/features/sessions/limit-resume-preference'
import { SessionNotificationSetting } from '@/features/settings/session-notification-setting'
import { ModulesSettings } from '@/features/settings/modules-settings'
import { useEditionSummary } from '@/features/settings/edition-summary'
import { LicenseTab } from '@/features/console/license-tab'
// The license panel's strings: this page registers the namespace itself, the
// way the console view does for its own tab.
import '@/features/console/i18n'
import {
  SETTINGS_SECTIONS,
  settingsSections,
  type SettingsSection,
} from '@/features/settings/sections'
import { useUrlState } from '@/lib/hooks/use-url-state'
import { ReportSigningSettings } from '@/features/settings/report-signing-settings'
import { PermissionModesSettings } from '@/features/settings/permission-modes-settings'
import { SessionIdleSettings } from '@/features/settings/session-idle-settings'
import { TracingSettings } from '@/features/settings/tracing-settings'
import { useViewAccess } from '@/features/navigation/authorization'
import { fold, viewById } from '@/features/navigation/model'
import {
  CLIENT_SETTING_DEFAULTS,
  localTimeZone,
  notificationOwner,
  START_PAGE_IDS,
  useClientSettings,
  type ClockFormat,
  type SendKey,
  type SendWhileRunning,
  type StartPageId,
} from '@/features/settings/preferences'
import { usePreferencesStore, type Density } from '@/stores/preferences'
import { DEFAULT_THEME, useThemeStore, type Theme } from '@/stores/theme'

import {
  ChangedReset,
  SettingsRow as Row,
} from '@/features/settings/settings-row'
import { SessionDefaultsSettings } from '@/features/settings/session-defaults-settings'
import { NotificationEventSettings } from '@/features/settings/notification-event-settings'

const THEMES = ['system', 'light', 'dark', 'black'] as const

/**
 * Samples of the three frames. A child cannot escape `.dark` variable inheritance. A bar is
 * line-strong over the frame in every theme.
 */
const PREVIEW = {
  light: {
    frame: '#ecebe9',
    canvas: '#fafaf9',
    bar: '#cdcccb',
    accent: '#f08000',
  },
  dark: {
    frame: '#1b1b1e',
    canvas: '#28282b',
    bar: '#39393b',
    accent: '#f08000',
  },
  black: {
    frame: '#000000',
    canvas: '#000000',
    bar: '#262626',
    accent: '#f08000',
  },
} as const

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

function PreviewPane({ tone }: { tone: keyof typeof PREVIEW }) {
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

/**
 * General's titled groups, by the key of each row. A row no group names (a
 * search result from another section) shows in an untitled list after them.
 */
const GENERAL_GROUPS: [string, string[]][] = [
  ['appearance', ['theme', 'density', 'motion']],
  ['language', ['language', 'time']],
  ['start', ['start']],
  ['sessions', ['sendKey', 'sendWhileRunning', 'confirm', 'usage-reset']],
  ['editor', ['editor', 'editorSsh']],
]
const GROUPED_KEYS = GENERAL_GROUPS.flatMap(([, keys]) => keys)

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
  return (
    <Button type="button" variant="ghost" size="sm" onClick={onClick}>
      {t('restoreAll')}
    </Button>
  )
}

export function SettingsPage() {
  const { t } = useTranslation(['settings', 'common', 'auth', 'nav', 'console'])
  const { principal, activeTenant, isSuperadmin, activeRole, grants, can } =
    useAuth()
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
  const sendKey = useClientSettings((s) => s.sendKey)
  const setSendKey = useClientSettings((s) => s.setSendKey)
  const sendWhileRunning = useClientSettings((s) => s.sendWhileRunning)
  const setSendWhileRunning = useClientSettings((s) => s.setSendWhileRunning)
  const setConfirmStop = useClientSettings((s) => s.setConfirmStop)
  const editor = useClientSettings((s) => s.editor)
  const setEditor = useClientSettings((s) => s.setEditor)
  const editorSsh = useClientSettings((s) => s.editorSsh)
  const setEditorSsh = useClientSettings((s) => s.setEditorSsh)
  // `add` and `returnTo` belong to the AI tools room (the setup wizard's "Add an API key"):
  // owned here only so that leaving a section can clear them.
  const [url] = useUrlState(['section', 'q', 'add', 'returnTo'])
  // A page this installation does not run, or this principal may not open, is neither a
  // section nor offered as a start page: the same gate the sidebar uses.
  const { navigable, state } = useViewAccess()
  const offered = settingsSections(isSuperadmin, navigable)
  // An address to a section this person is not offered, or that does not exist, shows General.
  const section = offered.find((s) => s.id === url.section)?.id ?? 'general'
  // A section only a system administrator is offered, asked for by address: it says so, and
  // never opens General in its place. Its own mark or the permission of the page it hosts.
  const refused = (s: SettingsSection) => {
    const hosted = s.view ? viewById(s.view) : undefined
    return (
      (s.adminOnly && !isSuperadmin) || (!!hosted && state(hosted) === 'denied')
    )
  }
  const withheld = SETTINGS_SECTIONS.find(
    (s) => s.id === url.section && refused(s),
  )?.id
  const hostedView = offered.find((s) => s.id === section)?.view
  const query = url.q ?? ''
  // The wizard's address stays behind when the hosted room goes away (another section, or a
  // search in its place), or the add-a-key form would reopen when the room mounts again.
  // A section is a place, not a filter: Back returns to the one before it, inside Settings.
  const linked = (id: string) => {
    const view = viewById(id)
    return view && navigable(view) ? view : undefined
  }
  const lang = currentLanguage()

  useEffect(() => {
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key !== '/' || event.metaKey || event.ctrlKey || event.altKey)
        return
      if (isTypingTarget(event.target)) return
      const search = document.getElementById('settings-search')
      if (!search) return
      event.preventDefault()
      event.stopPropagation()
      search.focus()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [])

  const zone = localTimeZone()
  // Folded, like the palette's haystack: a query typed without diacritics
  // must find the accented label here too, or the two searches disagree.
  const q = fold(query.trim())
  // A hosted page is the page's own element, mounted here instead of at its route: the
  // same room, never a second copy of it. A search query replaces it, as it
  // does every section.
  const hostedRoom =
    q.length === 0 && hostedView ? viewById(hostedView)?.element() : null
  const rowMatches = (name: string, description: string) =>
    q.length === 0 || fold(`${name} ${description}`).includes(q)

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
  const sendKeyName = t('general.sendKey')
  const sendKeyHint = t('general.sendKeyHint')
  const sendRunName = t('general.sendWhileRunning')
  const sendRunHint = t('general.sendWhileRunningHint')
  const confirmName = t('general.confirmStop')
  const confirmHint = t('general.confirmStopHint')
  const editorName = t('general.editor')
  const editorHint = t('general.editorHint')
  const editorSshName = t('general.editorSsh')
  const editorSshHint = t('general.editorSshHint')
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
          className="grid grid-cols-2 gap-3 sm:grid-cols-4"
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
    rowMatches(sendKeyName, sendKeyHint) ? (
      <Row
        key="sendKey"
        name={sendKeyName}
        description={sendKeyHint}
        changed={sendKey !== CLIENT_SETTING_DEFAULTS.sendKey}
        onReset={() => setSendKey(CLIENT_SETTING_DEFAULTS.sendKey)}
        control={
          <Segmented<SendKey>
            aria-label={sendKeyName}
            size="sm"
            value={sendKey}
            onValueChange={setSendKey}
            options={[
              { value: 'enter', label: 'Enter' },
              { value: 'modEnter', label: chordHint('Mod+Enter') },
            ]}
          />
        }
      />
    ) : null,
    rowMatches(sendRunName, sendRunHint) ? (
      <Row
        key="sendWhileRunning"
        name={sendRunName}
        description={sendRunHint}
        changed={sendWhileRunning !== CLIENT_SETTING_DEFAULTS.sendWhileRunning}
        onReset={() =>
          setSendWhileRunning(CLIENT_SETTING_DEFAULTS.sendWhileRunning)
        }
        control={
          <Segmented<SendWhileRunning>
            aria-label={sendRunName}
            size="sm"
            value={sendWhileRunning}
            onValueChange={setSendWhileRunning}
            options={[
              { value: 'queue', label: t('general.sendQueue') },
              { value: 'steer', label: t('general.sendSteer') },
            ]}
          />
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
    rowMatches(editorName, editorHint) ? (
      <Row
        key="editor"
        name={editorName}
        description={editorHint}
        changed={editor !== CLIENT_SETTING_DEFAULTS.editor}
        onReset={() => setEditor(CLIENT_SETTING_DEFAULTS.editor)}
        control={
          <Select
            value={editor}
            onValueChange={(value) => setEditor(value as EditorId)}
          >
            <SelectTrigger aria-label={editorName} className="w-56">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {EDITOR_IDS.map((id) => (
                <SelectItem key={id} value={id}>
                  {EDITORS[id].label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
      />
    ) : null,
    rowMatches(editorSshName, editorSshHint) ? (
      <Row
        key="editorSsh"
        name={editorSshName}
        description={editorSshHint}
        changed={editorSsh !== CLIENT_SETTING_DEFAULTS.editorSsh}
        onReset={() => setEditorSsh(CLIENT_SETTING_DEFAULTS.editorSsh)}
        control={
          <Input
            aria-label={editorSshName}
            value={editorSsh}
            onChange={(event) => setEditorSsh(event.target.value)}
            placeholder="user@host"
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            className="w-56"
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
    q.length > 0 &&
    rowMatches(
      t('sessionNotifications.title'),
      t('sessionNotifications.hint'),
    ) ? (
      <SessionNotificationSetting
        key={
          notificationOwner(principal?.user_id, activeTenant) ?? 'notifications'
        }
        owner={notificationOwner(principal?.user_id, activeTenant)}
      />
    ) : null,
    q.length > 0 &&
    rowMatches(
      t('nav.newSessions'),
      (['tool', 'account', 'model', 'folder'] as const)
        .map((field) => t(`newSessions.${field}`))
        .join(' '),
    ) ? (
      <section key="newSessions">
        <h2 className="text-body-l font-semibold text-text">
          {t('nav.newSessions')}
        </h2>
        <SessionDefaultsSettings />
      </section>
    ) : null,
    q.length > 0 &&
    rowMatches(
      t('nav.notifications'),
      (['needsYou', 'finished', 'failed'] as const)
        .map((event) => t(`notificationEvents.${event}`))
        .join(' '),
    ) ? (
      <section key="events">
        <h2 className="text-body-l font-semibold text-text">
          {t('nav.notifications')}
        </h2>
        <NotificationEventSettings />
      </section>
    ) : null,
    can('sessions:run:write') &&
    linked('sessions') &&
    rowMatches(
      t('sessions:limit.automatic'),
      t('sessions:limit.automatic', { lng: 'en' }),
    ) ? (
      <div key="usage-reset" className="border-t border-line py-3.5">
        <LimitResumePreference />
      </div>
    ) : null,
  ].filter(Boolean)
  const groups = [
    ...GENERAL_GROUPS.map(([group, keys]) => ({
      group,
      rows: generalRows.filter((row) => keys.includes(row?.key ?? '')),
    })),
    {
      group: '',
      rows: generalRows.filter((row) => !GROUPED_KEYS.includes(row?.key ?? '')),
    },
  ].filter(({ rows }) => rows.length > 0)

  const showGeneral = q.length > 0 || (section === 'general' && !withheld)
  const editionLabel = t('nav.edition')
  const showLicenseResult =
    q.length > 0 &&
    (rowMatches(editionLabel, t('nav.edition', { lng: 'en' })) ||
      rowMatches(
        t('console:license.title'),
        t('console:license.title', { lng: 'en' }),
      ))
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="grid min-h-0 flex-1 grid-cols-1">
        <div className="min-w-0 overflow-auto">
          <div className="mx-auto flex w-full max-w-[880px] flex-col gap-2 px-7 py-6">
            {q.length === 0 && withheld ? (
              <>
                <PageHeader title={t(`nav.${withheld}`)} />
                <SystemAdminOnly />
              </>
            ) : null}
            {showGeneral ? (
              <>
                <PageHeader
                  title={q.length > 0 ? t('title') : t('general.title')}
                  description={q.length > 0 ? undefined : t('general.scope')}
                  actions={<RestoreAll />}
                />
                {generalRows.length > 0 || showLicenseResult ? (
                  groups.map(({ group, rows }) =>
                    group ? (
                      <section
                        key={group}
                        aria-labelledby={`general-${group}`}
                        className="flex flex-col gap-2"
                      >
                        <h2
                          id={`general-${group}`}
                          className="text-body-l font-semibold text-text"
                        >
                          {t(`general.groups.${group}`)}
                        </h2>
                        <BoxedList>{rows}</BoxedList>
                      </section>
                    ) : (
                      <BoxedList key="other">{rows}</BoxedList>
                    ),
                  )
                ) : (
                  <p className="text-body text-text-2">{t('noMatches')}</p>
                )}
                {showLicenseResult ? (
                  <AppLink
                    href="/settings?section=edition"
                    className="text-body font-medium text-foreground underline underline-offset-2"
                  >
                    {editionLabel}
                  </AppLink>
                ) : null}
              </>
            ) : null}
            {q.length === 0 && section === 'signIn' ? (
              <>
                <PageHeader title={t('nav.signIn')} />
                <SignInSettings key={principal?.user_id} />
              </>
            ) : null}
            {q.length === 0 && section === 'keyboard' ? <KeyboardList /> : null}
            {q.length === 0 && section === 'newSessions' ? (
              <>
                <PageHeader
                  title={t('nav.newSessions')}
                  description={t('newSessions.scope')}
                />
                <SessionDefaultsSettings />
              </>
            ) : null}
            {q.length === 0 && section === 'notifications' ? (
              <>
                <PageHeader title={t('nav.notifications')} />
                <SessionNotificationSetting
                  key={
                    notificationOwner(principal?.user_id, activeTenant) ??
                    'notifications'
                  }
                  owner={notificationOwner(principal?.user_id, activeTenant)}
                />
                <NotificationEventSettings />
              </>
            ) : null}
            {hostedRoom}
            {q.length === 0 && section === 'edition' ? (
              <>
                {/* The section's h1 precedes the license panel's h2: heading
                    order must read h1 → h2, as it does under the console's h1. */}
                <PageHeader
                  title={t('nav.edition')}
                  description={t('edition.scope')}
                />
                <LicenseTab />
                <Edition
                  edition={serverInfo.data?.edition}
                  superadmin={isSuperadmin}
                />
              </>
            ) : null}
            {q.length === 0 && section === 'modules' ? (
              <>
                <PageHeader
                  title={t('nav.modules')}
                  description={t('modules.description')}
                />
                <ModulesSettings />
              </>
            ) : null}
            {q.length === 0 && section === 'permissions' ? (
              <>
                <PageHeader
                  title={t('nav.permissions')}
                  description={t('permissions.description')}
                />
                <PermissionModesSettings />
              </>
            ) : null}
            {q.length === 0 && section === 'sessionIdle' ? (
              <>
                <PageHeader
                  title={t('nav.sessionIdle')}
                  description={t('sessionIdle.description')}
                />
                <SessionIdleSettings key={principal?.user_id} />
              </>
            ) : null}
            {q.length === 0 && section === 'tracing' ? (
              <TracingSettings key={principal?.user_id} />
            ) : null}
            {q.length === 0 && section === 'signing' ? (
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
      {KEY_GROUPS.map((group) => {
        // Grouped and printed as the shortcut help does: one cap per key.
        const rows = [...commands].filter(
          ([, rule]) => COMMAND_GROUP[rule.command] === group,
        )
        if (rows.length === 0) return null
        return (
          <section
            key={group}
            aria-labelledby={`keys-${group}`}
            className="flex flex-col gap-2"
          >
            <h2
              id={`keys-${group}`}
              className="text-body-l font-semibold text-text"
            >
              {t(`common:keys.group.${group}`)}
            </h2>
            <BoxedList role="list">
              {rows.map(([id, rule]) => (
                <BoxedListRow
                  key={id}
                  role="listitem"
                  className="justify-between"
                >
                  <span className="min-w-0 truncate text-body">
                    {t(`common:keys.command.${rule.command}`)}
                  </span>
                  <span className="flex shrink-0 items-center gap-2">
                    {rule.keys.map((keys, index) => (
                      <Fragment key={keys}>
                        {index > 0 ? (
                          <span className="text-text-3">/</span>
                        ) : null}
                        <span className="flex gap-1">
                          {chordKeys(keys).map((token) => (
                            <Kbd key={token}>{token}</Kbd>
                          ))}
                        </span>
                      </Fragment>
                    ))}
                  </span>
                </BoxedListRow>
              ))}
            </BoxedList>
          </section>
        )
      })}
    </>
  )
}

/** EDITION: the one place that names the build's edition and, on a Community build, what
 * a Business build adds — stated once, with no prompt elsewhere. The edition is the
 * engine's own build fact from server-info, never the license. A superadmin already reads
 * it on the license panel above, so the row is only for everyone else. The engine's paid
 * build string is "enterprise"; the product names that build Business (Enterprise is a
 * negotiated scope, not a build). The module switches are the Modules section. */
function Edition({
  edition,
  superadmin,
}: {
  edition?: string
  superadmin: boolean
}) {
  const { t } = useTranslation('settings')
  const summary = useEditionSummary()
  const name = editionName(edition, t) ?? edition ?? t('edition.unknown')
  return (
    <>
      {!superadmin ? (
        <dl className="divide-y divide-line">
          <div className="flex items-center justify-between gap-4 py-2">
            <dt className="text-body text-text-2">{t('edition.label')}</dt>
            <dd className="min-w-0 truncate text-body text-text">{name}</dd>
          </div>
        </dl>
      ) : null}
      <EditionBadgeSetting />
      {edition === 'community' ? (
        <section
          aria-labelledby="edition-adds"
          className="mt-4 flex flex-col gap-2"
        >
          <h2 id="edition-adds" className="text-body font-medium text-text">
            {t('edition.businessAdds')}
          </h2>
          {/* The summary of docs/editions.md, as on the license panel. */}
          <ul className="flex list-disc flex-col gap-1 pl-5 text-body text-text-2">
            {summary.business.map(({ text, lang }) => (
              <li key={text} lang={lang}>
                {text}
              </li>
            ))}
          </ul>
          {summary.enterprise.map(({ text, lang }) => (
            <p key={text} lang={lang} className="text-body text-text-2">
              {text}
            </p>
          ))}
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
    // Licensed to None".
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
            <dd
              className="min-w-0 truncate text-body text-text"
              data-testid={
                label === t('about.version') ? 'about-version' : undefined
              }
            >
              {value}
            </dd>
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
