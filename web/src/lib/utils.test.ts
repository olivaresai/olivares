// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// `cn` MUST KNOW THE TYPE SCALE, and this file is the reason it does.
//
// ⛔ THE DEFECT THESE CASES PIN, measured on 2026-09-18 against a live engine and the
//    bundle this branch inherited, NOT reasoned from the library's documentation:
//    `DeploymentIdentity` composes `cn('text-caption text-muted-foreground', className)`,
//    and the signed-out screen rendered
//        class="text-muted-foreground pb-6 text-center"   font-size 14px, line-height 22px
//    with `text-caption` (12px/18px) ABSENT. tailwind-merge 3.6.0 classifies any
//    `text-<name>` it does not recognise as a text COLOUR, so the size lost to the colour
//    that followed it — silently, with no build warning and no runtime error.
//
// ⇒ A SIZE THAT DISAPPEARS IS WORSE THAN ONE THAT WAS NEVER APPLIED: the source says
//   `text-caption`, every reader believes the screen is on the ladder, and it is not.
//   That is why the console-wide type layer could not land before this fix.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { cn, TYPE_SCALE_STEPS, slugify } from './utils'

describe('cn and the type scale', () => {
  it('keeps a ladder step when a text colour follows it — the measured defect', () => {
    // Every one of these is a call site that existed at this branch's base.
    expect(cn('text-caption text-muted-foreground')).toBe(
      'text-caption text-muted-foreground',
    )
    expect(cn('text-body text-muted-foreground')).toBe(
      'text-body text-muted-foreground',
    )
    expect(cn('font-display text-display text-foreground')).toBe(
      'font-display text-display text-foreground',
    )
    expect(cn('text-caption text-danger')).toBe('text-caption text-danger')
  })

  it('keeps every step of the ladder, not just the ones a call site happens to use', () => {
    for (const step of TYPE_SCALE_STEPS) {
      expect(cn(`text-${step} text-muted-foreground`)).toBe(
        `text-${step} text-muted-foreground`,
      )
    }
  })

  it('resolves two sizes to one, whichever family each comes from', () => {
    // The point of extending rather than replacing the group: a screen that overrides a
    // primitive's ladder step gets ONE size, and the last one wins — the contract `cn`
    // has always had for Tailwind's own sizes.
    expect(cn('text-body', 'text-caption')).toBe('text-caption')
    expect(cn('text-body', 'text-xs')).toBe('text-xs')
    expect(cn('text-sm', 'text-caption')).toBe('text-caption')
    expect(cn('text-caption', 'text-body')).toBe('text-body')
  })

  it('still resolves two colours to one, and does not confuse a colour for a size', () => {
    expect(cn('text-foreground', 'text-muted-foreground')).toBe(
      'text-muted-foreground',
    )
    // A colour and a size are different decisions and must not cancel each other.
    expect(cn('text-danger', 'text-caption')).toBe('text-danger text-caption')
  })

  it('lists exactly the steps the token source emits', () => {
    // A step added to the tokens and not to TYPE_SCALE_STEPS is the same silent-deletion
    // defect again, on the new step. The token file is the authority.
    const tokens = JSON.parse(
      readFileSync(
        resolve(__dirname, '../../tokens/primitives.tokens.json'),
        'utf8',
      ),
    ) as { type: Record<string, unknown> }
    const emitted = Object.keys(tokens.type)
      .filter((k) => k.startsWith('text-') && !k.includes('--'))
      .map((k) => k.slice('text-'.length))
      .sort()
    expect([...TYPE_SCALE_STEPS].sort()).toEqual(emitted)
  })
})

describe('slugify', () => {
  it('derives a DNS-safe label', () => {
    expect(slugify('  My New Workspace! ')).toBe('my-new-workspace')
    expect(slugify('a'.repeat(80))).toHaveLength(63)
  })
})
