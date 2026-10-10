// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** The name the server gives a download (Content-Disposition), else `fallback`: the console
 * never invents a second name for a file the server and the CLI already name. The value is
 * a quoted string or a token (RFC 6266 section 4.1: filename="a b.tar.gz" or
 * filename=a.tar.gz). Built with the RegExp constructor, not a literal: the export
 * scrubber's lexer has no regex concept, and a literal with an odd number of double quotes
 * desynchronizes its string tracking. */
export function serverFilename(headers: Headers, fallback: string): string {
  const disposition = headers.get('Content-Disposition') ?? ''
  const m = new RegExp('filename=(?:"([^"]+)"|([^";\\s]+))', 'i').exec(
    disposition,
  )
  return m?.[1] ?? m?.[2] ?? fallback
}

/** Save a Blob as a file through the browser's native download: an anchor with the
 * download attribute, clicked once, its object URL released afterwards. */
export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}
