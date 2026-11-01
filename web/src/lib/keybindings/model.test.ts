// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE KEYBINDING CONTRACT, driven rule by rule. The three properties the contract
// carries are the three describes below: precedence, unknown contexts, invalid rules.
import { afterEach, describe, expect, it } from 'vitest'
import {
  auditKeybindings,
  chordHint,
  chordKeys,
  chordMatches,
  modKeyName,
  parseChord,
  resolveBinding,
  type KeyRule,
} from './model'
import { COMMAND_GROUP, KEYBINDINGS } from './table'

describe('parseChord', () => {
  it('reads a bare key, and a chord with modifiers', () => {
    expect(parseChord('p')).toEqual({
      key: 'p',
      mod: false,
      shift: false,
      alt: false,
    })
    expect(parseChord('Mod+Shift+K')).toEqual({
      key: 'K',
      mod: true,
      shift: true,
      alt: false,
    })
  })

  it('tolerates spacing, because a table is written by hand', () => {
    expect(parseChord(' Mod + Enter ')?.key).toBe('Enter')
  })

  it.each([
    ['', 'empty'],
    ['+', 'no key'],
    ['Mod+', 'a modifier and nothing to press'],
    ['Mod+Shift', 'the last segment is a modifier'],
    ['Hyper+k', 'a modifier this console does not have'],
  ])('refuses %o (%s)', (keys) => {
    expect(parseChord(keys)).toBeNull()
  })
})

describe('chordMatches', () => {
  const p = parseChord('p')!
  const modK = parseChord('Mod+k')!
  const enter = parseChord('Enter')!

  it('matches a plain key, either case', () => {
    expect(chordMatches(p, { key: 'p' })).toBe(true)
    expect(chordMatches(p, { key: 'P' })).toBe(true)
  })

  it('does NOT fire a plain key when a modifier is held', () => {
    // Otherwise Ctrl+P — print — would pin a session on the way to the dialog.
    expect(chordMatches(p, { key: 'p', ctrlKey: true })).toBe(false)
    expect(chordMatches(p, { key: 'p', metaKey: true })).toBe(false)
    expect(chordMatches(p, { key: 'p', altKey: true })).toBe(false)
  })

  it('treats Meta and Control as the same modifier, so one rule serves both platforms', () => {
    expect(chordMatches(modK, { key: 'k', metaKey: true })).toBe(true)
    expect(chordMatches(modK, { key: 'k', ctrlKey: true })).toBe(true)
    expect(chordMatches(modK, { key: 'k' })).toBe(false)
  })

  it('compares Shift exactly for a NAMED key', () => {
    expect(chordMatches(enter, { key: 'Enter' })).toBe(true)
    expect(chordMatches(enter, { key: 'Enter', shiftKey: true })).toBe(false)
  })

  it('ignores Shift for a printable key, because the character already carries it', () => {
    // `?` is typed WITH Shift on most layouts. A rule demanding shiftKey=false would
    // make it unreachable, and one demanding true would break the layouts where it is
    // not — so the character is the whole comparison.
    const question = parseChord('?')!
    expect(chordMatches(question, { key: '?', shiftKey: true })).toBe(true)
    expect(chordMatches(question, { key: '?' })).toBe(true)
  })

  it('names the physical key of an Alt+letter chord, because Option types another character on macOS', () => {
    // Option+Shift+O types "Ø" and Option+Shift+W "„" on a Mac: `key` alone never matches.
    const openBeside = parseChord('Alt+Shift+o')!
    const mac = { key: 'Ø', code: 'KeyO', altKey: true, shiftKey: true }
    expect(chordMatches(openBeside, mac)).toBe(true)
    expect(chordMatches(openBeside, { ...mac, code: 'KeyW' })).toBe(false)
    expect(chordMatches(openBeside, { ...mac, altKey: false })).toBe(false)
    expect(chordMatches(openBeside, { ...mac, shiftKey: false })).toBe(false)
    // A chord without Alt keeps comparing the character: the physical key of a
    // Dvorak or AZERTY layout is not what the person typed.
    expect(chordMatches(parseChord('p')!, { key: 'x', code: 'KeyP' })).toBe(
      false,
    )
  })

  it('takes the letter the layout typed over its physical key, when it typed one', () => {
    const approve = parseChord('Alt+a')!
    // AZERTY: A sits where QWERTY has Q, and Q where QWERTY has A.
    expect(
      chordMatches(approve, { key: 'a', code: 'KeyQ', altKey: true }),
    ).toBe(true)
    expect(
      chordMatches(approve, { key: 'q', code: 'KeyA', altKey: true }),
    ).toBe(false)
    // Option+A types "å" on a Mac, and a Cyrillic layout types "ф": the physical key.
    for (const key of ['å', 'ф'])
      expect(chordMatches(approve, { key, code: 'KeyA', altKey: true })).toBe(
        true,
      )
  })
})

