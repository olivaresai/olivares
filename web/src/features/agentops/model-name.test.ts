// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { modelName } from './model-name'

describe('modelName: one name for a model everywhere the console says it', () => {
  it('says the name the tool lists for the model first', () => {
    const listed = [{ id: 'claude-opus-5-5', display_name: 'Opus 5.5 (1M)' }]
    expect(modelName('claude-opus-5-5', listed)).toBe('Opus 5.5 (1M)')
  })

  it('names a model the tool did not list in words, not as its id', () => {
    expect(modelName('claude-opus-5-5')).toBe('Claude Opus 5.5')
    expect(modelName('claude-haiku-5-5')).toBe('Claude Haiku 5.5')
    expect(modelName('claude-sonnet-4-5-20250929')).toBe('Claude Sonnet 4.5')
    expect(modelName('claude-3-5-sonnet-20241022')).toBe('Claude 3.5 Sonnet')
    expect(modelName('gpt-5.5')).toBe('GPT-5.5')
    expect(modelName('gpt-5.5-codex')).toBe('GPT-5.5 Codex')
    expect(modelName('grok-4.7')).toBe('Grok 4.7')
    expect(modelName('glm-5.3')).toBe('GLM 5.3')
    expect(modelName('gemini-3-pro')).toBe('Gemini 3 Pro')
  })

  it('keeps the variant the tool appends and drops a dated pin', () => {
    expect(modelName('claude-sonnet-5-5[1m]')).toBe('Claude Sonnet 5.5 (1M)')
    expect(modelName('gpt-4o-2024-08-06')).toBe('GPT-4o')
    // A word that is also a property name of every object is still only a word.
    expect(modelName('x-constructor')).toBe('x Constructor')
  })

  it('leaves an id it cannot read as it is', () => {
    for (const id of ['o3', 'qwen3:32b', 'anthropic/claude-opus-5.5', 'X-1'])
      expect(modelName(id)).toBe(id)
  })

  it('ignores an empty listed name and an empty id', () => {
    expect(modelName('glm-5.3', [{ id: 'glm-5.3', display_name: ' ' }])).toBe(
      'GLM 5.3',
    )
    expect(modelName('')).toBe('')
  })
})
