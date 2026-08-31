// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

export interface CurlExportInput {
  method: string
  url: string
  headers: Record<string, string>
  body: string | null
}

function shellEscape(s: string): string {
  if (!/[^a-zA-Z0-9_./:@=&?%+-]/.test(s)) return s
  return "'" + s.replace(/'/g, "'\\''") + "'"
}

export function generateCurl(input: CurlExportInput): string {
  const parts: string[] = ['curl']

  if (input.method !== 'GET') {
    parts.push(`-X ${input.method}`)
  }

  for (const [key, value] of Object.entries(input.headers)) {
    if (!value) continue
    parts.push(`-H ${shellEscape(`${key}: ${value}`)}`)
  }

  if (input.body?.trim() && input.method !== 'GET') {
    parts.push(`-d ${shellEscape(input.body)}`)
  }

  parts.push(shellEscape(input.url))

  return parts.join(' \\\n  ')
}
