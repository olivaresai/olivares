// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Speech to text inside the browser: OpenAI Whisper tiny (multilingual) on ONNX Runtime Web,
// through Transformers.js. The audio never leaves this worker. Everything it loads, the
// runtime and the model, comes from the engine's own origin (see vite.config.ts): no Hugging
// Face, no CDN, at any time.
import {
  env,
  pipeline,
  type AutomaticSpeechRecognitionPipeline,
} from '@huggingface/transformers'
import ortMjs from 'onnxruntime-web/ort-wasm-simd-threaded.asyncify.mjs?url'
import ortWasm from 'onnxruntime-web/ort-wasm-simd-threaded.asyncify.wasm?url'
import model from './whisper-model.json'

export type Job =
  | { type: 'load' }
  | { type: 'transcribe'; audio: Float32Array; language: string }
export type Request = Job & { id: number }
export type Reply =
  | { id: number; text: string; error?: undefined }
  | { id: number; error: string }

env.allowRemoteModels = false
env.allowLocalModels = true
// The model is served at /assets/voice/<revision>/<file>; its id is its revision.
env.localModelPath = '/assets/voice/'
// The runtime imports its glue by URL; the cached copy would be a blob: import.
env.useWasmCache = false
env.backends.onnx.wasm!.wasmPaths = { mjs: ortMjs, wasm: ortWasm }

type Device = 'webgpu' | 'wasm'
type Recognizer = { device: Device; run: AutomaticSpeechRecognitionPipeline }

/** Below this peak a take is silence; Whisper would invent words for it. */
const SILENCE = 0.01

let recognizer: Promise<Recognizer> | null = null
/** Set once WebGPU fails here, to load or to run: from then on the CPU runs the model. */
let gpuFailed = false

async function load(): Promise<Recognizer> {
  const open = async (device: Device) => ({
    device,
    run: await pipeline('automatic-speech-recognition', model.revision, {
      device,
      dtype: 'q8',
    }),
  })
  const gpu = (navigator as { gpu?: { requestAdapter(): Promise<unknown> } })
    .gpu
  if (!gpuFailed && gpu && (await gpu.requestAdapter().catch(() => null))) {
    try {
      return await open('webgpu')
    } catch (error) {
      gpuFailed = true
      console.warn(
        'dictation: WebGPU could not load the model; using WASM',
        error,
      )
    }
  }
  return open('wasm')
}

async function transcribe(
  audio: Float32Array,
  language: string,
): Promise<string> {
  if (!audio.some((sample) => Math.abs(sample) > SILENCE)) return ''
  recognizer ??= load()
  const { device, run } = await recognizer
  try {
    // Long takes are cut into Whisper's 30 s windows with an overlap.
    // Whisper is told the language: unasked, this library transcribes everything as English.
    const output = await run(audio, {
      language,
      task: 'transcribe',
      chunk_length_s: 30,
      stride_length_s: 5,
    })
    return (Array.isArray(output) ? output[0] : output).text
  } catch (error) {
    if (device !== 'webgpu') throw error
    gpuFailed = true
    console.warn('dictation: WebGPU failed to transcribe; using WASM', error)
    recognizer = null
    void run.dispose()
    return transcribe(audio, language)
  }
}

self.onmessage = async ({ data }: MessageEvent<Request>) => {
  try {
    if (data.type === 'load') {
      recognizer ??= load()
      await recognizer
    }
    const text =
      data.type === 'transcribe'
        ? await transcribe(data.audio, data.language)
        : ''
    self.postMessage({ id: data.id, text } satisfies Reply)
  } catch (error) {
    if (data.type === 'load') recognizer = null
    self.postMessage({
      id: data.id,
      error: error instanceof Error ? error.message : String(error),
    } satisfies Reply)
  }
}
