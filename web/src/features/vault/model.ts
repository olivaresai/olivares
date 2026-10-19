// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A vault item is the JSON document the engine seals in a person's vault/<user-id>/<label>
// secret (core/auth/vault_item.go). The list never holds a value: it carries the entry's
// hint, which the engine sets to the item's kind ("login", "login+totp", "note", "totp").
import type { SecretDTO } from '@/features/console/api'

export type VaultKind = 'login' | 'note' | 'totp'

export interface VaultItem {
  type: VaultKind
  username?: string
  password?: string
  url?: string
  note?: string
  /** A base32 secret or the otpauth://totp/ link of a service's QR code. */
  totp?: string
}

/** One row of the list: what the engine tells without opening the item. */
export interface VaultRow {
  /** The full entry name, vault/<user-id>/<label>. */
  name: string
  label: string
  kind: VaultKind
  /** The item carries an authenticator secret (a login with one, or an authenticator item). */
  hasCode: boolean
  description: string
  updatedAt: string
}

export const VAULT_PREFIX = 'vault/'

/** The characters a label may use: the secret store's own name rule. */
export const LABEL_RE = /^[A-Za-z0-9._-]{1,128}$/

export const VAULT_KINDS: readonly VaultKind[] = ['login', 'note', 'totp']

export function isVaultKind(x: unknown): x is VaultKind {
  return VAULT_KINDS.some((kind) => kind === x)
}

export function entryName(userId: string, label: string): string {
  return `${VAULT_PREFIX}${userId}/${label}`
}

/** The rows of the signed-in person's own items, by label. The engine already lists only
 * their own; the prefix check keeps an organization's mcp/ and env/ names out. */
export function vaultRows(
  secrets: readonly SecretDTO[],
  userId: string,
): VaultRow[] {
  const own = `${VAULT_PREFIX}${userId}/`
  return secrets
    .filter((s) => s.name.startsWith(own) && s.name.length > own.length)
    .map((s) => {
      const [kind, ...extras] = s.hint.split('+')
      return {
        name: s.name,
        label: s.name.slice(own.length),
        kind: isVaultKind(kind) ? kind : 'note',
        hasCode: kind === 'totp' || extras.includes('totp'),
        description: s.description,
        updatedAt: s.updated_at,
      }
    })
    .sort((a, b) => a.label.localeCompare(b.label))
}

/** Rows whose label, description or kind contains every word of the query. */
export function findRows(rows: readonly VaultRow[], query: string): VaultRow[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  if (words.length === 0) return [...rows]
  return rows.filter((row) => {
    const text = `${row.label} ${row.description} ${row.kind}`.toLowerCase()
    return words.every((w) => text.includes(w))
  })
}

/** The document the engine seals. Empty fields are left out, as the engine does. */
export function serializeItem(item: VaultItem): string {
  const out: Record<string, string> = { type: item.type }
  for (const key of ['username', 'password', 'url', 'note', 'totp'] as const) {
    const value = item[key]
    if (value) out[key] = value
  }
  return JSON.stringify(out)
}

/** The item a reveal returned, or null when the value is not an item document. */
export function parseItem(value: string): VaultItem | null {
  try {
    const raw: unknown = JSON.parse(value)
    if (typeof raw !== 'object' || raw === null) return null
    const doc = raw as Record<string, unknown>
    if (!isVaultKind(doc.type)) return null
    const item: VaultItem = { type: doc.type }
    for (const key of [
      'username',
      'password',
      'url',
      'note',
      'totp',
    ] as const) {
      const field = doc[key]
      if (typeof field === 'string') item[key] = field
    }
    return item
  } catch {
    return null
  }
}

/** What a quick copy puts on the clipboard: the password of a login, the text of a note. */
export function primaryValue(item: VaultItem): string {
  if (item.type === 'note') return item.note ?? ''
  if (item.type === 'totp') return item.totp ?? ''
  return item.password ?? ''
}

/** A link worth opening: only http(s). Anything else is shown as text. */
export function safeLink(url: string | undefined): string | null {
  if (!url) return null
  try {
    const u = new URL(url.includes('://') ? url : `https://${url}`)
    return u.protocol === 'https:' || u.protocol === 'http:'
      ? u.toString()
      : null
  } catch {
    return null
  }
}
