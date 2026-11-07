// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { PANEL_EXTENSIONS } from '@/features/extensions'
import {
  PERMISSION_CHOICES,
  PERMISSION_MODE,
  PERMISSION_NAME,
} from './session-launch'

/** An optional engine-authored restriction on a named launch permission. */
export interface PermissionRestriction {
  allows: (name: string) => boolean
  /** The sentence shown beside Start. A composition that refuses a name must supply it. */
  reason?: string
  error?: unknown
  retry?: () => void
}
const unrestricted: PermissionRestriction = { allows: () => true }
const useExtension =
  PANEL_EXTENSIONS.usePermissionRestriction ?? (() => unrestricted)
export function usePermissionRestriction(
  enabled = true,
): PermissionRestriction {
  return useExtension(enabled)
}

export function permissionNameForMode(mode: string): string {
  const raw =
    mode === '' ? 'default' : mode === 'dontAsk' ? 'bypassPermissions' : mode
  const choice = PERMISSION_CHOICES.find((c) => PERMISSION_MODE[c] === raw)
  return choice ? PERMISSION_NAME[choice] : ''
}
