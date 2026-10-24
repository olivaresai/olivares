// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NAME OF A MODEL, said one way wherever the console says it: "Claude Opus 5.5",
// never "claude-opus-5-5" cut to "clau…". The tool's own catalog names its models
// (GET /provider-profiles/{ref}/models, `display_name`) and wins wherever it was read.
// Elsewhere (a header, a card, a row) the id is put in words by one rule; nothing here
// lists a model. The id itself stays on the hover, in the CLI and in the API.
import type { ProfileModelDTO } from './types'

/** Words written in capitals in the vendors' own names. */
const CAPITALS: Readonly<Record<string, string>> = { gpt: 'GPT', glm: 'GLM' }

/** A lowercase id of words, numbers and dots joined by hyphens ("claude-opus-5-5"). */
const READABLE = /^[a-z][a-z0-9.]*(-[a-z0-9.]+)*$/

/** A release date the vendor appends to a pinned id ("-20250929", "-2024-08-06"). */
const DATE_SUFFIX = /-(\d{8}|\d{4}-\d{2}-\d{2})$/

/** A variant the tool appends in brackets ("claude-sonnet-5-5[1m]", the 1M context). */
const VARIANT = /\[([a-z0-9]+)\]$/i

/**
 * A model's name: the one the tool lists for `id`, else the id in words. An id of
 * another shape (a path, a tag, capitals) is returned as it is, never guessed.
 */
export function modelName(
  id: string,
  listed?: readonly Pick<ProfileModelDTO, 'id' | 'display_name'>[],
): string {
  const named = listed?.find((m) => m.id === id)?.display_name?.trim()
  if (named) return named
  const variant = VARIANT.exec(id)
  const base = variant ? id.slice(0, variant.index) : id
  if (!READABLE.test(base)) return id
  const words: string[] = []
  for (const part of base.replace(DATE_SUFFIX, '').split('-')) {
    const prev = words.at(-1)
    // "5-5" is version 5.5; "gpt-5.5" keeps OpenAI's hyphen.
    if (/^\d{1,2}$/.test(part) && prev && /^\d+(\.\d+)*$/.test(prev))
      words[words.length - 1] = `${prev}.${part}`
    else if (prev === 'GPT' && /^\d/.test(part))
      words[words.length - 1] = `GPT-${part}`
    else
      words.push(
        (Object.hasOwn(CAPITALS, part) ? CAPITALS[part] : undefined) ??
          (/^[a-z]{2,}$/.test(part)
            ? part[0]!.toUpperCase() + part.slice(1)
            : part),
      )
  }
  return words.join(' ') + (variant ? ` (${variant[1]!.toUpperCase()})` : '')
}
