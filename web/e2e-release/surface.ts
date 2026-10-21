// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { globSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { expect, type Page, type Request } from '@playwright/test'
import auth from '../src/lib/i18n/locales/en/auth.json' with { type: 'json' }

/** The release grammar: bare MAJOR.MINOR, the one scripts/release-first-hour.sh admits. */
export const RELEASE_VERSION = /^\d+\.\d+$/

/** Where the console says which deployment this is: the shell and the signed-out line.
 * ponytail: any visible leaf in the sidebar that is exactly a version (a workspace named
 * "2.0") is read as one; give the sidebar version a data-testid and scan only that. */
const IDENTITY =
  'aside[aria-label="Primary"], [data-testid="deployment-identity"]'

/** An identity element that is only a version (0.1, v1.0, 26.10.1, 1.0-rc1), never a host
 * such as 127.0.0.1:8443. A bare MAJOR.MINOR elsewhere is prose (CVSS v3.1, SCIM 2.0). */
const VERSION_DISPLAY = /^v?\d+\.\d+(?:\.\d+)?(?:[-+][\w.+-]+)?$/

export async function expectReleaseVersion(
  page: Page,
  version: string,
): Promise<void> {
  // Dialogs hide the sidebar from the accessibility tree, but its version stays
  // painted. Match the deployment identity itself, never a release in page copy.
  await expect(
    page.locator(IDENTITY).getByText(version, { exact: true }),
    `release version ${version} must be visible in the console identity`,
  ).toBeVisible()
}

// Like src/test/i18n-keys.ts, use the real dictionaries. Playwright runs in
// Node, so Vite's import.meta.glob and the Vitest assertion helper cannot run here.
const translationKeys = new Set<string>()
const collectKeys = (value: unknown, prefix = '') => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return
  for (const [key, child] of Object.entries(value)) {
    const path = prefix ? `${prefix}.${key}` : key
    translationKeys.add(path)
    collectKeys(child, path)
  }
}
const source = fileURLToPath(new URL('../src/', import.meta.url))
for (const path of globSync(
  ['lib/i18n/locales/en/*.json', 'features/**/i18n/en.json'],
  { cwd: source },
)) {
  collectKeys(JSON.parse(readFileSync(`${source}/${path}`, 'utf8')))
}
if (!translationKeys.size) throw new Error('No English translation keys found')

/** How long the gate waits for a page or control to become ready. Waits return as
 * soon as it is; only a page still loading after this is a failure (#1177). */
export const READY_MS = 30_000
/** The console's target for a load. A longer one is recorded with its time. */
export const SLOW_MS = 5000

export function surfaceOptions(version: string): {
  version: string
  identity: string
  versionDisplay: string
  translationKeys: string[]
  pendingLabels: string[]
  readyMs: number
  slowMs: number
} {
  return {
    version,
    identity: IDENTITY,
    versionDisplay: VERSION_DISPLAY.source,
    translationKeys: [...translationKeys],
    pendingLabels: [auth.setup.creating, auth.login.signingIn],
    readyMs: READY_MS,
    slowMs: SLOW_MS,
  }
}

declare global {
  interface Window {
    __firstHourFinding: (message: string) => Promise<void>
    __firstHourSlow?: (message: string, waitedMs: number) => Promise<void>
    __firstHourLoading?: () => boolean
    __firstHourVersions?: () => string[]
  }
}

