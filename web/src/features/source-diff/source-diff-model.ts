// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** One page of a Git host compare. Fields match the read route. */
export interface GitHostDiffFile {
  path: string
  previous_path: string
  status: string
  binary: boolean
  truncated: boolean
  hunks: string[]
}

export interface GitHostDiff {
  source: string
  host: string
  repository: string
  base: string
  head: string
  truncated: boolean
  files: GitHostDiffFile[]
}

export interface GitHostDiffQuery {
  source: string
  host: string
  repository: string
  base: string
  head: string
}

export const DIFF_WINDOW = 200
export const FILE_BOUND = 100

export interface LineCount {
  added: number
  removed: number
  /** True when the file's hunk text was cut, so the numbers are not a total. */
  partial: boolean
}

/** Complete body lines. A hunk that does not end on a newline has a cut last line. */
export function completeLines(hunk: string): string[] {
  const parts = hunk.split('\n')
  if (parts.length > 0) parts.pop()
  return parts.filter((line) => line !== '' && !line.startsWith('\\'))
}

export function countChangedLines(hunks: readonly string[]): {
  added: number
  removed: number
} {
  let added = 0
  let removed = 0
  for (const hunk of hunks) {
    for (const line of completeLines(hunk)) {
      if (line.startsWith('+++') || line.startsWith('---')) continue
      if (line.startsWith('+')) added += 1
      else if (line.startsWith('-')) removed += 1
    }
  }
  return { added, removed }
}

/** Counts for the rail. Empty or binary files have none. A cut file is partial. */
export function lineCount(file: {
  binary: boolean
  truncated: boolean
  hunks: readonly string[]
}): LineCount | null {
  if (file.binary || file.hunks.length === 0) return null
  const counts = countChangedLines(file.hunks)
  if (!file.truncated && counts.added === 0 && counts.removed === 0) return null
  return { ...counts, partial: file.truncated }
}

export function fileTitle(file: {
  path: string
  previous_path: string
}): string {
  if (file.previous_path) return `${file.previous_path} → ${file.path}`
  return file.path
}

export type DiffRow =
  | { kind: 'hunk'; text: string }
  | {
      kind: 'context' | 'added' | 'removed'
      oldLine: number | null
      newLine: number | null
      text: string
      marker: '+' | '−' | ' '
    }

const HUNK_START = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/

export function parseHunks(hunks: readonly string[]): DiffRow[] {
  const rows: DiffRow[] = []
  for (const hunk of hunks) {
    let oldLine = 0
    let newLine = 0
    let inBody = false
    for (const line of completeLines(hunk)) {
      if (line.startsWith('@@')) {
        const match = HUNK_START.exec(line)
        if (match) {
          oldLine = Number(match[1])
          newLine = Number(match[2])
        }
        rows.push({ kind: 'hunk', text: line })
        inBody = true
        continue
      }
      if (!inBody) continue
      if (line.startsWith('+')) {
        rows.push({
          kind: 'added',
          oldLine: null,
          newLine,
          text: line.slice(1),
          marker: '+',
        })
        newLine += 1
      } else if (line.startsWith('-')) {
        rows.push({
          kind: 'removed',
          oldLine,
          newLine: null,
          text: line.slice(1),
          marker: '−',
        })
        oldLine += 1
      } else {
        const text = line.startsWith(' ') ? line.slice(1) : line
        rows.push({
          kind: 'context',
          oldLine,
          newLine,
          text,
          marker: ' ',
        })
        oldLine += 1
        newLine += 1
      }
    }
  }
  return rows
}

/** Which bound the response-level truncated flag is naming. */
export function truncationBound(fileCount: number): 'files' | 'bytes' {
  return fileCount >= FILE_BOUND ? 'files' : 'bytes'
}

export function windowBounds(
  length: number,
  start: number,
  size = DIFF_WINDOW,
): { start: number; end: number } {
  if (length <= size) return { start: 0, end: length }
  const clamped = Math.min(Math.max(0, start), length - size)
  return { start: clamped, end: clamped + size }
}

export function nextHunk(
  rows: readonly DiffRow[],
  from: number,
  direction: 1 | -1,
): number {
  for (
    let index = from + direction;
    index >= 0 && index < rows.length;
    index += direction
  ) {
    if (rows[index]?.kind === 'hunk') return index
  }
  return from
}

export function compareHref(row: {
  name: string
  kind?: string
}): string | null {
  const host = (row.kind ?? '').toLowerCase()
  if (host !== 'github' && host !== 'gitlab') return null
  const params = new URLSearchParams({ source: row.name, host })
  return `/console/sources/diff?${params.toString()}`
}

export const DIFF_SEARCH_KEYS = [
  'source',
  'host',
  'repository',
  'base',
  'head',
] as const

export function queryComplete(query: GitHostDiffQuery): boolean {
  return DIFF_SEARCH_KEYS.every((key) => query[key].trim() !== '')
}
