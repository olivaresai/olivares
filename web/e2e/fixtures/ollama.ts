// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { createServer } from 'node:http'
import { test as base, expect } from '@playwright/test'

// A labelled model stand-in on loopback. The engine and its installed OpenCode
// tool reach this endpoint themselves; browser/API requests are not intercepted.
export const test = base.extend<{
  ollama: {
    endpoint: string
    response: string
    toolCommand: string
    toolDone: string
  }
}>({
  // Playwright requires destructuring even when a fixture has no dependencies.
  // eslint-disable-next-line no-empty-pattern
  ollama: async ({}, provide) => {
    const response =
      'Local model stand-in: the first session reached its configured endpoint.'
    const toolCommand = 'echo journey-approval'
    const toolDone = 'Local model stand-in: the approved command ran.'
    const model = 'qwen3:8b'
    const server = createServer(async (req, res) => {
      const json = (body: unknown) => {
        res.writeHead(200, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify(body))
      }
      if (req.method === 'GET' && req.url === '/api/tags') {
        json({
          models: [
            {
              name: model,
              model,
              size: 1,
              digest: 'stand-in',
              details: { family: 'qwen3' },
            },
          ],
        })
      } else if (req.method === 'GET' && req.url === '/v1/models') {
        json({ data: [{ id: model, object: 'model' }] })
      } else if (req.method === 'POST' && req.url === '/api/show') {
        req.resume()
        json({
          capabilities: ['completion', 'tools'],
          details: { family: 'qwen3' },
          model_info: {
            'general.architecture': 'qwen3',
            'qwen3.context_length': 32768,
          },
        })
      } else if (req.method === 'POST' && req.url === '/v1/chat/completions') {
        let body = ''
        for await (const chunk of req) body += chunk
        let request: {
          stream?: boolean
          tools?: unknown[]
          messages?: { role: string; content: unknown }[]
        }
        try {
          request = JSON.parse(body)
        } catch {
          res.writeHead(400).end('Invalid JSON')
          return
        }
        const stream = request.stream === true
        const last = request.messages?.at(-1)
        // A turn that names `toolCommand` answers with one shell call, so the tool asks
        // for approval; the turn after the tool's result answers `toolDone`.
        const call =
          last?.role === 'user' &&
          (request.tools?.length ?? 0) > 0 &&
          JSON.stringify(last.content).includes(toolCommand)
            ? {
                index: 0,
                id: 'call_stand_in',
                type: 'function',
                function: {
                  name: 'bash',
                  arguments: JSON.stringify({
                    command: toolCommand,
                    description: 'Print a marker',
                  }),
                },
              }
            : undefined
        const content = last?.role === 'tool' ? toolDone : response
        const completion = { id: 'chatcmpl-stand-in', created: 1, model }
        if (stream) {
          res.writeHead(200, { 'Content-Type': 'text/event-stream' })
          for (const choice of [
            call
              ? {
                  index: 0,
                  delta: { role: 'assistant', tool_calls: [call] },
                  finish_reason: null,
                }
              : {
                  index: 0,
                  delta: { role: 'assistant', content },
                  finish_reason: null,
                },
            {
              index: 0,
              delta: {},
              finish_reason: call ? 'tool_calls' : 'stop',
            },
          ]) {
            res.write(
              `data: ${JSON.stringify({ ...completion, object: 'chat.completion.chunk', choices: [choice] })}\n\n`,
            )
          }
          res.end('data: [DONE]\n\n')
        } else {
          json({
            ...completion,
            object: 'chat.completion',
            choices: [
              {
                index: 0,
                message: call
                  ? { role: 'assistant', content: null, tool_calls: [call] }
                  : { role: 'assistant', content },
                finish_reason: call ? 'tool_calls' : 'stop',
              },
            ],
            usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
          })
        }
      } else {
        res.writeHead(404).end('Unknown stand-in endpoint')
      }
    })
    try {
      await new Promise<void>((resolve, reject) => {
        server.once('error', reject)
        server.listen(0, '127.0.0.1', resolve)
      })
      const address = server.address()
      if (!address || typeof address === 'string')
        throw new Error('No stand-in listener')
      await provide({
        endpoint: `http://127.0.0.1:${address.port}`,
        response,
        toolCommand,
        toolDone,
      })
    } finally {
      server.closeAllConnections()
      await new Promise<void>((resolve, reject) => {
        server.close((error) => (error ? reject(error) : resolve()))
      })
    }
  },
})

export { expect }
