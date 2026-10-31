// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { editorLink, firstChangedLine, sshTarget } from './editor-links'

const root = '/srv/example/project'

describe('editorLink — a file on this machine', () => {
  it('builds the documented VS Code file URL with the line', () => {
    expect(editorLink('vscode', { root, file: 'src/a.ts', line: 12 }, '')).toBe(
      'vscode://file/srv/example/project/src/a.ts:12',
    )
  })

  it('encodes each path segment so a space or # cannot end the path', () => {
    expect(editorLink('vscode', { root, file: 'my docs/a#1.md' }, '')).toBe(
      'vscode://file/srv/example/project/my%20docs/a%231.md',
    )
  })

  it('builds the Zed file URL the editor itself parses', () => {
    expect(editorLink('zed', { root, file: 'src/a.ts', line: 3 }, '')).toBe(
      'zed://file/srv/example/project/src/a.ts:3',
    )
  })

  it('builds the JetBrains Toolbox navigate URL: project by folder name, path relative to it', () => {
    expect(
      editorLink('goland', { root, file: 'cmd/main.go', line: 7 }, ''),
    ).toBe(
      'jetbrains://goland/navigate/reference?project=project&path=cmd/main.go:7',
    )
  })
})

describe('editorLink — a workspace on another machine', () => {
  it('uses the VS Code remote URL with the ssh-remote authority', () => {
    expect(
      editorLink('vscode', { root, file: 'src/a.ts', line: 12 }, 'me@devbox'),
    ).toBe(
      'vscode://vscode-remote/ssh-remote+me@devbox/srv/example/project/src/a.ts:12',
    )
  })

  it('uses the Zed ssh URL', () => {
    expect(editorLink('zed', { root, file: 'a.ts' }, 'me@devbox:2222')).toBe(
      'zed://ssh/me@devbox:2222/srv/example/project/a.ts',
    )
  })

  it('opens the project over SSH in a JetBrains IDE with the documented Toolbox link', () => {
    expect(
      editorLink('goland', { root, file: 'cmd/main.go' }, 'me@devbox:2222'),
    ).toBe(
      'jetbrains://gateway/ssh/environment?h=devbox&u=me&p=2222&launchIde=true&ideHint=GO&projectHint=/srv/example/project',
    )
  })

  it('leaves the user out of the JetBrains link when none is given', () => {
    expect(editorLink('idea', { root, file: 'a.ts' }, 'devbox')).toBe(
      'jetbrains://gateway/ssh/environment?h=devbox&launchIde=true&ideHint=IU&projectHint=/srv/example/project',
    )
  })
})

describe('editorLink — what it refuses', () => {
  it.each([
    ['a file that climbs out of the folder', { root, file: '../etc/passwd' }],
    ['an absolute file path', { root, file: '/etc/passwd' }],
    ['a backslash path', { root, file: 'a\\b' }],
    ['a root that is not absolute', { root: 'project', file: 'a.ts' }],
    ['a root that climbs', { root: '/srv/../etc', file: 'a.ts' }],
    ['an empty path segment', { root, file: 'a//b' }],
  ])('returns null for %s', (_name, target) => {
    expect(editorLink('vscode', target, '')).toBeNull()
  })

  it.each([
    'dev box',
    'a/b',
    'host?x=1',
    'host#frag',
    '-oProxyCommand=x',
    'me@',
  ])('returns null for the SSH host %j', (host) => {
    expect(editorLink('vscode', { root, file: 'a.ts' }, host)).toBeNull()
    expect(editorLink('goland', { root, file: 'a.ts' }, host)).toBeNull()
  })
})

it('returns null for an editor it does not know', () => {
  expect(editorLink('emacs' as never, { root, file: 'a.ts' }, '')).toBeNull()
})

describe('sshTarget', () => {
  it('is empty when the console is served from this machine', () => {
    for (const host of ['localhost', '127.0.0.1', '[::1]', '::1']) {
      expect(sshTarget('', host)).toBe('')
    }
  })

  it('is the console host when the console is served from another machine', () => {
    expect(sshTarget('', 'devbox.example.net')).toBe('devbox.example.net')
  })

  it('prefers what the person typed, even on this machine', () => {
    expect(sshTarget('  me@devbox ', 'localhost')).toBe('me@devbox')
  })
})

describe('firstChangedLine', () => {
  it('is the first line that differs from HEAD', () => {
    expect(firstChangedLine('a\nb\nc\n', 'a\nB\nc\n')).toBe(2)
  })

  it('is the first appended line when the agent only added lines', () => {
    expect(firstChangedLine('a\nb\n', 'a\nb\nc\n')).toBe(3)
  })

  it('is undefined when nothing differs or there is no HEAD to compare', () => {
    expect(firstChangedLine('a\n', 'a\n')).toBeUndefined()
    expect(firstChangedLine(undefined, 'a\n')).toBeUndefined()
  })
})
