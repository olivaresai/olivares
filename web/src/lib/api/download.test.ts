// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { downloadBlob } from './download'

const urlStatics = {
  createObjectURL: URL.createObjectURL,
  revokeObjectURL: URL.revokeObjectURL,
}
const revoke = vi.fn()
let clicks: { href: string | null; download: string; attached: boolean }[]

function record(this: HTMLAnchorElement) {
  clicks.push({
    href: this.getAttribute('href'),
    download: this.download,
    attached: this.isConnected,
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  revoke.mockClear()
  clicks = []
  let n = 0
  URL.createObjectURL = () => `blob:download-${++n}`
  URL.revokeObjectURL = revoke
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(record)
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  Object.assign(URL, urlStatics)
})

describe('downloadBlob', () => {
  it('keeps the address until the download has started, then releases it once', () => {
    downloadBlob(new Blob(['{}']), 'source.json')

    expect(clicks).toEqual([
      { href: 'blob:download-1', download: 'source.json', attached: true },
    ])
    expect(document.querySelector('a[download]')).toBeNull()
    expect(revoke).not.toHaveBeenCalled()
    // A same-task or zero-delay release cancels the download in WebKit.
    vi.advanceTimersByTime(1_000)
    expect(revoke).not.toHaveBeenCalled()

    vi.runAllTimers()
    expect(revoke).toHaveBeenCalledTimes(1)
    expect(revoke).toHaveBeenCalledWith('blob:download-1')
  })

  it('two downloads in a row each release their own address', () => {
    downloadBlob(new Blob(['a']), 'a.json')
    downloadBlob(new Blob(['b']), 'b.json')

    expect(clicks.map((c) => [c.href, c.download])).toEqual([
      ['blob:download-1', 'a.json'],
      ['blob:download-2', 'b.json'],
    ])
    expect(revoke).not.toHaveBeenCalled()

    vi.runAllTimers()
    expect(revoke.mock.calls).toEqual([
      ['blob:download-1'],
      ['blob:download-2'],
    ])
  })
})
