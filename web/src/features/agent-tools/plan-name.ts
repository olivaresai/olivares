// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A plan the way its vendor names it, wherever a tool's plan is shown (the profile row and
// the tool's own card). Brand and plan names are the vendor's own: they are not translated.

const VENDOR: Record<string, string> = {
  claude: 'Claude',
  codex: 'ChatGPT',
}

/** pro, max, team, enterprise -> "Claude Max"; plus, pro, team, enterprise -> "ChatGPT Plus".
 * A value no one named yet keeps the vendor and the tool's value with its first letter
 * capitalized, never the raw token. Empty is unknown. */
export function planName(driver: string, raw?: string): string | undefined {
  if (!raw) return undefined
  const plan = raw.charAt(0).toUpperCase() + raw.slice(1)
  const vendor = VENDOR[driver]
  return vendor ? `${vendor} ${plan}` : plan
}
