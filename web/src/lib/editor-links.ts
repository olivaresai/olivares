// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A file of a session's folder as a link the person's own editor opens. Only the editors'
// URL handlers are used; nothing is sent to the engine and nothing runs here.
//
//   VS Code    vscode://file/<path>:<line>                          documented (code.visualstudio.com/docs/configure/command-line)
//              vscode://vscode-remote/ssh-remote+<host>/<path>      the documented remote form
//   Zed        zed://file/<path>:<line>, zed://ssh/<host>/<path>    the forms Zed's own URL handler parses
//   JetBrains  jetbrains://<tool>/navigate/reference?project=&path= the Toolbox link (undocumented by JetBrains, widely used)
//              jetbrains://gateway/ssh/environment?...              the documented SSH link; it opens the project, not the file

interface EditorSpec {
  label: string
  /** The JetBrains product code the Toolbox SSH link names; absent for the other editors. */
  product?: string
}

export const EDITORS = {
  vscode: { label: 'VS Code' },
  zed: { label: 'Zed' },
  idea: { label: 'IntelliJ IDEA', product: 'IU' },
  pycharm: { label: 'PyCharm', product: 'PY' },
  goland: { label: 'GoLand', product: 'GO' },
  webstorm: { label: 'WebStorm', product: 'WS' },
  rustrover: { label: 'RustRover', product: 'RR' },
  clion: { label: 'CLion', product: 'CL' },
  phpstorm: { label: 'PhpStorm', product: 'PS' },
  rubymine: { label: 'RubyMine', product: 'RM' },
  rider: { label: 'Rider', product: 'RD' },
} as const satisfies Record<string, EditorSpec>

export type EditorId = keyof typeof EDITORS

export const EDITOR_IDS = Object.keys(EDITORS) as EditorId[]

export interface EditorTarget {
  /** The session's folder: an absolute path on the machine the engine runs on. */
  root: string
  /** A file inside it, relative to it. */
  file: string
  /** The 1-based line to land on. */
  line?: number
}

const LOOPBACK_HOSTS = new Set(['localhost', '127.0.0.1', '::1', '[::1]'])

// [user@]host[:port], as ssh and the editors take it. A character outside this set could
// end the URL's authority or add an option, so it is not a host.
const SSH_TARGET =
  /^(?:([A-Za-z0-9_][A-Za-z0-9._-]*)@)?([A-Za-z0-9][A-Za-z0-9._-]*)(?::([0-9]{1,5}))?$/

/**
 * Where the folder is reached by SSH: what the person typed, else the host the console is
 * served from, else '' when that is this machine and no SSH is needed.
 *
 * ponytail: assumes the console's host is the machine the folder is on. Behind a reverse
 * proxy or in a container, or with an IPv6 address (no link is built), the person types the
 * host in Settings; upgrade when the engine can name the host its folders live on.
 */
export function sshTarget(typed: string, consoleHost: string): string {
  const setting = typed.trim()
  if (setting) return setting
  return LOOPBACK_HOSTS.has(consoleHost) ? '' : consoleHost
}

/** The path segments of an absolute or relative path, or null when it is not a plain one. */
function segments(path: string, absolute: boolean): string[] | null {
  if (path.startsWith('/') !== absolute || path.includes('\\')) return null
  const parts = path.split('/').slice(absolute ? 1 : 0)
  if (parts.length > 0 && parts[parts.length - 1] === '' && absolute)
    parts.pop()
  return parts.every((s) => s !== '' && s !== '.' && s !== '..') ? parts : null
}

const encode = (parts: string[]) => parts.map(encodeURIComponent).join('/')

/**
 * The link that opens `target` in `editor`, over SSH when `ssh` names a host; null when the
 * editor, the path or the host is not something a link can safely carry.
 */
export function editorLink(
  editor: EditorId,
  target: EditorTarget,
  ssh: string,
): string | null {
  if (!Object.hasOwn(EDITORS, editor)) return null
  const root = segments(target.root, true)
  const file = segments(target.file, false)
  if (!root || !file || file.length === 0) return null
  const host = ssh === '' ? null : SSH_TARGET.exec(ssh)
  if (ssh !== '' && !host) return null

  const rootPath = `/${encode(root)}`
  const path = `/${encode([...root, ...file])}`
  const at = target.line && target.line > 0 ? `:${Math.trunc(target.line)}` : ''
  const spec: EditorSpec = EDITORS[editor]

  if (editor === 'vscode') {
    return host
      ? `vscode://vscode-remote/ssh-remote+${ssh}${path}${at}`
      : `vscode://file${path}${at}`
  }
  if (editor === 'zed') {
    return host ? `zed://ssh/${ssh}${path}${at}` : `zed://file${path}${at}`
  }
  if (host) {
    const [, user, name, port] = host
    const query = [
      `h=${name}`,
      user && `u=${user}`,
      port && `p=${port}`,
      'launchIde=true',
      `ideHint=${spec.product}`,
      `projectHint=${rootPath}`,
    ].filter(Boolean)
    return `jetbrains://gateway/ssh/environment?${query.join('&')}`
  }
  const project = encodeURIComponent(root[root.length - 1] ?? '')
  return `jetbrains://${editor}/navigate/reference?project=${project}&path=${encode(file)}${at}`
}

/** The first line of `current` that differs from `committed` (git HEAD), 1-based. */
export function firstChangedLine(
  committed: string | undefined,
  current: string,
): number | undefined {
  if (committed === undefined) return undefined
  const before = committed.split('\n')
  const after = current.split('\n')
  const longest = Math.max(before.length, after.length)
  for (let i = 0; i < longest; i++) {
    if (before[i] !== after[i]) return i + 1
  }
  return undefined
}