/** Runs in the page, across SPA transitions; report text, never input values. */
export function watchSurface({
  version,
  identity,
  versionDisplay,
  translationKeys,
  pendingLabels,
  readyMs,
  slowMs,
}: ReturnType<typeof surfaceOptions>): void {
  const declaredKeys = new Set(translationKeys)
  const reported = new Set<string>()
  let loadingSince: number | undefined
  let route = location.pathname + location.search
  // The browser's own test: a closed <details> keeps its content laid out under
  // content-visibility: hidden, with boxes but nothing painted (#330).
  const visible = (element: Element) =>
    element.getClientRects().length > 0 &&
    element.checkVisibility({ visibilityProperty: true })
  const seconds = (ms: number) => (ms / 1000).toFixed(1)
  // Evidence only; a page closing while it is sent loses nothing the run needs.
  const slow = (message: string, waitedMs: number) =>
    void window.__firstHourSlow?.(message, waitedMs).catch(() => undefined)
  // The gate asks this at every stage end, and waits for false (#1177).
  window.__firstHourLoading = () => loadingSince !== undefined
  // The versions the identity paints right now: the one thing a stale deployment
  // changes. A number in page copy is not an identity, so only these are compared.
  const display = new RegExp(versionDisplay)
  const identityVersions = (): string[] => {
    const found = new Set<string>()
    for (const region of document.querySelectorAll(identity)) {
      for (const element of [region, ...region.querySelectorAll('*')]) {
        if (element.childElementCount > 0) continue
        const text = element.textContent?.trim() ?? ''
        if (display.test(text) && visible(element)) found.add(text)
      }
    }
    return [...found]
  }
  window.__firstHourVersions = identityVersions
  const report = (problem: string) => {
    const message = `${location.pathname}: ${problem}`
    if (!reported.has(message)) {
      reported.add(message)
      void window.__firstHourFinding(message)
    }
  }
  const inspect = () => {
    if (!document.body) return
    const copy = [document.body.innerText]
    for (const element of document.querySelectorAll<HTMLElement>(
      '[placeholder], [aria-label], [title], [alt]',
    )) {
      if (!visible(element)) continue
      for (const attribute of ['placeholder', 'aria-label', 'title', 'alt']) {
        // Filled controls do not display their placeholder. Never read values
        // into evidence; use the browser's placeholder visibility predicate.
        if (
          attribute === 'placeholder' &&
          !element.matches(':placeholder-shown')
        )
          continue
        copy.push(element.getAttribute(attribute) ?? '')
      }
    }
    const text = copy.join('\n')
    const keys =
      text
        .replace(/(?:https?:\/\/|[\w.+-]+@)\S+/g, '')
        .match(
          /(?<![\w./:@-])(?:[a-zA-Z][\w-]*:[a-zA-Z][\w-]*(?:\.[a-zA-Z][\w-]*)*|(?:[a-zA-Z][\w-]*\.)+[a-zA-Z][\w-]*)\b/g,
        ) ?? []
    for (const key of keys) {
      // Exact dictionary paths catch namespace-relative fallbacks. Preserve the
      // existing shape check for missing keys in the journey's known namespaces;
      // arbitrary roots/colon tokens also occur in filenames and resource URIs.
      if (
        declaredKeys.has(key) ||
        /^(?:keys|common|nav|auth|errors|settings|providers|firstHour|sessions|agentops|onboarding|deploy|workspaces)[.:]/.test(
          key,
        )
      ) {
        report(`unresolved translation key: ${key}`)
      }
    }
    if (
      /\b(?:HTTP(?: status)?|status(?: code)?|error|answers?)\s*[:=]?\s*[1-5]\d{2}\b|\b[45]\d{2}\s+(?:Unauthorized|Forbidden|Not Found|Service Unavailable)\b|^\s*[45]\d{2}\s*$/im.test(
        text,
      )
    ) {
      report('HTTP status code shown to the user')
    }
    for (const found of identityVersions()) {
      if (found !== version)
        report(`page version ${found} differs from release ${version}`)
    }
    for (const field of document.querySelectorAll<
      HTMLInputElement | HTMLTextAreaElement
    >('input:not([type=hidden]), textarea')) {
      if (!visible(field)) continue
      const label = [...(field.labels ?? [])]
        .map((node) => node.textContent)
        .join(' ')
      const hint = `${label} ${field.getAttribute('aria-label') ?? ''} ${field.placeholder}`
      if (
        /\b(?:folder|directory|filesystem|file.system|config(?:uration)? home|user home|(?:file|config|absolute) path)\b|(?:^|\s)\/(?:home|tmp|var|workspace|Users)\//i.test(
          hint,
        )
      ) {
        report(
          `filesystem path field: ${label.trim() || field.getAttribute('aria-label') || 'path placeholder'}`,
        )
      }
    }
    let pendingAction = false
    for (const button of document.querySelectorAll<HTMLElement>(
      'button:disabled, button[aria-disabled=true], [role=button][aria-disabled=true]',
    )) {
      if (
        !visible(button) ||
        !(
          button.getAttribute('type') === 'submit' ||
          button.classList.contains('bg-accent') ||
          button.dataset.variant === 'primary'
        )
      )
        continue
      const reason = (button.getAttribute('aria-describedby') ?? '')
        .split(/\s+/)
        .some((id) => {
          const node = document.getElementById(id)
          return node && visible(node) && !!node.textContent?.trim()
        })
      const progress =
        pendingLabels.includes(button.innerText.trim()) ||
        ([...button.querySelectorAll('.animate-spin, [role=progressbar]')].some(
          visible,
        ) &&
          /…|\.\.\./.test(button.innerText))
      pendingAction ||= progress
      if (!reason && !progress)
        report(
          `disabled primary button without a visible reason: ${button.innerText.trim()}`,
        )
    }
    const nextRoute = location.pathname + location.search
    if (nextRoute !== route) {
      // A load the page left is still evidence when it was already slow.
      if (loadingSince !== undefined) {
        const waited = performance.now() - loadingSince
        if (waited > slowMs)
          slow(
            `${route.split('?')[0]}: left while loading after ${seconds(waited)} s`,
            waited,
          )
      }
      route = nextRoute
      loadingSince = undefined
    }
    // Track the loading state, not node identity: React can replace a skeleton
    // without ever showing content. Do not reset the clock for those replacements.
    const loading =
      pendingAction ||
      [
        ...document.querySelectorAll(
          '.animate-spin, .animate-pulse, [aria-busy=true], [role=progressbar]',
        ),
      ].some(visible)
    const now = performance.now()
    if (!loading) {
      if (loadingSince !== undefined && now - loadingSince > slowMs)
        slow(
          `${location.pathname}: loading took ${seconds(now - loadingSince)} s`,
          now - loadingSince,
        )
      loadingSince = undefined
    } else if (loadingSince === undefined) loadingSince = now
    else if (now - loadingSince > readyMs)
      report(`loading longer than ${readyMs / 1000} s`)
  }
  inspect()
  setInterval(inspect, 100)
}

