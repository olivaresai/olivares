// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CLIPBOARD_CLEAR_MS, copySecret } from './clipboard'

// A clipboard the test can read and overwrite, as another application would.
function fakeClipboard(
  opts: { canRead?: boolean; refuseWrite?: boolean } = {},
) {
  const state = { text: '' }
  const clipboard = {
    writeText: vi.fn(async (text: string) => {
      if (opts.refuseWrite) throw new Error('NotAllowedError')
      state.text = text
    }),
    readText:
      opts.canRead === false ? undefined : vi.fn(async () => state.text),
  }
  Object.defineProperty(navigator, 'clipboard', {
    value: clipboard,
    configurable: true,
  })
  return { state, clipboard }
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('copySecret', () => {
  it('copies, then clears the clipboard once the time is up', async () => {
    const { state } = fakeClipboard()
    expect(await copySecret('pw-1')).toBe(true)
    expect(state.text).toBe('pw-1')
    await vi.advanceTimersByTimeAsync(CLIPBOARD_CLEAR_MS - 1)
    expect(state.text).toBe('pw-1')
    await vi.advanceTimersByTimeAsync(1)
    expect(state.text).toBe('')
  })

  it('leaves alone what the person copied since', async () => {
    const { state } = fakeClipboard()
    await copySecret('pw-2')
    state.text = 'something the person copied elsewhere'
    await vi.advanceTimersByTimeAsync(CLIPBOARD_CLEAR_MS)
    expect(state.text).toBe('something the person copied elsewhere')
  })

  it('clears anyway when the browser does not let a page read the clipboard', async () => {
    const { state } = fakeClipboard({ canRead: false })
    await copySecret('pw-3')
    await vi.advanceTimersByTimeAsync(CLIPBOARD_CLEAR_MS)
    expect(state.text).toBe('')
  })

  it('keeps one timer: the latest copy is the one that is cleared, and on time', async () => {
    const { state, clipboard } = fakeClipboard()
    await copySecret('first')
    await vi.advanceTimersByTimeAsync(20_000)
    await copySecret('second')
    await vi.advanceTimersByTimeAsync(CLIPBOARD_CLEAR_MS - 1)
    expect(state.text).toBe('second')
    await vi.advanceTimersByTimeAsync(1)
    expect(state.text).toBe('')
    // The first copy's timer was cancelled: only the clear of the second was written.
    expect(clipboard.writeText.mock.calls.map((c) => c[0])).toEqual([
      'first',
      'second',
      '',
    ])
  })

  it('reports false, and sets no timer, when the browser refuses the copy', async () => {
    const { clipboard } = fakeClipboard({ refuseWrite: true })
    expect(await copySecret('pw-4')).toBe(false)
    await vi.advanceTimersByTimeAsync(CLIPBOARD_CLEAR_MS)
    expect(clipboard.writeText).toHaveBeenCalledTimes(1)
  })

  it('tries the clear again when the window is back after a refused one', async () => {
    const { state, clipboard } = fakeClipboard()
    await copySecret('pw-6')
    clipboard.writeText.mockImplementationOnce(async () => {
      throw new Error('DocumentNotFocused')
    })
    await vi.advanceTimersByTimeAsync(CLIPBOARD_CLEAR_MS)
    expect(state.text).toBe('pw-6') // the clear was refused
    window.dispatchEvent(new Event('focus'))
    await vi.advanceTimersByTimeAsync(0)
    expect(state.text).toBe('')
  })

  it('still clears on leaving the page after the timer fired and the clear was refused', async () => {
    const { state, clipboard } = fakeClipboard()
    await copySecret('pw-8')
    clipboard.writeText.mockImplementationOnce(async () => {
      throw new Error('DocumentNotFocused')
    })
    await vi.advanceTimersByTimeAsync(CLIPBOARD_CLEAR_MS)
    expect(state.text).toBe('pw-8')
    window.dispatchEvent(new Event('pagehide'))
    await vi.advanceTimersByTimeAsync(0)
    expect(state.text).toBe('')
  })

  it('never lets the retry of an old clear wipe a newer copy', async () => {
    const { state, clipboard } = fakeClipboard()
    await copySecret('old-secret')
    clipboard.writeText.mockImplementationOnce(async () => {
      throw new Error('DocumentNotFocused')
    })
    await vi.advanceTimersByTimeAsync(CLIPBOARD_CLEAR_MS) // the clear is refused; a retry waits
    await copySecret('new-secret')
    window.dispatchEvent(new Event('focus')) // the old retry must be gone
    await vi.advanceTimersByTimeAsync(0)
    expect(state.text).toBe('new-secret')
    await vi.advanceTimersByTimeAsync(CLIPBOARD_CLEAR_MS)
    expect(state.text).toBe('') // the new copy is cleared on its own time
  })

  it('never lets a slow read for an old copy strand a newer copy on the clipboard', async () => {
    const { state } = fakeClipboard()
    await copySecret('first')
    let answer!: (text: string) => void
    vi.spyOn(navigator.clipboard, 'readText').mockImplementationOnce(
      () => new Promise<string>((resolve) => (answer = resolve)),
    )
    await vi.advanceTimersByTimeAsync(CLIPBOARD_CLEAR_MS) // the clear of 'first' waits on its read
    await copySecret('second') // a newer copy while that read is pending
    answer('second') // the read answers with the newer copy
    await vi.advanceTimersByTimeAsync(0)
    expect(state.text).toBe('second') // the old clear leaves it alone
    await vi.advanceTimersByTimeAsync(CLIPBOARD_CLEAR_MS)
    expect(state.text).toBe('') // and the newer copy is still cleared on its own time
  })

  it('does not blindly clear on a late retry when the clipboard cannot be read', async () => {
    const { state, clipboard } = fakeClipboard({ canRead: false })
    await copySecret('pw-9')
    clipboard.writeText.mockImplementationOnce(async () => {
      throw new Error('DocumentNotFocused')
    })
    await vi.advanceTimersByTimeAsync(CLIPBOARD_CLEAR_MS)
    state.text = 'something else the person copied later'
    window.dispatchEvent(new Event('focus'))
    await vi.advanceTimersByTimeAsync(0)
    expect(state.text).toBe('something else the person copied later')
  })

  it('clears when the page is left before the time is up', async () => {
    const { state } = fakeClipboard()
    await copySecret('pw-7')
    window.dispatchEvent(new Event('pagehide'))
    await vi.advanceTimersByTimeAsync(0)
    expect(state.text).toBe('')
  })

  it('reports false where there is no clipboard at all', async () => {
    Object.defineProperty(navigator, 'clipboard', {
      value: undefined,
      configurable: true,
    })
    expect(await copySecret('pw-5')).toBe(false)
  })
})
