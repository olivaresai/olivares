// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** The longest last segment kept whole: a file name, the end of a branch name. */
const TAIL_MAX = 32
/** The characters kept whole when no slash leaves a tail short enough. */
const TAIL_CHARS = 10
/** A value this short or shorter is never cut. */
const CUT_FROM = 20

const segmenter =
  typeof Intl.Segmenter === 'function'
    ? new Intl.Segmenter('en', { granularity: 'grapheme' })
    : undefined

/**
 * Where a path or a branch name is cut: the start may shrink to an ellipsis, the end stays
 * whole. The end is the last segment (`/verdict.md`, `/3135-calm-dense-console`, a trailing
 * slash kept with its folder) when it is short enough to keep; otherwise the last ten
 * characters (whole characters: an emoji is never split in two). A short value is never cut
 * and stays one piece of text. Without Intl.Segmenter no boundary is provably safe, so the
 * whole value stays. Joined, the two parts are always the value.
 */
export function splitMiddle(value: string): [string, string] {
  if (!segmenter) return [value, '']
  const graphemes = (text: string) =>
    Array.from(segmenter.segment(text), (part) => part.segment)
  const characters = graphemes(value)
  if (characters.length <= CUT_FROM) return [value, '']
  const trimmed = value.replace(/[/\\]+$/, '')
  const slash = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'))
  if (slash > 0 && graphemes(value.slice(slash)).length <= TAIL_MAX)
    return [value.slice(0, slash), value.slice(slash)]
  return [
    characters.slice(0, -TAIL_CHARS).join(''),
    characters.slice(-TAIL_CHARS).join(''),
  ]
}
