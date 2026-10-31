// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { mapConversationFrames } from './conversation-frames'
import { threadParts } from './conversation-work'

// Claude Code's own tool inputs: AskUserQuestion, ExitPlanMode and TodoWrite.
const call = (name: string, input: unknown, id: string) =>
  JSON.stringify({
    type: 'assistant',
    message: {
      id: `msg_${id}`,
      role: 'assistant',
      content: [{ type: 'tool_use', id, name, input }],
    },
  })

const QUESTION = {
  questions: [
    {
      question: 'Which database should the report use?',
      header: 'Database',
      multiSelect: false,
      options: [
        { label: 'PostgreSQL', description: 'The shared store' },
        { label: 'SQLite' },
      ],
    },
  ],
}

describe('tool calls that ask, plan or list', () => {
  it('reads a question call as a question card with its options', () => {
    const [item] = mapConversationFrames([
      call('AskUserQuestion', QUESTION, 'q1'),
    ])
    expect(item?.toolCard).toEqual({
      kind: 'question',
      questions: [
        {
          question: 'Which database should the report use?',
          header: 'Database',
          multiSelect: false,
          options: [
            { label: 'PostgreSQL', description: 'The shared store' },
            { label: 'SQLite', description: '' },
          ],
        },
      ],
    })
  })

  it('reads a plan call as a plan card with the plan text', () => {
    const [item] = mapConversationFrames([
      call('ExitPlanMode', { plan: '1. Read\n2. Change' }, 'p1'),
    ])
    expect(item?.toolCard).toEqual({ kind: 'plan', plan: '1. Read\n2. Change' })
  })

  it('reads a todo call as a todo card with each item and its state', () => {
    const todos = [
      {
        content: 'Write test',
        status: 'completed',
        activeForm: 'Writing test',
      },
      { content: 'Fix', status: 'in_progress', activeForm: 'Fixing' },
    ]
    const [item] = mapConversationFrames([call('TodoWrite', { todos }, 't1')])
    expect(item?.toolCard).toEqual({ kind: 'todos', todos })
  })

  it('leaves a call with an unreadable input as an ordinary tool call', () => {
    const [item] = mapConversationFrames([
      call('AskUserQuestion', { questions: 'none' }, 'q2'),
    ])
    expect(item?.toolCard).toBeUndefined()
  })

  it('paints a card between two work rows, never inside one', () => {
    const items = mapConversationFrames([
      call('Read', { file_path: '/a' }, 'r1'),
      call('AskUserQuestion', QUESTION, 'q1'),
      call('Read', { file_path: '/b' }, 'r2'),
    ])
    expect(threadParts(items).map((p) => p.kind)).toEqual([
      'work',
      'card',
      'work',
    ])
  })

  it('paints only the newest todo list as a card', () => {
    const list = (status: string, id: string) =>
      call(
        'TodoWrite',
        { todos: [{ content: 'Fix', status, activeForm: 'Fixing' }] },
        id,
      )
    const items = mapConversationFrames([
      list('pending', 't1'),
      list('in_progress', 't2'),
      list('completed', 't3'),
    ])
    const parts = threadParts(items)
    expect(parts.map((p) => p.kind)).toEqual(['work', 'card'])
    expect(parts[1]?.kind === 'card' && parts[1].item.toolId).toBe('t3')
    expect(threadParts(items, true).map((p) => p.kind)).toEqual(['work'])
  })
})
