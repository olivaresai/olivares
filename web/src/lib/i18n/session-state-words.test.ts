// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE SESSION STATE, ONE WORD. The rail (`nav:shell.rail.state`) names the four states a
// person acts on: Needs you, Working, Idle, Ended. The sessions list, the state chips, the
// composer and the Now queue showed the same states as "Running", "Active" and "Waiting
// for you", so one running session read as two different states on one screen. The rail
// is the source; every other place a session state is spelled must carry its word, in
// every language, so a translation cannot drift after the English is fixed.
import { describe, expect, it } from 'vitest'

import { LANGUAGE_CODES } from './index'

type Tree = { [key: string]: string | Tree }

const lib = import.meta.glob('@/lib/i18n/locales/*/*.json', {
  eager: true,
  import: 'default',
}) as Record<string, Tree>
const feature = import.meta.glob('@/features/*/i18n/*.json', {
  eager: true,
  import: 'default',
}) as Record<string, Tree>

function libFile(locale: string, ns: string): Tree {
  return lib[`/src/lib/i18n/locales/${locale}/${ns}.json`]
}
function featureFile(locale: string, name: string): Tree {
  return feature[`/src/features/${name}/i18n/${locale}.json`]
}
function at(tree: Tree, path: string): string {
  let node: string | Tree = tree
  for (const key of path.split('.')) {
    if (typeof node === 'string') return `<missing ${path}>`
    node = node[key]
  }
  return typeof node === 'string' ? node : `<missing ${path}>`
}

describe.each(LANGUAGE_CODES)('session state words (%s)', (locale) => {
  const nav = libFile(locale, 'nav')
  const common = libFile(locale, 'common')
  const sessions = featureFile(locale, 'sessions')
  const agentops = featureFile(locale, 'agentops')
  const word = {
    need: at(nav, 'shell.rail.state.need'),
    live: at(nav, 'shell.rail.state.live'),
    idle: at(nav, 'shell.rail.state.idle'),
    ended: at(nav, 'shell.rail.state.ended'),
    // The rail's third group is not a state: it holds everything neither asking nor working.
    earlier: at(nav, 'shell.rail.groups.earlier'),
  }

  // [catalog name, key, catalog, the rail word it must carry]
  const spelled: [string, string, Tree, keyof typeof word][] = [
    ['nav', 'shell.rail.groups.needsYou', nav, 'need'],
    ['nav', 'shell.rail.groups.working', nav, 'live'],
    ['nav', 'launcher.attachedWaiting', nav, 'need'],
    ['common', 'ui.status.needsYou', common, 'need'],
    ['common', 'ui.status.working', common, 'live'],
    ['sessions', 'list.group.attention', sessions, 'need'],
    ['sessions', 'list.group.active', sessions, 'live'],
    ['sessions', 'list.group.settled', sessions, 'earlier'],
    ['sessions', 'state.active', sessions, 'live'],
    ['sessions', 'state.idle', sessions, 'idle'],
    ['sessions', 'state.ended', sessions, 'ended'],
    ['sessions', 'runState.running', sessions, 'live'],
    // A run that is idle is waiting for its next turn and the rail lists it under Working
    // (`isLiveRun`: pending, running, idle), so its chip says the rail's word for that group.
    ['sessions', 'runState.idle', sessions, 'live'],
    ['sessions', 'summary.active', sessions, 'live'],
    ['sessions', 'summary.idle', sessions, 'idle'],
    ['agentops', 'state.running', agentops, 'live'],
    ['agentops', 'state.idle', agentops, 'live'],
    ['agentops', 'summary.running', agentops, 'live'],
    ['agentops', 'summary.idle', agentops, 'idle'],
  ]

  // Sentences carry the word inside their own grammar (case, plural): the rail's word,
  // less its inflected ending, must be in the sentence.
  const stem = (w: string) => (w.length > 6 ? w.slice(0, -2) : w).toLowerCase()
  it("the Now tile caption says Idle in the rail's word", () => {
    const caption = at(featureFile(locale, 'home'), 'tiles.sessions.caption')
    expect(caption.toLowerCase()).toContain(stem(word.idle))
  })
  it("the sessions count says Working in the rail's word (English)", () => {
    if (locale !== 'en') return
    expect(at(sessions, 'summary.running')).toBe(
      `{{n}} ${word.live.toLowerCase()}`,
    )
  })

  it.each(spelled)('%s:%s carries the rail word', (_ns, key, tree, state) => {
    expect(at(tree, key)).toBe(word[state])
  })
})
