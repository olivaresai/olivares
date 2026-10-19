// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Images attached to a message (#2158) from the composer: paste, drop and the picker
// store the image with the session as it is attached, the message carries the ids, and
// everything the engine refuses is said at that moment, before a message is sent.
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  grants: [] as { tenant: string; tenant_name?: string }[],
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  listWorkspaces: vi.fn(),
  input: vi.fn(),
  inputText: vi.fn(),
  inputWithAttachments: vi.fn(),
  uploadAttachment: vi.fn(),
  getRun: vi.fn(),
  profileModels: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))

const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('@/components/ui/toaster', () => ({ toast: toasts }))

import { ApiError, parseErrorEnvelope } from '@/lib/api/errors'
import type { RunDTO } from '@/features/agentops/types'
import { MAX_ATTACHMENTS } from '@/features/agentops/attachments'
import { WorkComposer } from './work-composer'

const RUN: RunDTO = {
  run_ref: 'run_live',
  name: 'nightly',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
  provider_driver: 'claude',
  provider_profile_ref: 'ppf_team',
}

const PNG = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]
const pngFile = (name = 'shot.png') =>
  new File([new Uint8Array(PNG)], name, { type: 'image/png' })
const PNG_BASE64 = btoa(String.fromCharCode(...PNG))

let n = 0
beforeEach(() => {
  vi.clearAllMocks()
  n = 0
  auth.activeTenant = 'tnt-demo'
  auth.can = () => true
  api.listProfiles.mockResolvedValue({ items: [], has_more: false })
  api.listWorkspaces.mockResolvedValue({ items: [], has_more: false })
  api.input.mockResolvedValue({ accepted: true })
  api.inputText.mockResolvedValue({ accepted: true })
  api.inputWithAttachments.mockResolvedValue({ accepted: true })
  api.uploadAttachment.mockImplementation(
    async (_run: string, body: { media_type: string }) => ({
      id: `att_${++n}`,
      media_type: body.media_type,
      size_bytes: PNG.length,
    }),
  )
  api.profileModels.mockResolvedValue({ models: [], state: 'fresh' })
})

function mount() {
  renderIntel(<WorkComposer attached={{ run: RUN, group: 'active' }} />)
  return screen.findByTestId('launcher-input')
}

const paste = (field: HTMLElement, ...files: File[]) =>
  fireEvent.paste(field, { clipboardData: { files, types: ['Files'] } })

