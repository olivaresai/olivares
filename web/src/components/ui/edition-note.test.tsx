// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A CAPABILITY REFUSED BY EDITION: one calm line, once per capability and browser, then
// only the quiet edition tag. The engine refuses in three shapes (403 edition_required,
// 501 business_required, 403 addon_requires_license); each names the edition whole, and
// no code, status or capability key ever reaches what a person reads or hears.
import { cleanup, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { preferenceStorage } from '@/lib/preference-storage'
import { renderIntel } from '@/test/intel'
import { EditionNote, editionRefusal } from './edition-note'

const RAW =
  /\b501\b|\b403\b|business_required|edition_required|addon_requires_license|accessmap|claude-policy|inferenceproxy|sourcescope|\bsso\b|mystery-key/

const refused = (
  status: number,
  code: string,
  extra: Record<string, unknown> = {},
  message = `raw: ${status} ${code}`,
) => new ApiError(status, code, message, 'req-1', extra)

/** What a person can meet: the text, and every attribute a browser reads aloud or shows. */
function expectNoRawWords() {
  expect(document.body.textContent ?? '').not.toMatch(RAW)
  for (const el of document.body.querySelectorAll('*'))
    for (const name of ['title', 'aria-label', 'aria-description'])
      expect(el.getAttribute(name) ?? '').not.toMatch(RAW)
}

/** The line as its mount draws it: by default for whoever can add a license. */
function show(
  error: unknown,
  place?: string,
  readsBelow?: boolean,
  canAddLicense = true,
) {
  const refusal = editionRefusal(error, place)
  expect(refusal).not.toBeNull()
  return renderIntel(
    <EditionNote
      refusal={refusal!}
      readsBelow={readsBelow}
      canAddLicense={canAddLicense}
    />,
  )
}

beforeEach(() => {
  localStorage.clear()
  preferenceStorage.attach(null, {})
})
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('editionRefusal', () => {
  it('reads nothing from any other answer', () => {
    expect(editionRefusal(refused(403, 'forbidden'))).toBeNull()
    expect(editionRefusal(refused(404, 'module_not_enabled'))).toBeNull()
    expect(editionRefusal(new Error('edition_required'))).toBeNull()
    expect(editionRefusal(undefined)).toBeNull()
    expect(editionRefusal(refused(501, 'not_implemented'))).toBeNull()
    expect(
      editionRefusal(refused(403, 'forbidden', { edition: 'community' })),
    ).toBeNull()
  })

  // The engine as written (core/api/errorbody.go errorEdition): every code that sends a
  // person to an edition names it in `edition`; only edition_required names a capability.
  // A caller's key stands for the capability only where it IS what was refused: a page's
  // or a module's key is wider, so the caller passes none and the line names none.
  it('reads the edition any refusal names; the capability from the answer or the refused key', () => {
    const departments = editionRefusal(
      refused(501, 'departments_unavailable', { edition: 'business' }),
    )
    expect(departments?.edition).toBe('business')
    expect(departments?.capability).toBeUndefined()
    expect(
      editionRefusal(
        refused(501, 'organizations_unavailable', { edition: 'enterprise' }),
      ),
    ).toMatchObject({ edition: 'enterprise' })
    expect(
      editionRefusal(
        refused(501, 'business_required', { edition: 'business' }),
        'approvalTiers',
      ),
    ).toMatchObject({ edition: 'business', capability: 'approvalTiers' })
    expect(
      editionRefusal(
        refused(403, 'edition_required', {
          edition: 'engineer',
          capability: 'skills',
        }),
        'approvalTiers',
      ),
    ).toMatchObject({ edition: 'engineer', capability: 'skills' })
  })
})

describe('EditionNote: the three refusal shapes', () => {
  it.each([
    [
      '403 edition_required',
      refused(403, 'edition_required', {
        edition: 'engineer',
        capability: 'accessmap',
      }),
      undefined,
      'Access map is part of Olivares AI Engineer.',
    ],
    [
      '501 business_required, as the engine answers today',
      refused(501, 'business_required'),
      undefined,
      'This is part of Olivares AI Business.',
    ],
    [
      '501 business_required naming its edition, for the refused write',
      refused(501, 'business_required', { edition: 'business' }),
      'approvalTiers',
      "Changing an action's risk tier or approval count is part of Olivares AI Business.",
    ],
    [
      '403 addon_requires_license naming its edition',
      refused(403, 'addon_requires_license', { edition: 'business' }),
      'sso',
      'Single sign-on is part of Olivares AI Business.',
    ],
    [
      '501 departments_unavailable, an older code that names its edition',
      refused(501, 'departments_unavailable', { edition: 'business' }),
      undefined,
      'This is part of Olivares AI Business.',
    ],
    [
      '403 edition_required for Enterprise',
      refused(403, 'edition_required', {
        edition: 'enterprise',
        capability: 'sourcescope',
      }),
      undefined,
      'Source scope is part of Olivares AI Enterprise.',
    ],
  ])('%s: one line naming the edition whole', (_, error, place, lead) => {
    show(error, place)
    const notes = screen.getAllByRole('status')
    expect(notes).toHaveLength(1)
    expect(notes[0]).toHaveTextContent(
      `${lead} Add a license in Settings > Edition, or see olivares.ai/pricing.`,
    )
    expectNoRawWords()
  })

  it('an add-on refusal that names no edition keeps the engine sentence, naming none', () => {
    show(
      refused(
        403,
        'addon_requires_license',
        { capability: 'sso' },
        'The identity add-on is not licensed. Reads and exports keep working.',
      ),
    )
    expect(screen.getByRole('status')).toHaveTextContent(
      'The identity add-on is not licensed. Reads and exports keep working.',
    )
    expect(screen.queryByText(/Olivares AI/)).toBeNull()
    expectNoRawWords()
  })

  // A newer engine may name an edition this console does not know: it cannot name it whole,
  // so the line is the engine's own sentence, and the raw value is never printed.
  it('an edition the console does not know is never printed raw', () => {
    show(
      refused(
        403,
        'edition_required',
        { edition: 'platinum', capability: 'knowledge' },
        'Knowledge is part of Olivares AI Platinum.',
      ),
    )
    expect(screen.getByRole('status')).toHaveTextContent(
      'Knowledge is part of Olivares AI Platinum.',
    )
    expect(document.body.textContent).not.toMatch(/\bplatinum\b/)
    expect(screen.queryByText(/Add a license|Ask an administrator/)).toBeNull()
    expectNoRawWords()
    expect(preferenceStorage.getItem('olivares.notices.dismissed')).toBeNull()
  })

  it('a capability the console has no words for is never printed by its key', () => {
    show(
      refused(403, 'edition_required', {
        edition: 'engineer',
        capability: 'mystery-key',
      }),
    )
    expect(screen.getByRole('status')).toHaveTextContent(
      'This is part of Olivares AI Engineer.',
    )
    expectNoRawWords()
  })

  it('says what stays where stored data stays readable below the edition', () => {
    show(
      refused(403, 'edition_required', {
        edition: 'engineer',
        capability: 'knowledge',
      }),
      undefined,
      true,
    )
    expect(screen.getByRole('status')).toHaveTextContent(
      'What is already stored stays readable and exportable.',
    )
  })
})

/** Where the console keeps what a person was already told (lib/notify.ts). */
const DISMISSED = 'olivares.notices.dismissed'

describe('EditionNote: once per capability and person', () => {
  const knowledge = () =>
    refused(403, 'edition_required', {
      edition: 'engineer',
      capability: 'knowledge',
    })

  it('a second visit renders no line, only the quiet edition tag', () => {
    show(knowledge())
    expect(screen.getByRole('status')).toBeInTheDocument()
    cleanup()
    show(knowledge())
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.queryByText(/is part of|Add a license/)).toBeNull()
    expect(screen.getByText('Olivares AI Engineer')).toBeInTheDocument()
    expect(screen.getByText('Part of Olivares AI Engineer')).toHaveClass(
      'sr-only',
    )
    expectNoRawWords()
  })

  it('remembers each capability apart, and tells again a capability now in another edition', () => {
    show(knowledge())
    cleanup()
    show(
      refused(403, 'edition_required', {
        edition: 'engineer',
        capability: 'skills',
      }),
    )
    expect(screen.getByRole('status')).toHaveTextContent(
      'Skills is part of Olivares AI Engineer.',
    )
    cleanup()
    show(
      refused(403, 'edition_required', {
        edition: 'business',
        capability: 'knowledge',
      }),
    )
    expect(screen.getByRole('status')).toHaveTextContent(
      'Knowledge is part of Olivares AI Business.',
    )
  })

  it('a refusal that names no capability is told each time and silences no other', () => {
    const unnamed = () => refused(501, 'business_required')
    show(unnamed())
    cleanup()
    show(unnamed())
    expect(screen.getByRole('status')).toHaveTextContent(
      'This is part of Olivares AI Business.',
    )
    expect(preferenceStorage.getItem(DISMISSED)).toBeNull()
  })

  it('writes the memory once: a second visit writes nothing', () => {
    show(knowledge())
    cleanup()
    const write = vi.spyOn(preferenceStorage, 'setItem')
    show(knowledge())
    expect(screen.queryByRole('status')).toBeNull()
    expect(write).not.toHaveBeenCalled()
  })

  it('is the person’s own memory: another person signed in here is told again', () => {
    show(knowledge())
    cleanup()
    preferenceStorage.attach(null, {}, ['https://one', 'user', 'u-2', 'acme'])
    show(knowledge())
    expect(screen.getByRole('status')).toBeInTheDocument()
  })

  it('keeps no secret and no words of the person: one entry per capability, with the dismissed notices', () => {
    show(knowledge())
    const told = JSON.parse(preferenceStorage.getItem(DISMISSED) ?? '{}')
    expect(told).toStrictEqual({ 'edition-note:knowledge': 'engineer' })
    expect(localStorage.length).toBe(0)
  })

  it('shows the line again when the memory cannot be read, and never fails', () => {
    preferenceStorage.setItem(DISMISSED, '{not json')
    show(knowledge())
    cleanup()
    preferenceStorage.setItem(DISMISSED, '{not json')
    show(knowledge())
    expect(screen.getByRole('status')).toHaveTextContent(
      'Knowledge is part of Olivares AI Engineer.',
    )
  })
})

