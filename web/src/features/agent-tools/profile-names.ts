// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The name a new profile (a provider account) starts with. The engine is the authority
// (modules/sessions/accountname): it checks the shape and the uniqueness, and its answer
// wins. This only fills the field in with the name the engine would pick, so the person
// sees it before pressing Create.

import { toolName } from '@/features/agentops/tool-names'

const MAX_LENGTH = 32
const SHAPE = /^[a-z][a-z0-9-]*$/

/** A name the engine accepts: lowercase ASCII, a letter first, then letters, digits or
 * dashes, at most 32 characters. Checked exactly as typed. */
export function isProfileName(name: string): boolean {
  return name.length <= MAX_LENGTH && SHAPE.test(name)
}

/** n >= 1 in bijective base 26 over a to z: 1 is "a", 26 is "z", 27 is "aa". */
function letters(n: number): string {
  let out = ''
  for (let i = n; i > 0; i = Math.floor((i - 1) / 26))
    out = String.fromCharCode(97 + ((i - 1) % 26)) + out
  return out
}

/**
 * The first name of the tool's sequence that is not taken: the tool key, then
 * `<key>-b`, `-c` ... `-z`, `-aa`. A name is unique across every tool and every state,
 * so `taken` holds every account name the engine returned. '' when the key cannot seed a
 * name (the engine refuses it) or no name fits in 32 characters.
 */
export function nextFreeName(stem: string, taken: ReadonlySet<string>): string {
  if (!isProfileName(stem)) return ''
  for (let n = 1; n <= taken.size + 1; n++) {
    const name = n === 1 ? stem : `${stem}-${letters(n)}`
    if (name.length > MAX_LENGTH) break
    if (!taken.has(name)) return name
  }
  return ''
}

/**
 * What a tool's profile names start from: the first word of its product name (Claude Code
 * is claude, Gemini CLI is gemini), else the tool key. The engine is the authority and
 * takes any valid name; this is the one the person sees filled in.
 */
export function profileStem(driver: string): string {
  const word = toolName(driver).split(' ')[0].toLowerCase()
  return isProfileName(word) ? word : driver
}