/** One request the page made, in the clock of the engine log (UTC), so the line that
 * answered it can be found there by its request id or its time. */
export interface PageRequest {
  method: string
  /** The path only: a query can carry a credential. */
  path: string
  /** The browser's resource type: a Suspense spinner waits on a `script` chunk. */
  type: string
  /** null: no response arrived. */
  status: number | null
  startedAt: string
  ms: number
  /** The engine's X-Request-ID, the `request_id` of its log line. */
  requestId?: string
  /** Still open when the load ended or was left: ms is the time it had been open. A
   * stream the page opened earlier is listed the same way; startedAt tells them apart. */
  pending?: true
  error?: string
}

/** A load over the console's target and what the page asked for while it waited (#2608). */
export interface SlowLoad {
  load: string
  requests: PageRequest[]
  /** Requests that overlapped the wait but are not listed. */
  omitted: number
}

/** A load lists the requests with the longest share of its wait, which is enough to name
 * the one it waited on; a request open for minutes shares no more than one that took 7 s. */
const SLOW_REQUESTS_KEPT = 20

type SeenRequest = Omit<PageRequest, 'startedAt' | 'ms'> & {
  start: number
  end?: number
}

/** Receives the watcher's slow loads (window.__firstHourSlow) and keeps, for each, the
 * page's own requests that overlapped it, from the browser's network events. The
 * returned list grows as loads are reported. */
export async function recordSlowLoads(
  page: Page,
  redact: (text: string) => string = (text) => text,
): Promise<SlowLoad[]> {
  const seen = new Map<Request, SeenRequest>()
  page.on('request', (request) => {
    seen.set(request, {
      method: request.method(),
      path: new URL(request.url()).pathname,
      type: request.resourceType(),
      status: null,
      start: Date.now(),
    })
  })
  page.on('response', (response) => {
    const request = response.request()
    const entry = seen.get(request)
    if (!entry) return
    entry.status = response.status()
    entry.requestId = response.headers()['x-request-id']
    // The browser reports neither a response nor a failure for a request in flight when the
    // main frame loads another document, so without this it stays open for the rest of the
    // run and is listed under every later slow load as one the page still waits on (#2608).
    if (!request.isNavigationRequest() || request.frame() !== page.mainFrame())
      return
    const closedAt = Date.now()
    for (const [other, open] of seen) {
      if (other === request || open.end !== undefined) continue
      open.end = closedAt
      open.error = 'the page loaded another document'
    }
  })
  page.on('requestfinished', (request) => {
    const entry = seen.get(request)
    if (entry) entry.end = Date.now()
  })
  page.on('requestfailed', (request) => {
    const entry = seen.get(request)
    if (entry) {
      entry.end = Date.now()
      entry.error = request.failure()?.errorText
    }
  })
  const slowLoads: SlowLoad[] = []
  await page.exposeFunction(
    '__firstHourSlow',
    (load: string, waitedMs: number) => {
      const reported = Date.now()
      const from = reported - waitedMs
      const shareOfWait = ({ start, end }: SeenRequest) =>
        (end ?? reported) - Math.max(start, from)
      const overlapping = [...seen.values()].filter(
        (entry) => shareOfWait(entry) >= 0,
      )
      const requests = overlapping
        .sort((a, b) => shareOfWait(b) - shareOfWait(a))
        .slice(0, SLOW_REQUESTS_KEPT)
        .map(({ start, end, requestId, error, ...entry }): PageRequest => ({
          ...entry,
          path: redact(entry.path),
          startedAt: new Date(start).toISOString(),
          ms: (end ?? reported) - start,
          ...(requestId && { requestId: redact(requestId) }),
          ...(error && { error: redact(error) }),
          ...(end === undefined && { pending: true as const }),
        }))
        .sort((a, b) => a.startedAt.localeCompare(b.startedAt))
      slowLoads.push({
        load: redact(load),
        requests,
        omitted: overlapping.length - requests.length,
      })
    },
  )
  return slowLoads
}

/** Opens AI tools' install review for one tool and returns the menu item that starts it:
 * Install on the not-installed line, or Update in the options of a tool a provider's
 * first test already installed. The page shows either only once its reads finish, so
 * the choice waits for one of them instead of counting a page still loading (#330). */
export async function openToolInstall(page: Page, name: string) {
  const missing = page.getByTestId('not-installed').getByText(name)
  const options = page.getByRole('button', {
    name: `Options for ${name}`,
    exact: true,
  })
  await expect(missing.or(options).first()).toBeVisible()
  if ((await missing.count()) > 0) {
    await page
      .getByTestId('not-installed')
      .getByRole('button', { name: /^Install/ })
      .click()
    return page.getByRole('menuitem', { name: `Install ${name}`, exact: true })
  }
  await options.click()
  return page.getByRole('menuitem', { name: 'Update', exact: true })
}
