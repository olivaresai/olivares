// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { TFunction } from 'i18next'

const DEVICES: [RegExp, string][] = [
  [/iPhone/, 'iPhone'],
  [/iPad/, 'iPad'],
  [/Android/, 'Android'],
  [/CrOS|Chrome ?OS/, 'ChromeOS'],
  [/Mac/, 'Mac'],
  [/Win/, 'Windows'],
  [/Linux/, 'Linux'],
]

/** The kind of device this browser runs on, or '' when it cannot be told. */
export function deviceKind(
  nav: Pick<Navigator, 'userAgent'> | undefined = globalThis.navigator,
): string {
  const hints = nav as { userAgentData?: { platform?: string } } | undefined
  const ua = `${hints?.userAgentData?.platform ?? ''} ${nav?.userAgent ?? ''}`
  return DEVICES.find(([re]) => re.test(ua))?.[1] ?? ''
}

/** The name a new passkey gets when none is typed: the device it was made on. */
export function defaultPasskeyName(
  t: TFunction,
  nav?: Pick<Navigator, 'userAgent'>,
): string {
  const device = deviceKind(nav)
  return device
    ? t('identity:login.passkeys.defaultName', { device })
    : t('identity:login.passkeys.defaultNameNoDevice')
}
