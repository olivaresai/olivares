// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { createElement } from 'react'
import {
  File,
  FileArchive,
  FileBraces,
  FileCode,
  FileCog,
  FileImage,
  FileSpreadsheet,
  FileTerminal,
  FileText,
  type LucideIcon,
} from 'lucide-react'

/** The kind of a file by its extension, drawn from the console's own icon set; a file
 * the table does not name keeps the plain file glyph. */
const KINDS: ReadonlyArray<[LucideIcon, string]> = [
  [
    FileCode,
    'ts tsx js jsx mjs cjs go py rs java kt c h cc cpp hpp cs rb php swift scala lua vue svelte html css scss sql',
  ],
  [FileBraces, 'json jsonc json5'],
  [FileText, 'md mdx txt rst adoc'],
  [FileImage, 'png jpg jpeg gif svg webp ico avif bmp'],
  [FileArchive, 'zip tar gz tgz bz2 xz zst 7z rar'],
  [FileSpreadsheet, 'csv tsv xls xlsx ods'],
  [FileTerminal, 'sh bash zsh fish ps1'],
  [
    FileCog,
    'yaml yml toml ini conf cfg env lock gitignore dockerignore editorconfig',
  ],
]

const BY_EXTENSION = new Map<string, LucideIcon>(
  KINDS.flatMap(([icon, exts]) =>
    exts.split(' ').map((ext) => [ext, icon] as const),
  ),
)

function fileTypeIcon(path: string): LucideIcon {
  const name = path.slice(path.lastIndexOf('/') + 1)
  const dot = name.lastIndexOf('.')
  return (
    // A dotfile (.env, .gitignore) is named by what follows its dot.
    (dot >= 0 && BY_EXTENSION.get(name.slice(dot + 1).toLowerCase())) || File
  )
}

/** A file's kind beside its name: decoration, since the name says it. */
export function FileTypeIcon({
  path,
  className,
}: {
  path: string
  className?: string
}) {
  return createElement(fileTypeIcon(path), { 'aria-hidden': true, className })
}
