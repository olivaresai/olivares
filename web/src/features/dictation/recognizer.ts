// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The page's side of the speech recognizer. The model runs in a worker so a transcription
// never freezes the console; the worker and its 70 MB of runtime and model load only on the
// first press of the microphone.
import type { Job, Reply } from './whisper.worker'

let worker: Worker | null = null
let ready: Promise<void> | null = null
let nextId = 0
const waiting = new Map<
  number,
  { resolve: (text: string) => void; reject: (error: Error) => void }
>()

function failAll(error: Error) {
  for (const { reject } of waiting.values()) reject(error)
  waiting.clear()
  worker?.terminate()
  worker = null
  ready = null
}

function start(): Worker {
  const started = new Worker(new URL('./whisper.worker.ts', import.meta.url), {
    type: 'module',
  })
  started.onmessage = ({ data }: MessageEvent<Reply>) => {
    const call = waiting.get(data.id)
    waiting.delete(data.id)
    if (data.error !== undefined) call?.reject(new Error(data.error))
    else call?.resolve(data.text)
  }
  started.onerror = (event) => {
    event.preventDefault()
    failAll(new Error(event.message || 'speech recognizer failed to start'))
  }
  return started
}

function call(job: Job, transfer: Transferable[] = []) {
  worker ??= start()
  const id = ++nextId
  return new Promise<string>((resolve, reject) => {
    waiting.set(id, { resolve, reject })
    worker!.postMessage({ ...job, id }, transfer)
  })
}

/** Load the model once; a failed load is tried again on the next press. */
export function loadRecognizer(): Promise<void> {
  ready ??= call({ type: 'load' }).then(
    () => undefined,
    (error: unknown) => {
      ready = null
      throw error
    },
  )
  return ready
}

/** Transcribe 16 kHz mono samples spoken in `language` (ISO 639-1). The samples move to the worker. */
export function transcribe(
  audio: Float32Array,
  language: string,
): Promise<string> {
  return call({ type: 'transcribe', audio, language }, [audio.buffer])
}
