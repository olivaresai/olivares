// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { load, JSON_SCHEMA } from 'js-yaml'
import { agentOpsApi } from '@/features/agentops/api'
import {
  useClientSettings,
  type AddedScript,
} from '@/features/settings/preferences'
import { ApiError } from '@/lib/api/errors'

export interface ProjectScript {
  name: string
  source: string
  command: string
}

const object = (value: unknown): Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {}
const quote = (value: string) => "'" + value.replaceAll("'", "'\\''") + "'"

/**
 * Read declarations only: even `make -qp` and `task --list` can execute project code.
 * Given the names at the top of the folder (`topLevelFiles`), only the declarations it
 * holds are read; a file it does not hold would answer 404, which the browser logs.
 */
export async function projectScripts(
  workspaceRef: string,
  tenant: string | null,
  signal: AbortSignal,
  present?: ReadonlySet<string>,
): Promise<ProjectScript[]> {
  const read = async (path: string) => {
    if (present && !present.has(path)) return null
    try {
      const file = await agentOpsApi.readFile(workspaceRef, path, undefined, {
        tenant,
        signal,
      })
      if (file.encoding !== 'utf-8' || file.truncated) throw new Error(path)
      return file.content
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) return null
      throw err
    }
  }
  const [pkg, ...taskfiles] = await Promise.all([
    read('package.json'),
    read('Taskfile.yml'),
    read('Taskfile.yaml'),
    read('taskfile.yml'),
    read('taskfile.yaml'),
  ])
  const scripts: ProjectScript[] = []
  if (pkg !== null) {
    const manifest = object(JSON.parse(pkg))
    const manager =
      typeof manifest.packageManager === 'string'
        ? manifest.packageManager.split('@')[0]
        : 'npm'
    const runner = ['npm', 'pnpm', 'yarn', 'bun'].includes(manager)
      ? manager
      : 'npm'
    for (const [name, command] of Object.entries(object(manifest.scripts))) {
      if (typeof command === 'string' && name && !/\p{Cc}/u.test(name))
        scripts.push({
          name,
          source: 'package.json',
          command: `${runner} run ${runner === 'npm' ? '-- ' : ''}${quote(name)}`,
        })
    }
  }
  const taskIndex = taskfiles.findIndex((text) => text !== null)
  if (taskIndex >= 0) {
    const source = [
      'Taskfile.yml',
      'Taskfile.yaml',
      'taskfile.yml',
      'taskfile.yaml',
    ][taskIndex]
    const tasks = object(
      object(load(taskfiles[taskIndex]!, { schema: JSON_SCHEMA })).tasks,
    )
    for (const [name, task] of Object.entries(tasks)) {
      if (
        name &&
        !name.startsWith('-') &&
        !/[\p{Cc}*?]/u.test(name) &&
        object(task).internal !== true
      )
        scripts.push({
          name,
          source,
          command: `task --taskfile ${quote(source)} ${quote(name)}`,
        })
    }
  }
  // Only literal targets in the root makefile; includes, patterns and computed targets
  // remain Make's responsibility and are never expanded during discovery.
  for (const source of ['GNUmakefile', 'makefile', 'Makefile']) {
    const text = await read(source)
    if (text === null) continue
    const names = new Set<string>()
    for (const line of text.split('\n')) {
      const rule = /^([A-Za-z0-9_./ -]+):(?!=)/.exec(line)
      if (!rule) continue
      for (const name of rule[1].trim().split(/\s+/)) {
        if (name && !name.startsWith('.') && !name.startsWith('-'))
          names.add(name)
      }
    }
    for (const name of names)
      scripts.push({
        name,
        source,
        command: `make -f ${quote(source)} -- ${quote(name)}`,
      })
    break
  }
  return scripts
}

/** The names of the files at the top of a folder, every page of its listing. */
export async function topLevelFiles(
  workspaceRef: string,
  tenant: string | null,
  signal: AbortSignal,
): Promise<Set<string>> {
  const names = new Set<string>()
  let cursor: string | undefined
  do {
    const page = await agentOpsApi.listFiles(
      workspaceRef,
      { cursor },
      { tenant, signal },
    )
    for (const entry of page.entries)
      if (entry.type !== 'dir') names.add(entry.name)
    cursor = page.has_more ? page.cursor : undefined
  } while (cursor)
  return names
}

const NONE: AddedScript[] = []

/** The scripts this person added to the folder (kept with their settings), run as typed. */
export function useAddedScripts(workspaceRef: string): AddedScript[] {
  return useClientSettings((s) => s.projectScripts[workspaceRef] ?? NONE)
}
