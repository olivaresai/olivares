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
  isInsideList,
  parseChord,
  resolveBinding,
  ruleId,
  validKeyEdits,
  withOverrides,
  type KeyRule,
} from './model'
import { COMMAND_GROUP, KEYBINDINGS, NAV_LEADER } from './table'
import { setKeyOverride } from './effective'
import { useClientSettings } from '@/features/settings/preferences'

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

  it('ignores Shift for a printable key other than a letter, because the character already carries it', () => {
    // `?` is typed WITH Shift on most layouts. A rule demanding shiftKey=false would
    // make it unreachable, and one demanding true would break the layouts where it is
    // not — so the character is the whole comparison.
    const question = parseChord('?')!
    expect(chordMatches(question, { key: '?', shiftKey: true })).toBe(true)
    expect(chordMatches(question, { key: '?' })).toBe(true)
  })

  it("takes Shift as part of a letter's keys, because Shift+P types a capital", () => {
    // Caps Lock types the capital without Shift: that is still `p` (above).
    expect(chordMatches(p, { key: 'P', shiftKey: true })).toBe(false)
    const shiftP = parseChord('Shift+p')!
    expect(chordMatches(shiftP, { key: 'P', shiftKey: true })).toBe(true)
    expect(chordMatches(shiftP, { key: 'p' })).toBe(false)
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
    // `Mod+b` folds the sidebar and `Mod+Alt+b` the session details: chords, never typed.
    const shellWide = KEYBINDINGS.filter((r) => r.when === undefined)
    expect(shellWide.map((r) => r.keys).sort()).toEqual([
      '/',
      '?',
      'Mod+Alt+b',
      'Mod+b',
      'Mod+k',
    ])
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

describe('the Mod key, named as the platform names it', () => {
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
describe('list movement', () => {
  it('binds j/k to moving a list, and only outside one', () => {
    expect(
      resolveBinding(KEYBINDINGS, { key: 'j' }, { outsideList: true }),
    ).toBe('list.next')
    expect(
      resolveBinding(KEYBINDINGS, { key: 'k' }, { outsideList: true }),
    ).toBe('list.previous')
    // A context nobody supplies is false, and outside a field is NOT outside a list.
    expect(resolveBinding(KEYBINDINGS, { key: 'j' }, {})).toBeNull()
    expect(
      resolveBinding(KEYBINDINGS, { key: 'j' }, { outsideField: true }),
    ).toBeNull()
  })

  it('moves the rail with j/k, and the rail rule wins when both contexts are true', () => {
    expect(
      resolveBinding(KEYBINDINGS, { key: 'j' }, { railFocused: true }),
    ).toBe('rail.next')
    expect(
      resolveBinding(KEYBINDINGS, { key: 'k' }, { railFocused: true }),
    ).toBe('rail.previous')
    // The rail is a listbox, so a rail-focused j matches both rules; order decides.
    expect(
      resolveBinding(
        KEYBINDINGS,
        { key: 'j' },
        { railFocused: true, outsideList: true },
      ),
    ).toBe('rail.next')
  })
})

describe('isInsideList — what the shell will not move', () => {
  it('is false for null and for elements outside any list surface', () => {
    expect(isInsideList(null)).toBe(false)
    const button = document.createElement('button')
    document.body.appendChild(button)
    expect(isInsideList(button)).toBe(false)
  })

  it.each(['grid', 'listbox', 'tree', 'tablist'])(
    'is true inside a %s, even for a nested target',
    (role) => {
      const surface = document.createElement('div')
      surface.setAttribute('role', role)
      const cell = document.createElement('div')
      surface.appendChild(cell)
      document.body.appendChild(surface)
      expect(isInsideList(cell)).toBe(true)
      expect(isInsideList(surface)).toBe(true)
    },
  )
})

// A person's own keys (Settings › Keyboard): the declared table with their edits,
// which every handler reads through the one resolver.
describe("resolveBinding — a person's own keys", () => {
  afterEach(() => useClientSettings.setState({ keyOverrides: {} }))

  it('fires on the keys the person chose for a row, and no longer on the old ones', () => {
    setKeyOverride('palette.open|', 'Mod+p')
    expect(resolveBinding(KEYBINDINGS, { key: 'p', ctrlKey: true })).toBe(
      'palette.open',
    )
    expect(resolveBinding(KEYBINDINGS, { key: 'k', ctrlKey: true })).toBeNull()
    setKeyOverride('palette.open|', null)
    expect(resolveBinding(KEYBINDINGS, { key: 'k', ctrlKey: true })).toBe(
      'palette.open',
    )
  })

  it('edits one row in its own context and in its place, so precedence holds', () => {
    const rules: KeyRule[] = [
      { command: 'general.enter', keys: 'Enter' },
      { command: 'rail.open', keys: 'Enter', when: 'railFocused' },
      { command: 'rail.open', keys: 'Space', when: 'railFocused' },
    ]
    const edited = withOverrides(rules, { 'rail.open|railFocused': 'o' })
    expect(edited.map((rule) => `${ruleId(rule)} ${rule.keys}`)).toEqual([
      'general.enter| Enter',
      'rail.open|railFocused o',
    ])
  })

  it('keeps the declared keys when an edit is not a chord', () => {
    const rules: KeyRule[] = [{ command: 'palette.open', keys: 'Mod+k' }]
    expect(withOverrides(rules, { 'palette.open|': 'Mod+Shift' })).toEqual(
      rules,
    )
  })
})

// Settings › Keyboard writes through `setKeyOverride` (effective.ts), which refuses what
// cannot work and says why, as a reason the editor renders.
describe('Settings › Keyboard refuses keys that cannot work', () => {
  const edit = (id: string, keys: string) => setKeyOverride(id, keys)
  afterEach(() =>
    useClientSettings.setState({ keyOverrides: {}, projectScripts: {} }),
  )

  it('refuses keys longer than any chord, and an edit the saved settings cannot hold', () => {
    expect(edit('palette.open|', `Mod+${'x'.repeat(29)}`)).toEqual({
      ok: false,
      reason: 'notAChord',
    })
    // One value on the server holds every setting, the scripts a person added too.
    useClientSettings.setState({
      projectScripts: { big: [{ name: 'big', command: 'x'.repeat(16_000) }] },
    })
    expect(edit('palette.open|', 'Mod+p')).toEqual({
      ok: false,
      reason: 'tooLarge',
    })
    expect(useClientSettings.getState().keyOverrides).toEqual({})
  })

  it('refuses keys another row answers in the same place, and names that row', () => {
    expect(edit('palette.open|', 'Mod+b')).toEqual({
      ok: false,
      reason: 'taken',
      by: { command: 'shell.sidebar', keys: 'Mod+b' },
    })
    expect(edit('palette.open|', 'Mod+p')).toEqual({ ok: true })
  })

  it('refuses keys another row answers where both can apply, and keeps the pairs the table declares', () => {
    // On the approvals queue a bare key is outside a list too: `d` would move and deny.
    expect(edit('list.next|outsideList', 'd')).toEqual({
      ok: false,
      reason: 'taken',
      by: { command: 'approvals.deny', keys: 'd', when: 'approvalQueue' },
    })
    // `j` moves a list and the rail by the table's own precedence: no clash to give back.
    expect(edit('list.next|outsideList', 'Alt+j')).toEqual({ ok: true })
    expect(setKeyOverride('list.next|outsideList', null)).toEqual({ ok: true })
  })

  it('never lets one key decide a request, or fire while a person types', () => {
    expect(edit('approvals.approve|approvalQueue', 'y')).toEqual({
      ok: false,
      reason: 'needsModifier',
    })
    expect(edit('palette.open|', 'p')).toEqual({
      ok: false,
      reason: 'needsModifier',
    })
    expect(edit('rail.pin|railFocused', 'y')).toEqual({ ok: true })
  })

  it('never lets Mod alone decide a request: typing presses it too', () => {
    for (const id of [
      'approvals.approve|approvalQueue',
      'surface.approve|workSurface',
    ])
      expect(edit(id, 'Mod+a')).toEqual({
        ok: false,
        reason: 'needsAltOrShift',
      })
    expect(edit('approvals.deny|approvalQueue', 'Mod+d')).toEqual({
      ok: false,
      reason: 'needsAltOrShift',
    })
    expect(edit('approvals.approve|approvalQueue', 'Alt+y')).toEqual({
      ok: true,
    })
    expect(edit('approvals.deny|approvalQueue', 'Mod+Shift+x')).toEqual({
      ok: true,
    })
  })

  it('gives a row back its keys only while no other row holds them, and names that row', () => {
    const reset = (id: string) => setKeyOverride(id, null)
    expect(edit('palette.open|', 'Mod+p')).toEqual({ ok: true })
    expect(edit('shell.sidebar|', 'Mod+k')).toEqual({ ok: true })
    expect(reset('palette.open|')).toEqual({
      ok: false,
      reason: 'taken',
      by: { command: 'shell.sidebar', keys: 'Mod+k' },
    })
    expect(useClientSettings.getState().keyOverrides).toEqual({
      'palette.open|': 'Mod+p',
      'shell.sidebar|': 'Mod+k',
    })
    expect(reset('shell.sidebar|')).toEqual({ ok: true })
    expect(reset('palette.open|')).toEqual({ ok: true })
    expect(useClientSettings.getState().keyOverrides).toEqual({})
  })

  it('gives every row back its keys at once, a swapped pair too', () => {
    const { resetKeyOverrides } = useClientSettings.getState()
    edit('palette.open|', 'Mod+p')
    edit('shell.sidebar|', 'Mod+k')
    expect(edit('palette.open|', 'Mod+b')).toEqual({ ok: true })
    // Each row's own reset is refused: the other row holds its keys.
    expect(setKeyOverride('palette.open|', null).ok).toBe(false)
    expect(setKeyOverride('shell.sidebar|', null).ok).toBe(false)
    resetKeyOverrides()
    expect(useClientSettings.getState().keyOverrides).toEqual({})
    expect(resolveBinding(KEYBINDINGS, { key: 'k', ctrlKey: true })).toBe(
      'palette.open',
    )
  })

  it('keeps g alone for the go-to keys, which it arms before any row could fire', () => {
    expect(edit('session.new|outsideField', 'g')).toEqual({
      ok: false,
      reason: 'leader',
    })
    expect(edit('rail.pin|railFocused', 'G')).toEqual({
      ok: false,
      reason: 'leader',
    })
    expect(edit('session.new|outsideField', 'Alt+g')).toEqual({ ok: true })
  })

  // The settings keep the edits as stored text; they are read against the table.
  it('reads the stored edits against the table, whatever order they were saved in', () => {
    // A swap saved in the order it was made, the re-edited row last: both work.
    const swapped = { 'shell.sidebar|': 'Mod+k', 'palette.open|': 'Mod+b' }
    expect(validKeyEdits(KEYBINDINGS, swapped, NAV_LEADER)).toEqual(swapped)
    // A row the table no longer has, keys that would decide a request alone, and keys
    // another row's edit, earlier in the table, holds: none of them.
    const stored = {
      'palette.open|': 'Mod+p',
      'palette.close|': 'Mod+n',
      'approvals.deny|approvalQueue': 'x',
      'help.toggle|': 'Mod+p',
    }
    expect(validKeyEdits(KEYBINDINGS, stored, NAV_LEADER)).toEqual({
      'palette.open|': 'Mod+p',
    })
  })

  it('reads back no edit on keys a row takes back when its own edit is dropped', () => {
    // Two rows on the same keys: the later in the table loses its edit and takes its
    // own keys back, Mod+b, which the earlier row's edit holds, so that edit goes too.
    expect(
      validKeyEdits(
        KEYBINDINGS,
        { 'palette.open|': 'Mod+b', 'shell.sidebar|': 'Mod+b' },
        NAV_LEADER,
      ),
    ).toEqual({})
  })

  it('refuses a row the table does not have', () => {
    expect(edit('palette.close|', 'Mod+p')).toEqual({
      ok: false,
      reason: 'unknownRow',
    })
  })
})
