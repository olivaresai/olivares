// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The sidebar filter's text folding, on its own.
//
// This exists because the first version was a blanket `.replace(/\p{Diacritic}/gu, '')`,
// which is accent-insensitivity ONLY in Latin script. Outside it, the same line is
// corruption — NFD decomposes Russian "й" into "и" + combining breve and Japanese "が"
// into "か" + combining voiced mark, and stripping those returns a DIFFERENT letter. The
// console ships seven languages; the bug made two of them fail to find their own words,
// and it was invisible through the component tests because those type Latin.
//
// The property that matters is stated directly below: every published label must match
// itself. A folding function can only break that by changing letters.
import { describe, expect, it } from 'vitest'
import { fold } from './sidebar'

describe('filter folding', () => {
  it('folds case', () => {
    expect(fold('Sessions')).toBe('sessions')
  })

  it('makes Latin diacritics optional — the reason it exists', () => {
    expect(fold('Sesión')).toBe('sesion')
    expect(fold('Prüfung')).toBe('prufung')
    expect(fold('Identités')).toBe('identites')
    expect(fold('Zustände')).toBe('zustande')
  })

  it('leaves CYRILLIC letters alone', () => {
    // "й" is not "и": NFD + a blanket strip silently equated them.
    expect(fold('Задачи')).toBe('задачи')
    expect(fold('Рабочие процессы')).toBe('рабочие процессы')
    expect(fold('й')).not.toBe('и')
  })

  it('leaves JAPANESE kana alone', () => {
    // が is not か. The dakuten is a combining mark, and stripping it changes the word.
    expect(fold('ワークフロー')).toBe('ワークフロー')
    expect(fold('が')).not.toBe('か')
    expect(fold('タスク')).toBe('タスク')
  })

  it('leaves CJK ideographs alone', () => {
    expect(fold('智能体')).toBe('智能体')
    expect(fold('工作流')).toBe('工作流')
  })

  it('is idempotent, so a folded needle still matches a folded haystack', () => {
    for (const s of ['Sesión', 'Задачи', 'が', '智能体', 'Prüfung'])
      expect(fold(fold(s))).toBe(fold(s))
  })

  it('lets every published label find ITSELF in all seven languages', () => {
    // The end-to-end property. Whatever the folding does, a word must never stop
    // matching itself — that is the only way a filter can be silently useless.
    const labels = [
      'Sesiones',
      'Agentes',
      'Conexiones',
      'Identidades', // es
      'Sitzungen',
      'Identitäten',
      'Zustände', // de
      'Identités',
      'Modèles',
      'Règles',
      'Tâches', // fr
      'セッション',
      'エージェント',
      'ワークフロー',
      'インフラストラクチャ', // ja
      'Сессии',
      'Идентичности',
      'Задачи',
      'Рабочие процессы', // ru
      '会话',
      '智能体',
      '基础设施',
      '工作流', // zh
    ]
    for (const l of labels) expect(fold(l).includes(fold(l))).toBe(true)
    // And distinct labels must not collapse into one another.
    expect(new Set(labels.map(fold)).size).toBe(labels.length)
  })
})
