// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The microphone itself, against fakes of the browser's stream, recorder and decoder: every
// way a take can end, failures included, stops the microphone's tracks, and an end the person
// did not ask for is reported.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { startCapture } from './capture'

let tracks: { stop: ReturnType<typeof vi.fn> }[]
let fail: { construct?: Error; start?: Error }
let recorder: FakeRecorder

class FakeRecorder {
  state: 'inactive' | 'recording' = 'inactive'
  ondataavailable: ((event: { data: Blob }) => void) | null = null
  onstop: (() => void) | null = null
  onerror: ((event: Event) => void) | null = null
  constructor() {
    if (fail.construct) throw fail.construct
    // eslint-disable-next-line @typescript-eslint/no-this-alias -- the test drives the instance capture.ts made
    recorder = this
  }
  start() {
    if (fail.start) throw fail.start
    this.state = 'recording'
  }
  stop() {
    this.state = 'inactive'
    this.ondataavailable?.({ data: new Blob([new Uint8Array([1, 2, 3])]) })
    this.onstop?.()
  }
  /** The browser ending the take by itself: the microphone went away. */
  end() {
    this.stop()
  }
  /** The browser failing the take. */
  error(error: Error) {
    this.onerror?.(Object.assign(new Event('error'), { error }))
    this.stop()
  }
}

class FakeAudioContext {
  decodeAudioData = vi.fn().mockResolvedValue({
    getChannelData: () => new Float32Array([0.5, -0.5]),
  })
  close = vi.fn().mockResolvedValue(undefined)
}

beforeEach(() => {
  tracks = [{ stop: vi.fn() }]
  fail = {}
  vi.stubGlobal('navigator', {
    ...navigator,
    mediaDevices: {
      getUserMedia: vi.fn().mockResolvedValue({ getTracks: () => tracks }),
    },
  })
  vi.stubGlobal('MediaRecorder', FakeRecorder)
  vi.stubGlobal('AudioContext', FakeAudioContext)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const released = () => tracks.every((track) => track.stop.mock.calls.length > 0)

describe('startCapture', () => {
  it('releases the microphone when the recorder refuses to start', async () => {
    fail.start = new DOMException(
      'track cannot be recorded',
      'NotSupportedError',
    )
    await expect(startCapture(vi.fn())).rejects.toThrow('cannot be recorded')
    expect(released()).toBe(true)
  })

  it('releases the microphone when no recorder can be made', async () => {
    fail.construct = new DOMException('no codec', 'NotSupportedError')
    await expect(startCapture(vi.fn())).rejects.toThrow('no codec')
    expect(released()).toBe(true)
  })

  it('releases the microphone and reports a recorder error at once', async () => {
    const onInterrupt = vi.fn()
    await startCapture(onInterrupt)
    recorder.error(new Error('encoder failed'))
    expect(released()).toBe(true)
    expect(onInterrupt).toHaveBeenCalledOnce()
    expect(onInterrupt.mock.calls[0][0]).toBeInstanceOf(Error)
    expect(onInterrupt.mock.calls[0][0].message).toBe('encoder failed')
  })

  it('reports a take the browser ended by itself, with no error', async () => {
    const onInterrupt = vi.fn()
    await startCapture(onInterrupt)
    recorder.end()
    expect(released()).toBe(true)
    expect(onInterrupt).toHaveBeenCalledWith()
  })

  it('returns the take on stop and reports nothing', async () => {
    const onInterrupt = vi.fn()
    const take = await startCapture(onInterrupt)
    await expect(take.stop()).resolves.toEqual(new Float32Array([0.5, -0.5]))
    expect(released()).toBe(true)
    expect(onInterrupt).not.toHaveBeenCalled()
  })

  it('reports nothing for a recorder error after the take ended', async () => {
    const onInterrupt = vi.fn()
    const take = await startCapture(onInterrupt)
    take.cancel()
    recorder.onerror?.(
      Object.assign(new Event('error'), { error: new Error('late') }),
    )
    expect(onInterrupt).not.toHaveBeenCalled()
  })

  it('drops the take on cancel and reports nothing', async () => {
    const onInterrupt = vi.fn()
    const take = await startCapture(onInterrupt)
    take.cancel()
    expect(released()).toBe(true)
    expect(recorder.state).toBe('inactive')
    expect(onInterrupt).not.toHaveBeenCalled()
  })
})
