// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

/**
 * cn merges class names with clsx (conditional classes) then tailwind-merge
 * (resolves Tailwind conflicts so the last utility wins). Every component in the
 * design system composes classes through this helper.
 */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs))
}

/**
 * slugify derives the URL/DNS-safe slug a name suggests: lowercase, every run of
 * non-alphanumerics collapsed to a single hyphen, no leading/trailing hyphen, at
 * most 63 characters (a DNS label). It is a CONVENIENCE for forms that prefill a
 * slug from a name — the operator can always override it, and the ENGINE remains
 * the authority on what a slug may be (a rejected slug surfaces as its error, we
 * never pre-empt a rule the backend does not have).
 */
export function slugify(name: string): string {
  return name
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63)
}
