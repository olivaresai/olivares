// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** Whisper reads 16 kHz mono samples. */
const SAMPLE_RATE = 16_000

export type Capture = {
  /** Stop recording and return the take as 16 kHz mono samples. */
  stop: () => Promise<Float32Array>
  /** Stop recording and drop the take. */
  cancel: () => void
}

/** Whether this browser can record the microphone and run the recognizer at all. */
export function canCapture(): boolean {
  return (
    typeof navigator !== 'undefined' &&
    typeof navigator.mediaDevices?.getUserMedia === 'function' &&
    typeof MediaRecorder !== 'undefined' &&
    typeof AudioContext !== 'undefined' &&
    typeof Worker !== 'undefined' &&
    typeof WebAssembly !== 'undefined'
  )
}

/**
 * Start recording the microphone. The browser asks for permission here; a refusal rejects
 * with the browser's NotAllowedError.
 */
export async function startCapture(): Promise<Capture> {
  const stream = await navigator.mediaDevices.getUserMedia({ audio: true })
  const release = () => stream.getTracks().forEach((track) => track.stop())
  const chunks: Blob[] = []
  const recorder = new MediaRecorder(stream)
  recorder.ondataavailable = (event) => chunks.push(event.data)
  recorder.start()
  return {
    stop: () =>
      new Promise<Float32Array>((resolve, reject) => {
        const finish = () => {
          release()
          decode(new Blob(chunks)).then(resolve, reject)
        }
        // A recorder the browser already ended (device unplugged, permission revoked)
        // keeps what it heard.
        if (recorder.state === 'inactive') return finish()
        recorder.onstop = finish
        recorder.stop()
      }),
    cancel: () => {
      recorder.onstop = null
      if (recorder.state !== 'inactive') recorder.stop()
      release()
    },
  }
}

/** Decode the recorder's compressed take; the context resamples it to its own rate. */
async function decode(take: Blob): Promise<Float32Array> {
  if (take.size === 0) throw new Error('the microphone recorded nothing')
  const context = new AudioContext({ sampleRate: SAMPLE_RATE })
  try {
    const audio = await context.decodeAudioData(await take.arrayBuffer())
    return audio.getChannelData(0).slice()
  } finally {
    void context.close()
  }
}
