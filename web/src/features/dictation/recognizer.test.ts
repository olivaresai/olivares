// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The page's side of the recognizer against a fake worker: one worker and one model load for
// the page, a failed load tried again, and a crashed worker failing its pending calls and
// replaced on the next one.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Reply, Request } from './whisper.worker'

class FakeWorker {
  static all: FakeWorker[] = []
  posted: Request[] = []
  onmessage: ((event: MessageEvent<Reply>) => void) | null = null
  onerror: ((event: ErrorEvent) => void) | null = null
  terminated = false
  constructor() {
    FakeWorker.all.push(this)
  }
  postMessage(request: Request) {
    this.posted.push(request)
  }
  terminate() {
    this.terminated = true
  }
  answer(reply: Reply) {
    this.onmessage?.({ data: reply } as MessageEvent<Reply>)
  }
}

let recognizer: typeof import('./recognizer')

beforeEach(async () => {
  FakeWorker.all = []
  vi.stubGlobal('Worker', FakeWorker)
  vi.resetModules()
  recognizer = await import('./recognizer')
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const last = () => FakeWorker.all.at(-1)!

describe('recognizer', () => {
  it('starts one worker and loads the model once for every press', async () => {
    const first = recognizer.loadRecognizer()
    const second = recognizer.loadRecognizer()
    expect(FakeWorker.all).toHaveLength(1)
    expect(last().posted).toEqual([{ type: 'load', id: 1 }])
    last().answer({ id: 1, text: '' })
    await expect(Promise.all([first, second])).resolves.toEqual([
      undefined,
      undefined,
    ])
    await recognizer.loadRecognizer()
    expect(last().posted).toHaveLength(1)
  })

  it('tries a failed load again on the next press', async () => {
    const failed = recognizer.loadRecognizer()
    last().answer({ id: 1, error: 'model 404' })
    await expect(failed).rejects.toThrow('model 404')

    void recognizer.loadRecognizer()
    expect(last().posted.map((r) => r.type)).toEqual(['load', 'load'])
  })

  it('moves the samples to the worker and returns its text', async () => {
    const heard = recognizer.transcribe(new Float32Array([0.5, -0.5]), 'es')
    expect(last().posted[0]).toMatchObject({
      type: 'transcribe',
      language: 'es',
      id: 1,
    })
    last().answer({ id: 1, text: 'hola' })
    await expect(heard).resolves.toBe('hola')
  })

  it('fails what was pending when the worker dies, and starts a new one next time', async () => {
    const pending = recognizer.transcribe(new Float32Array(1), 'en')
    const crashed = last()
    crashed.onerror?.({
      message: 'blocked by CSP',
      preventDefault() {},
    } as ErrorEvent)
    await expect(pending).rejects.toThrow('blocked by CSP')
    expect(crashed.terminated).toBe(true)

    void recognizer.loadRecognizer()
    expect(FakeWorker.all).toHaveLength(2)
  })
})