describe('resolveBinding — last matching rule wins', () => {
  const rules: KeyRule[] = [
    { command: 'general.enter', keys: 'Enter' },
    { command: 'rail.open', keys: 'Enter', when: 'railFocused' },
  ]

  it('takes the LAST match, which is what makes order the precedence', () => {
    expect(resolveBinding(rules, { key: 'Enter' }, { railFocused: true })).toBe(
      'rail.open',
    )
  })

  it('falls back to the general rule when the narrow one does not apply', () => {
    expect(resolveBinding(rules, { key: 'Enter' }, {})).toBe('general.enter')
  })

  it('answers null when nothing matches, rather than guessing', () => {
    expect(resolveBinding(rules, { key: 'q' }, {})).toBeNull()
  })
})

describe('resolveBinding — an unknown context key is FALSE', () => {
  const rules: KeyRule[] = [
    { command: 'rail.pin', keys: 'p', when: 'railFocused' },
  ]

  it('does not fire when nobody supplies the context', () => {
    // THE POINT: a `when` can only narrow. A rule whose condition is never supplied
    // cannot become accidentally global.
    expect(resolveBinding(rules, { key: 'p' }, {})).toBeNull()
    expect(
      resolveBinding(rules, { key: 'p' }, { railFocused: false }),
    ).toBeNull()
    expect(
      resolveBinding(rules, { key: 'p' }, { somethingElse: true }),
    ).toBeNull()
  })

  it('fires when it is supplied and true', () => {
    expect(resolveBinding(rules, { key: 'p' }, { railFocused: true })).toBe(
      'rail.pin',
    )
  })
})

describe('auditKeybindings — invalid rules are ignored and REPORTED, never fatal', () => {
  const rules: KeyRule[] = [
    { command: 'good', keys: 'g' },
    { command: 'bad-chord', keys: 'Hyper+g' },
    { command: '', keys: 'x' },
    { command: 'also-good', keys: 'Mod+g' },
  ]

  it('keeps every rule that can fire', () => {
    expect(auditKeybindings(rules).valid.map((v) => v.rule.command)).toEqual([
      'good',
      'also-good',
    ])
  })

  it('names what it refused and why', () => {
    expect(auditKeybindings(rules).problems).toEqual([
      { rule: rules[1], reason: 'unparsable-chord' },
      { rule: rules[2], reason: 'no-command' },
    ])
  })

  it('leaves the other bindings working — one typo is not an outage', () => {
    expect(resolveBinding(rules, { key: 'g' })).toBe('good')
  })
})

describe('the shipped table', () => {
  it('has no invalid rule', () => {
    // The audit exists so a typo is survivable. It must still be EMPTY here, or the
    // console ships a binding nobody can press and nothing says so.
    expect(auditKeybindings(KEYBINDINGS).problems).toEqual([])
  })

  it('documents every command it declares', () => {
    // The one page that documents the table reads this map. A command with no group
    // would fire and appear nowhere — a keyboard secret, which is the defect a declared
    // table exists to remove.
    for (const rule of KEYBINDINGS)
      expect(COMMAND_GROUP[rule.command], rule.command).toBeDefined()
  })

  it('binds nothing shell-wide that an operator could type into a field', () => {
    // A rule with no `when` fires wherever the shell handler runs. Every one of them
    // must therefore be a chord, or a key the shell only reads when the operator is NOT
    // typing (`/` and `?`), which `GlobalShortcuts` enforces with `isTypingTarget`.
    // (`Mod+b` folded the sidebar rail until the top bar replaced the sidebar, #2544.)
    const shellWide = KEYBINDINGS.filter((r) => r.when === undefined)
    expect(shellWide.map((r) => r.keys).sort()).toEqual(['/', '?', 'Mod+k'])
    // The bare keys among them stay exactly two, and both are guarded by isTypingTarget.
    expect(
      shellWide
        .filter((r) => !r.keys.includes('+'))
        .map((r) => r.keys)
        .sort(),
    ).toEqual(['/', '?'])
  })

  it('narrows Enter to a context, in both places that use it', () => {
    const enters = KEYBINDINGS.filter((r) => r.keys === 'Enter')
    expect(enters).toHaveLength(2)
    for (const rule of enters) expect(rule.when).toBeDefined()
  })
})