describe('EditionNote: its two ways on', () => {
  const note = () =>
    show(
      refused(403, 'edition_required', {
        edition: 'business',
        capability: 'sso',
      }),
    )

  it('olivares.ai/pricing opens apart, with nothing of the console', () => {
    note()
    const pricing = screen.getByRole('link', { name: 'olivares.ai/pricing' })
    expect(pricing).toHaveAttribute('href', 'https://olivares.ai/pricing')
    expect(pricing).toHaveAttribute('target', '_blank')
    expect(pricing.getAttribute('rel')?.split(' ')).toEqual(
      expect.arrayContaining(['noopener', 'noreferrer']),
    )
  })

  it('Settings > Edition is a link to that place for whoever can add a license', () => {
    note()
    expect(
      screen.getByRole('link', { name: 'Settings > Edition' }),
    ).toHaveAttribute('href', '/settings?section=edition')
  })

  // The mount decides by the license tab's own gate; the line only says what it is told.
  it('whoever cannot add a license is asked to ask an administrator, with no link', () => {
    show(
      refused(403, 'edition_required', {
        edition: 'business',
        capability: 'sso',
      }),
      undefined,
      false,
      false,
    )
    expect(screen.getByRole('status')).toHaveTextContent(
      'Single sign-on is part of Olivares AI Business. Ask an administrator.',
    )
    expect(screen.queryByRole('link')).toBeNull()
    expect(screen.queryByText(/Add a license|olivares\.ai\/pricing/)).toBeNull()
    expectNoRawWords()
  })

  it('an unnamed refusal asks the same, naming the edition whole', () => {
    show(refused(501, 'business_required'), undefined, false, false)
    expect(screen.getByRole('status')).toHaveTextContent(
      'This is part of Olivares AI Business. Ask an administrator.',
    )
  })
})
