// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

const wasm = vi.hoisted(() => ({}) as Record<string, unknown>)
const pipeline = vi.hoisted(() => vi.fn().mockResolvedValue(vi.fn()))
vi.mock('@huggingface/transformers', () => ({
  env: { backends: { onnx: { wasm } } },
  pipeline,
}))
vi.mock('onnxruntime-web/ort-wasm-simd-threaded.asyncify.mjs?url', () => ({
  default: '/assets/runtime.mjs',
}))
vi.mock('onnxruntime-web/ort-wasm-simd-threaded.asyncify.wasm?url', () => ({
  default: '/assets/runtime.wasm',
}))

const bytes = new Uint8Array([0, 97, 115, 109]).buffer
let cache: {
  match: ReturnType<typeof vi.fn>
  put: ReturnType<typeof vi.fn>
  keys: ReturnType<typeof vi.fn>
  delete: ReturnType<typeof vi.fn>
}
let fetcher: ReturnType<typeof vi.fn>
let worker: {
  location: { href: string }
  postMessage: ReturnType<typeof vi.fn>
  onmessage?: (event: { data: { type: string; id: number } }) => Promise<void>
}

beforeEach(() => {
  vi.resetModules()
  delete wasm.wasmBinary
  pipeline.mockClear()
  worker = {
    location: { href: 'https://engine.test/assets/worker.js' },
    postMessage: vi.fn(),
  }
  cache = {
    match: vi.fn().mockResolvedValue(undefined),
    put: vi.fn().mockResolvedValue(undefined),
    keys: vi.fn().mockResolvedValue([]),
    delete: vi.fn().mockResolvedValue(true),
  }
  fetcher = vi.fn().mockResolvedValue(new Response(bytes))
  vi.stubGlobal('self', worker)
  vi.stubGlobal('fetch', fetcher)
  vi.stubGlobal('caches', { open: vi.fn().mockResolvedValue(cache) })
})
afterEach(() => vi.unstubAllGlobals())

async function load() {
  await import('./whisper.worker')
  await worker.onmessage!({ data: { type: 'load', id: 1 } })
}

it('loads the runtime from the browser cache without a network', async () => {
  cache.match.mockResolvedValue(new Response(bytes))
  fetcher.mockRejectedValue(new Error('offline'))
  await load()
  expect(wasm.wasmBinary).toEqual(bytes)
  expect(fetcher).not.toHaveBeenCalled()
  expect(worker.postMessage).toHaveBeenCalledWith({ id: 1, text: '' })
})

it('keeps the downloaded runtime for the next offline visit', async () => {
  await load()
  expect(fetcher).toHaveBeenCalledWith(
    'https://engine.test/assets/runtime.wasm',
  )
  expect(cache.put).toHaveBeenCalledOnce()
  expect(await cache.put.mock.calls[0][1].arrayBuffer()).toEqual(bytes)
  expect(wasm.wasmBinary).toEqual(bytes)
})

it('still loads when the browser refuses cache writes', async () => {
  cache.put.mockRejectedValue(new Error('quota'))
  await load()
  expect(wasm.wasmBinary).toEqual(bytes)
  expect(worker.postMessage).toHaveBeenCalledWith({ id: 1, text: '' })
})

it('reports a failed runtime download without starting recognition', async () => {
  fetcher.mockResolvedValue(new Response('', { status: 503 }))
  await load()
  expect(pipeline).not.toHaveBeenCalled()
  expect(worker.postMessage).toHaveBeenCalledWith({
    id: 1,
    error: expect.stringContaining('503'),
  })
})