describe('WorkComposer attachments', () => {
  it('a pasted PNG is stored with the session and goes with the message by id', async () => {
    const user = userEvent.setup()
    const field = await mount()
    paste(field, pngFile())
    const tray = await screen.findByTestId('attachment-tray')
    await waitFor(() => expect(within(tray).getByRole('img')).toBeVisible())
    // The thumbnail is the file itself as a data URL: jsdom has no object URLs, so a
    // return to createObjectURL throws here, and nothing is left to release.
    expect(within(tray).getByRole('img')).toHaveAttribute(
      'src',
      `data:image/png;base64,${PNG_BASE64}`,
    )
    expect(api.uploadAttachment).toHaveBeenCalledWith('run_live', {
      media_type: 'image/png',
      data: PNG_BASE64,
    })
    await user.type(field, 'what is wrong here?')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() =>
      expect(api.inputWithAttachments).toHaveBeenCalledWith(
        'run_live',
        'what is wrong here?',
        ['att_1'],
      ),
    )
    // One body for every tool: no raw frame, no plain text route.
    expect(api.input).not.toHaveBeenCalled()
    expect(api.inputText).not.toHaveBeenCalled()
    // The picture went with the message; the draft starts empty.
    await waitFor(() =>
      expect(screen.queryByTestId('attachment-tray')).toBeNull(),
    )
  })

  it('an image dropped on the card and one chosen with the picker are attached too', async () => {
    const user = userEvent.setup()
    await mount()
    fireEvent.drop(screen.getByTestId('work-composer'), {
      dataTransfer: { files: [pngFile('dropped.png')], types: ['Files'] },
    })
    await waitFor(() => expect(api.uploadAttachment).toHaveBeenCalledTimes(1))
    await user.upload(screen.getByTestId('attach-input'), pngFile('picked.png'))
    await waitFor(() => expect(api.uploadAttachment).toHaveBeenCalledTimes(2))
    const tray = await screen.findByTestId('attachment-tray')
    await waitFor(() =>
      expect(within(tray).getAllByRole('img')).toHaveLength(2),
    )
  })

  it('a file that is not a PNG, JPEG, WebP or GIF is refused before anything is stored', async () => {
    const field = await mount()
    paste(field, new File(['x'], 'notes.pdf', { type: 'image/svg+xml' }))
    await waitFor(() => expect(toasts.error).toHaveBeenCalledTimes(1))
    expect(String(toasts.error.mock.calls[0][0])).toContain('notes.pdf')
    expect(String(toasts.error.mock.calls[0][0])).toContain(
      'PNG, JPEG, WebP or GIF',
    )
    expect(api.uploadAttachment).not.toHaveBeenCalled()
    expect(screen.queryByTestId('attachment-tray')).toBeNull()
  })

  it('an image over the limit is refused with the size and the limit', async () => {
    const field = await mount()
    paste(
      field,
      new File([new Uint8Array(4 * 2 ** 20)], 'big.png', { type: 'image/png' }),
    )
    await waitFor(() => expect(toasts.error).toHaveBeenCalledTimes(1))
    expect(String(toasts.error.mock.calls[0][0])).toMatch(
      /big\.png.*4\.0 MB.*3\.8 MB/,
    )
    expect(api.uploadAttachment).not.toHaveBeenCalled()
  })

  it("the engine's refusal (a tool with no image input) is said with the tool's name and nothing is attached", async () => {
    const body = {
      error: {
        code: 'unprocessable',
        message: 'OpenCode does not take images',
      },
    }
    const error = parseErrorEnvelope(body, 'Unprocessable')
    api.uploadAttachment.mockRejectedValueOnce(
      new ApiError(
        422,
        error.code,
        error.message,
        undefined,
        error.details,
        body,
      ),
    )
    const user = userEvent.setup()
    const field = await mount()
    paste(field, pngFile())
    await waitFor(() => expect(toasts.error).toHaveBeenCalledTimes(1))
    expect(String(toasts.error.mock.calls[0][0])).toContain(
      'OpenCode does not take images',
    )
    expect(screen.queryByTestId('attachment-tray')).toBeNull()
    // The message goes without it, as a plain turn: nothing was dropped silently, the
    // person was told.
    await user.type(field, 'hello')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledTimes(1))
    expect(api.inputWithAttachments).not.toHaveBeenCalled()
  })

  it('a file the browser cannot read is refused by name and nothing is uploaded', async () => {
    const read = vi
      .spyOn(FileReader.prototype, 'readAsDataURL')
      .mockImplementation(function (this: FileReader) {
        queueMicrotask(() =>
          this.onerror?.(
            new ProgressEvent('error') as ProgressEvent<FileReader>,
          ),
        )
      })
    try {
      const user = userEvent.setup()
      const field = await mount()
      paste(field, pngFile('unreadable.png'))
      await waitFor(() => expect(toasts.error).toHaveBeenCalledTimes(1))
      expect(String(toasts.error.mock.calls[0][0])).toContain('unreadable.png')
      expect(api.uploadAttachment).not.toHaveBeenCalled()
      expect(screen.queryByTestId('attachment-tray')).toBeNull()
      // The wait ended with the failure: Send is not left blocked on a read that never ends.
      await user.type(field, 'hello')
      await user.click(screen.getByTestId('composer-send'))
      await waitFor(() => expect(api.input).toHaveBeenCalledTimes(1))
    } finally {
      read.mockRestore()
    }
  })

  it('Send waits while an image is being stored and says why', async () => {
    let finish: (v: unknown) => void = () => {}
    api.uploadAttachment.mockImplementationOnce(
      () => new Promise((resolve) => (finish = resolve)),
    )
    const user = userEvent.setup()
    const field = await mount()
    await user.type(field, 'look')
    paste(field, pngFile())
    const send = screen.getByTestId('composer-send')
    await waitFor(() => expect(send).toBeDisabled())
    expect(send).toHaveAccessibleDescription(/Storing the image/)
    // The upload starts once the file is read; it can only be finished after that.
    await waitFor(() => expect(api.uploadAttachment).toHaveBeenCalledTimes(1))
    finish({ id: 'att_9', media_type: 'image/png', size_bytes: 8 })
    await waitFor(() => expect(send).toBeEnabled())
  })

  it('an image can be taken off before the message is sent', async () => {
    const user = userEvent.setup()
    const field = await mount()
    paste(field, pngFile('first.png'), pngFile('second.png'))
    const tray = await screen.findByTestId('attachment-tray')
    await waitFor(() =>
      expect(within(tray).getAllByRole('img')).toHaveLength(2),
    )
    await user.click(
      within(tray).getByRole('button', { name: 'Remove first.png' }),
    )
    await user.type(field, 'only the second')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() =>
      expect(api.inputWithAttachments).toHaveBeenCalledWith(
        'run_live',
        'only the second',
        ['att_2'],
      ),
    )
  })

  it(`a message takes at most ${MAX_ATTACHMENTS} images`, async () => {
    const field = await mount()
    paste(
      field,
      ...Array.from({ length: MAX_ATTACHMENTS + 1 }, (_, i) =>
        pngFile(`${i}.png`),
      ),
    )
    await waitFor(() => expect(toasts.error).toHaveBeenCalledTimes(1))
    expect(String(toasts.error.mock.calls[0][0])).toContain(
      `up to ${MAX_ATTACHMENTS} images`,
    )
    // The uploads start once each file is read.
    await waitFor(() =>
      expect(api.uploadAttachment).toHaveBeenCalledTimes(MAX_ATTACHMENTS),
    )
  })

  it('pasting text is left to the field', async () => {
    const field = await mount()
    const event = fireEvent.paste(field, {
      clipboardData: { files: [], types: ['text/plain'] },
    })
    expect(event).toBe(true)
    expect(api.uploadAttachment).not.toHaveBeenCalled()
  })
})