describe('Space, spelled out', () => {
  it('parses to the literal space key', () => {
    // A literal space cannot survive splitting on `+` and trimming, so the table spells
    // it. The listbox pattern binds it beside Enter, and a rail row that answered only
    // to Enter would be half the convention.
    expect(parseChord('Space')?.key).toBe(' ')
    expect(chordMatches(parseChord('Space')!, { key: ' ' })).toBe(true)
  })

  it('opens a rail row, exactly as Enter does', () => {
    expect(
      resolveBinding(KEYBINDINGS, { key: ' ' }, { railFocused: true }),
    ).toBe('rail.open')
  })
})

describe('session switching rules', () => {
  it('declares the previous-session chord only when history is available', () => {
    for (const modifier of ['ctrlKey', 'metaKey']) {
      const event = { key: 'Enter', shiftKey: true, [modifier]: true }
      expect(resolveBinding(KEYBINDINGS, event, { sessionHistory: true })).toBe(
        'session.previous',
      )
      expect(resolveBinding(KEYBINDINGS, event, {})).toBeNull()
      expect(
        resolveBinding(KEYBINDINGS, event, { sessionHistory: false }),
      ).toBeNull()
    }
  })

  it.each([1, 2, 3, 4, 5, 6])(
    'narrows digit %s to the empty palette outside fields',
    (digit) => {
      const event = { key: String(digit) }
      expect(resolveBinding(KEYBINDINGS, event, { paletteRecent: true })).toBe(
        `palette.recent${digit}`,
      )
      expect(resolveBinding(KEYBINDINGS, event, {})).toBeNull()
      expect(
        resolveBinding(KEYBINDINGS, event, { paletteRecent: false }),
      ).toBeNull()
      expect(
        resolveBinding(
          KEYBINDINGS,
          { ...event, ctrlKey: true },
          { paletteRecent: true },
        ),
      ).toBeNull()
      const later = [
        ...KEYBINDINGS,
        { command: 'override', keys: String(digit), when: 'paletteRecent' },
      ]
      expect(resolveBinding(later, event, { paletteRecent: true })).toBe(
        'override',
      )
    },
  )
})

describe('the Mod key, named as the platform names it (#3121)', () => {
  const nav = window.navigator as Navigator & {
    userAgentData?: { platform?: string }
  }
  const set = (platform: string, uaPlatform?: string) => {
    Object.defineProperty(nav, 'platform', {
      value: platform,
      configurable: true,
    })
    Object.defineProperty(nav, 'userAgentData', {
      value: uaPlatform === undefined ? undefined : { platform: uaPlatform },
      configurable: true,
    })
  }
  afterEach(() => {
    delete (nav as { platform?: string }).platform
    delete (nav as { userAgentData?: unknown }).userAgentData
  })

  it.each([
    ['Linux x86_64', undefined, 'Ctrl'],
    ['Win32', undefined, 'Ctrl'],
    ['MacIntel', undefined, '⌘'],
    ['', 'macOS', '⌘'],
    ['', 'Linux', 'Ctrl'],
    // An empty client hint falls back to the platform rather than to Ctrl.
    ['MacIntel', '', '⌘'],
  ])('platform %j, client hint %j: %s', (platform, hint, mod) => {
    set(platform, hint)
    expect(modKeyName()).toBe(mod)
  })

  it('prints a chord with the key this platform has', () => {
    set('Linux x86_64')
    expect(chordKeys('Mod+k')).toEqual(['Ctrl', 'k'])
    expect(chordHint('Mod+k')).toBe('Ctrl+K')
    expect(chordHint('Mod+Shift+p')).toBe('Ctrl+Shift+P')
    set('MacIntel')
    expect(chordKeys('Mod+k')).toEqual(['⌘', 'k'])
    expect(chordHint('Mod+k')).toBe('⌘K')
    // Glyphs join; a word keeps its +.
    expect(chordHint('Mod+Shift+p')).toBe('⌘+Shift+P')
  })
})
