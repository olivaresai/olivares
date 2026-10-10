// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { globSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { expect, type Page } from '@playwright/test'
import auth from '../src/lib/i18n/locales/en/auth.json' with { type: 'json' }

export async function expectReleaseVersion(
  page: Page,
  version: string,
): Promise<void> {
  // Dialogs hide the sidebar from the accessibility tree, but its version stays
  // painted. Match the deployment identity itself, never a release in page copy.
  await expect(
    page
      .locator(
        'aside[aria-label="Primary"], [data-testid="deployment-identity"]',
      )
      .getByText(version, { exact: true }),
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
  translationKeys: string[]
  pendingLabels: string[]
  readyMs: number
  slowMs: number
} {
  return {
    version,
    translationKeys: [...translationKeys],
    pendingLabels: [auth.setup.creating, auth.login.signingIn],
    readyMs: READY_MS,
    slowMs: SLOW_MS,
  }
}

declare global {
  interface Window {
    __firstHourFinding: (message: string) => Promise<void>
    __firstHourSlow?: (message: string) => Promise<void>
    __firstHourLoading?: () => boolean
  }
}

/** Runs in the page, across SPA transitions; report text, never input values. */
export function watchSurface({
  version,
  translationKeys,
  pendingLabels,
  readyMs,
  slowMs,
}: ReturnType<typeof surfaceOptions>): void {
  const declaredKeys = new Set(translationKeys)
  const reported = new Set<string>()
  let loadingSince: number | undefined
  let route = location.pathname + location.search
  const visible = (element: Element) => {
    const style = getComputedStyle(element)
    return (
      element.getClientRects().length > 0 &&
      style.visibility !== 'hidden' &&
      style.display !== 'none'
    )
  }
  const seconds = (ms: number) => (ms / 1000).toFixed(1)
  // Evidence only; a page closing while it is sent loses nothing the run needs.
  const slow = (message: string) =>
    void window.__firstHourSlow?.(message).catch(() => undefined)
  // The gate asks this at every stage end, and waits for false (#1177).
  window.__firstHourLoading = () => loadingSince !== undefined
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
    for (const found of text.match(
      /\bv?\d{2}\.\d{1,2}\.\d+(?:[-+][\w.-]+)?\b/g,
    ) ?? []) {
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
      if (
        loadingSince !== undefined &&
        performance.now() - loadingSince > slowMs
      )
        slow(
          `${route.split('?')[0]}: left while loading after ${seconds(performance.now() - loadingSince)} s`,
        )
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
        )
      loadingSince = undefined
    } else if (loadingSince === undefined) loadingSince = now
    else if (now - loadingSince > readyMs)
      report(`loading longer than ${readyMs / 1000} s`)
  }
  inspect()
  setInterval(inspect, 100)
}
