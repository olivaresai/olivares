// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { clsx, type ClassValue } from 'clsx'
import { extendTailwindMerge } from 'tailwind-merge'

/**
 * THE TYPE SCALE, AS tailwind-merge SEES IT.
 *
 * The seven steps emitted into `@theme` from `web/tokens/primitives.tokens.json`.
 * They are listed here for ONE reason, and it is measured rather than tidy.
 *
 * ⛔ WITHOUT THIS LIST, `cn` DELETED THE SIZE. tailwind-merge 3.6.0 knows Tailwind's
 *    OWN font-size names (`text-xs` … `text-3xl`) and classifies every other `text-*`
 *    as a text COLOUR — so `cn('text-caption text-muted-foreground')` resolved to
 *    `text-muted-foreground` alone, silently, and the element fell back to whatever
 *    it inherited.
 *
 *    Measured on 2026-09-18 against a live engine, on the bundle this branch inherited:
 *    `DeploymentIdentity` asks for `text-caption` (12px/18px) and the signed-out screen
 *    rendered it with `class="text-muted-foreground pb-6 text-center"` at **14px/22px**.
 *    The class was not overridden — it was gone. Four call sites had it at that tip
 *    (`deployment-identity.tsx:42`, `shell-launcher.tsx:101` and `:293`,
 *    `ref-chip.tsx:60`), and every one of them is a component that composes through
 *    `cn`, which is all of them.
 *
 *    That is why this list is not cosmetic: it is the precondition for applying the
 *    design-system layer console-wide. A primitive that spells `text-body` and gets
 *    nothing is worse than one that spells `text-sm` — it reads as done and is not.
 *
 * ⚠ A STEP ADDED TO THE TOKENS AND NOT TO THIS LIST IS THE SAME DEFECT AGAIN, so
 *   `lib/utils.test.ts` reads the token source and fails when the two disagree. The
 *   list is code rather than a build-time read of the JSON because `cn` runs on every
 *   render and must not carry a parser.
 */
export const TYPE_SCALE_STEPS = [
  'display-lg',
  'display',
  'title',
  'heading',
  'body',
  'caption',
  'overline',
] as const

/** tailwind-merge, taught the seven steps so they resolve as FONT SIZES. */
const twMerge = extendTailwindMerge({
  extend: {
    classGroups: {
      // Extending — not replacing — keeps `text-xs` … `text-3xl` in the same group,
      // so a token and a raw size still resolve against each other and the last one
      // wins. A screen that overrides a primitive's `text-body` with `text-xs` gets
      // exactly one size, which is the whole contract of this helper.
      'font-size': [{ text: [...TYPE_SCALE_STEPS] }],
    },
  },
})

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