describe('WorkComposer attachments across a send and a session switch', () => {
  it('an image attached while a message is being sent stays for the next one', async () => {
    let accept: (v: unknown) => void = () => {}
    api.inputWithAttachments.mockImplementationOnce(
      () => new Promise((resolve) => (accept = resolve)),
    )
    const user = userEvent.setup()
    const field = await mount()
    paste(field, pngFile('first.png'))
    const tray = await screen.findByTestId('attachment-tray')
    await waitFor(() =>
      expect(within(tray).getAllByRole('img')).toHaveLength(1),
    )
    await user.type(field, 'one')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() =>
      expect(api.inputWithAttachments).toHaveBeenCalledTimes(1),
    )
    // Pasted while the first message is still on its way.
    paste(field, pngFile('second.png'))
    await waitFor(() => expect(api.uploadAttachment).toHaveBeenCalledTimes(2))
    accept({ accepted: true })
    await waitFor(() => expect(field).toHaveValue(''))
    const left = await screen.findByTestId('attachment-tray')
    await waitFor(() =>
      expect(within(left).getAllByRole('img')).toHaveLength(1),
    )
    expect(within(left).getByRole('img')).toHaveAttribute('alt', 'second.png')
    await user.type(field, 'two')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() =>
      expect(api.inputWithAttachments).toHaveBeenLastCalledWith(
        'run_live',
        'two',
        ['att_2'],
      ),
    )
  })

  it('an upload that finishes after the person moved to another session is said to be not added', async () => {
    let finish: (v: unknown) => void = () => {}
    api.uploadAttachment.mockImplementationOnce(
      () => new Promise((resolve) => (finish = resolve)),
    )
    const { rerender } = renderIntel(
      <WorkComposer attached={{ run: RUN, group: 'active' }} />,
    )
    paste(await screen.findByTestId('launcher-input'), pngFile('late.png'))
    // The upload starts once the file is read.
    await waitFor(() => expect(api.uploadAttachment).toHaveBeenCalledTimes(1))
    rerender(
      <WorkComposer
        attached={{ run: { ...RUN, run_ref: 'run_other' }, group: 'active' }}
      />,
    )
    finish({ id: 'att_7', media_type: 'image/png', size_bytes: 8 })
    await waitFor(() => expect(toasts.error).toHaveBeenCalledTimes(1))
    expect(String(toasts.error.mock.calls[0][0])).toMatch(
      /late\.png.*another session/,
    )
    expect(screen.queryByTestId('attachment-tray')).toBeNull()
  })

  it('an upload that finishes after its composer is gone is said to be not added', async () => {
    let finish: (v: unknown) => void = () => {}
    api.uploadAttachment.mockImplementationOnce(
      () => new Promise((resolve) => (finish = resolve)),
    )
    const { unmount } = renderIntel(
      <WorkComposer attached={{ run: RUN, group: 'active' }} />,
    )
    paste(await screen.findByTestId('launcher-input'), pngFile('late.png'))
    await waitFor(() => expect(api.uploadAttachment).toHaveBeenCalledTimes(1))
    unmount()
    finish({ id: 'att_8', media_type: 'image/png', size_bytes: 8 })
    await waitFor(() => expect(toasts.error).toHaveBeenCalledTimes(1))
    expect(String(toasts.error.mock.calls[0][0])).toMatch(
      /late\.png.*another session/,
    )
  })

  it("a send that finishes after the person moved to another session leaves that session's draft alone", async () => {
    let accept: (v: unknown) => void = () => {}
    api.inputWithAttachments.mockImplementationOnce(
      () => new Promise((resolve) => (accept = resolve)),
    )
    const user = userEvent.setup()
    const { rerender } = renderIntel(
      <WorkComposer attached={{ run: RUN, group: 'active' }} />,
    )
    const field = await screen.findByTestId('launcher-input')
    paste(field, pngFile('a.png'))
    await waitFor(() =>
      expect(
        within(screen.getByTestId('attachment-tray')).getAllByRole('img'),
      ).toHaveLength(1),
    )
    await user.type(field, 'one')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() =>
      expect(api.inputWithAttachments).toHaveBeenCalledTimes(1),
    )
    rerender(
      <WorkComposer
        attached={{ run: { ...RUN, run_ref: 'run_other' }, group: 'active' }}
      />,
    )
    paste(screen.getByTestId('launcher-input'), pngFile('b.png'))
    await waitFor(() => expect(api.uploadAttachment).toHaveBeenCalledTimes(2))
    await waitFor(() =>
      expect(
        within(screen.getByTestId('attachment-tray')).getByRole('img'),
      ).toHaveAttribute('alt', 'b.png'),
    )
    accept({ accepted: true })
    await waitFor(() =>
      expect(api.inputWithAttachments).toHaveBeenCalledTimes(1),
    )
    // The other session's image is still its draft's.
    expect(
      within(screen.getByTestId('attachment-tray')).getByRole('img'),
    ).toHaveAttribute('alt', 'b.png')
  })
})
