// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// The twelve regulatory-operations writes, tested against the CONTRACT THE
// ENGINE ACTUALLY ENFORCES.
//
// This file does not mock `./api`. Every request goes through the REAL client
// (lib/api/client.ts) into a stubbed `fetch`, and the assertions are on the
// REQUEST THE CLIENT HANDS TO FETCH: method, URL, query string, Content-Type and
// the exact body string, each pinned to the line of Go that enforces it. A cell
// asserting "the mutationFn was called" verifies that the console agrees with
// itself. The screen that presses these writes is a Business panel; its tests ship
// with that panel.
//
// ⚠ WHERE THE OBSERVATION STOPS, stated rather than implied: this is the pre-fetch
// RequestInit, not the bytes on the socket and not the bytes Go reads. No browser
// encodes the body here and no handler parses it. What is proved is exact
// forwarding from the client call to `fetch`. End-to-end byte identity needs a composed
// binary and a browser; this test runs neither.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { complianceApi, confirmedRemoval, isOpenCoreSeam } from './api'
import {
  COMPLIANCE_MAX_DOCUMENT_BYTES,
  COMPLIANCE_MAX_REF_RUNES,
  documentTooLarge,
  refTooLong,
  utf8ByteLength,
} from './types'

/** The operator's document, as it is typed into the textarea. */
const DOC = JSON.stringify({
  entity_lei: '5493001KJTIIGC8Y1R12',
  scope: 'group',
})

const REGISTER = {
  id: 'dr-1',
  regulation: 'DORA',
  entity_lei: '5493001KJTIIGC8Y1R12',
  entity_name: 'Example Bank',
  reference_date: '2026-12-31',
  error_count: 0,
  doc_sha256: 'a'.repeat(64),
  generated_by: 'ciso@example.com',
  generated_at: '2026-08-11T10:00:00Z',
  disclaimer:
    'Draft Register of Information. A competent person must review it.',
}

const INCIDENT = {
  id: 'di-1',
  reference: 'ICT-2026-004',
  major: true,
  provisional: true,
  critical_services: true,
  criteria_met: ['clients affected', 'duration'],
  rationale: 'critical services affected beyond the threshold',
  doc_sha256: 'b'.repeat(64),
  classified_by: 'ciso@example.com',
  classified_at: '2026-08-11T11:00:00Z',
  disclaimer:
    'Provisional classification. Decision support, not the legal verdict.',
}

const PROFILE = {
  id: 'op-1',
  framework: 'nist_800_53',
  doc_kind: 'profile',
  selected_control_ids: ['AC-2'],
  selected_count: 1,
  doc_sha256: 'c'.repeat(64),
  registered_by: 'ciso@example.com',
  registered_at: '2026-08-11T12:00:00Z',
  disclaimer: 'Ingested OSCAL document.',
}

const PACK = {
  id: 'dp-1',
  pack_type: 'us_state_law',
  regulation: 'TRAIGA',
  error_count: 0,
  doc_sha256: 'd'.repeat(64),
  generated_by: 'ciso@example.com',
  generated_at: '2026-08-11T13:00:00Z',
  disclaimer: 'Draft depth pack.',
}

const SECTOR_PACK = {
  ...PACK,
  id: 'dp-2',
  pack_type: 'sector_overlay',
  regulation: 'HIPAA',
}

/** TWO snapshots, and that is not padding. Drift compares against the PREDECESSOR
 *  (depthhandlers.go:1525-1535), so a one-snapshot fixture that answers 201 accepts
 *  a request production refuses — the exact "green because the double cannot
 *  reproduce what production can" failure caught by a rejecting fixture. */
const SNAPSHOT = {
  id: 'cs-0',
  snapshot_at: '2026-08-04T14:00:00Z',
  note: 'the oldest — no predecessor, so it is NOT an offerable pinned target',
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

/** A bodyless 204, which is what all five deletes on this tab actually answer. */
function noContent() {
  return new Response(null, { status: 204 })
}

/** Stub `fetch` and hand back the mock so a test can read the request it built. */
function stubFetch(
  handler: (url: string, init: RequestInit) => Response | Promise<Response>,
) {
  const mock = vi.fn((url: unknown, init: unknown) =>
    Promise.resolve(handler(String(url), (init ?? {}) as RequestInit)),
  )
  vi.stubGlobal('fetch', mock)
  return mock
}

/** The request the client actually built, parsed. */
function sentRequest(mock: ReturnType<typeof stubFetch>, call = 0) {
  const [url, init] = mock.mock.calls[call] as [string, RequestInit]
  const parsed = new URL(url, 'https://console.invalid')
  return {
    path: parsed.pathname,
    query: parsed.searchParams,
    method: init.method,
    body: init.body,
    headers: init.headers as Headers,
  }
}

beforeEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

// =============================================================================
// PART 1 — the wire contract of each write
// =============================================================================

describe('generateDoraRegister — raw document, reference date in the query', () => {
  it('sends the register document VERBATIM and does NOT double-encode it', async () => {
    const mock = stubFetch(() => jsonResponse(REGISTER, 201))

    await complianceApi.generateDoraRegister(DOC, '2026-12-31')

    const req = sentRequest(mock)
    expect(req.method).toBe('POST')
    expect(req.path).toBe('/v1/m/compliance/dora/register')
    // readBoundedBody (oscalprofile.go:493) reads the body as raw bytes and
    // regpackage.go:268 hashes exactly those into doc_sha256, BEFORE the packager
    // parses them. Byte-identical, or the anchor attests a document nobody wrote.
    expect(req.body).toBe(DOC)
    expect(req.headers.get('Content-Type')).toBe('application/json')
    // THE NEGATIVE CONTROL. Passing the document as the `body` argument runs it
    // through JSON.stringify (client.ts:139): a STRING comes back re-quoted and
    // escaped. This assertion is what tells the two shapes apart, and it fails the
    // moment the call reverts to http.post(path, document).
    expect(req.body).not.toBe(JSON.stringify(DOC))
    expect(String(req.body).startsWith('"')).toBe(false)
  })

  it('puts reference_date in the QUERY STRING, never in the body', async () => {
    const mock = stubFetch(() => jsonResponse(REGISTER, 201))

    await complianceApi.generateDoraRegister(DOC, '2026-12-31')

    // regpackage.go:265 reads it from r.URL.Query(); a date in the body would
    // never reach RegisterInput.ReferenceDate and would be silently absent.
    const req = sentRequest(mock)
    expect(req.query.get('reference_date')).toBe('2026-12-31')
    expect(String(req.body)).not.toContain('2026-12-31')
  })

  it('OMITS reference_date when blank rather than sending an empty one', async () => {
    const mock = stubFetch(() => jsonResponse(REGISTER, 201))

    await complianceApi.generateDoraRegister(DOC, '   ')

    // The handler clamps and stores whatever arrives (`:265`, `:301`), so ""
    // would persist as a real, empty reference date on a filed register.
    expect(sentRequest(mock).query.has('reference_date')).toBe(false)
  })
})

describe('classifyIncident — reference in the query, impact as raw bytes', () => {
  it('sends reference and finding_id as query params and the impact verbatim', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT, 201))

    await complianceApi.classifyIncident('ICT-2026-004', DOC, 'find-7')

    const req = sentRequest(mock)
    expect(req.path).toBe('/v1/m/compliance/dora/incidents')
    // doraincident.go:97 answers 400 "reference is required" when it is absent
    // from the URL; `:108` reads finding_id the same way; `:109` takes the body.
    expect(req.query.get('reference')).toBe('ICT-2026-004')
    expect(req.query.get('finding_id')).toBe('find-7')
    expect(req.body).toBe(DOC)
    expect(String(req.body)).not.toContain('ICT-2026-004')
    expect(req.body).not.toBe(JSON.stringify(DOC))
  })

  it('omits finding_id entirely when the operator left it blank', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT, 201))

    await complianceApi.classifyIncident('ICT-2026-004', DOC, '')

    expect(sentRequest(mock).query.has('finding_id')).toBe(false)
  })
})

describe('registerOscalProfile — the two query params that were unreachable', () => {
  it('sends the document raw and BOTH side inputs in the query', async () => {
    const mock = stubFetch(() => jsonResponse(PROFILE, 201))

    await complianceApi.registerOscalProfile(DOC, {
      framework: 'nist_800_53',
      scopeNote: 'production estate',
    })

    const req = sentRequest(mock)
    expect(req.path).toBe('/v1/m/compliance/oscal/profiles')
    // oscalprofile.go:249 body, :253 framework, :254 scope_note. Sent inside the
    // body they never reach ProfileInput.Framework — the resolver would be asked
    // to resolve against nothing.
    expect(req.body).toBe(DOC)
    expect(req.query.get('framework')).toBe('nist_800_53')
    expect(req.query.get('scope_note')).toBe('production estate')
    expect(req.body).not.toBe(JSON.stringify(DOC))
  })

  it('omits both when the operator supplied neither', async () => {
    const mock = stubFetch(() => jsonResponse(PROFILE, 201))

    await complianceApi.registerOscalProfile(DOC)

    const req = sentRequest(mock)
    expect(req.query.has('framework')).toBe(false)
    expect(req.query.has('scope_note')).toBe(false)
  })
})

describe('the two depth generators — raw document + ?scope_note', () => {
  it('generateUsLawPack posts to /depth/us-law with the document verbatim', async () => {
    const mock = stubFetch(() => jsonResponse(PACK, 201))

    await complianceApi.generateUsLawPack(DOC, 'CA and TX only')

    const req = sentRequest(mock)
    expect(req.path).toBe('/v1/m/compliance/depth/us-law')
    // depthhandlers.go:251 body, :256 scope_note, :262 hash.
    expect(req.body).toBe(DOC)
    expect(req.query.get('scope_note')).toBe('CA and TX only')
    expect(req.body).not.toBe(JSON.stringify(DOC))
  })

  it('generateSectorPack posts to /depth/sector with the same shape', async () => {
    const mock = stubFetch(() => jsonResponse(SECTOR_PACK, 201))

    await complianceApi.generateSectorPack(DOC, 'clinical only')

    const req = sentRequest(mock)
    expect(req.path).toBe('/v1/m/compliance/depth/sector')
    // depthhandlers.go:530 body, :535 scope_note, :541 hash.
    expect(req.body).toBe(DOC)
    expect(req.query.get('scope_note')).toBe('clinical only')
    expect(req.body).not.toBe(JSON.stringify(DOC))
  })
})

describe('triggerCcmSnapshot — the one write whose input IS a JSON body', () => {
  it('sends ONLY the two keys the decoder allows', async () => {
    const mock = stubFetch(() => jsonResponse(SNAPSHOT, 201))

    await complianceApi.triggerCcmSnapshot({
      frameworks: ['nist_800_53'],
      scope_note: 'quarterly',
    })

    const req = sentRequest(mock)
    expect(req.method).toBe('POST')
    expect(req.path).toBe('/v1/m/compliance/depth/ccm/snapshot')
    // depthhandlers.go:807-810 decodes into struct{Frameworks, ScopeNote} through
    // decodeJSON, which sets DisallowUnknownFields (helpers.go:99). ANY other key
    // is a 400 "invalid JSON body", not an ignored field — so the SET of keys on
    // the wire is the assertion, not just their values.
    expect(Object.keys(JSON.parse(String(req.body))).sort()).toEqual([
      'frameworks',
      'scope_note',
    ])
  })

  it('sends no body at all when nothing was narrowed — "all frameworks"', async () => {
    const mock = stubFetch(() => jsonResponse(SNAPSHOT, 201))

    await complianceApi.triggerCcmSnapshot()

    // The handler only reads the body when ContentLength > 0 (`:811`) and then
    // snapshots the whole catalog (`:821-826`). Absence is how "all" travels.
    expect(sentRequest(mock).body).toBeUndefined()
  })
})

describe('detectCcmDrift — the filter the engine reads, and where from', () => {
  it('puts snapshot_id in the QUERY STRING and sends no body', async () => {
    const mock = stubFetch(() => jsonResponse({ items: [] }, 201))

    await complianceApi.detectCcmDrift('cs-1')

    const req = sentRequest(mock)
    expect(req.method).toBe('POST')
    expect(req.path).toBe('/v1/m/compliance/depth/ccm/drift')
    // depthhandlers.go:984-985 reads r.URL.Query().Get("snapshot_id") and NEVER
    // touches the request body.
    expect(req.query.get('snapshot_id')).toBe('cs-1')
  })

  it('THE REGRESSION GUARD: the filter never travels in the body', async () => {
    const mock = stubFetch(() => jsonResponse({ items: [] }, 201))

    await complianceApi.detectCcmDrift('cs-1')

    // This assertion fails if
    // anyone restores `http.post(path, { snapshot_id })`. A body here is NOT a
    // 400 and NOT a network fault: the request succeeds with 201 and the engine
    // computes drift over its default pair of snapshots instead of the one the
    // operator picked. Every surface reports that as done.
    const req = sentRequest(mock)
    expect(req.body ?? null).toBeNull()
    expect(String(req.body ?? '')).not.toContain('cs-1')
  })

  it('omits the filter when no snapshot was pinned', async () => {
    const mock = stubFetch(() => jsonResponse({ items: [] }, 201))

    await complianceApi.detectCcmDrift()

    expect(sentRequest(mock).query.has('snapshot_id')).toBe(false)
  })
})

describe('the five deletes — the routes and the 204 they answer', () => {
  const cases: Array<[string, () => Promise<unknown>, string]> = [
    [
      'deleteDoraRegister',
      () => complianceApi.deleteDoraRegister('dr-1'),
      '/v1/m/compliance/dora/register/dr-1',
    ],
    [
      'deleteIncident',
      () => complianceApi.deleteIncident('di-1'),
      '/v1/m/compliance/dora/incidents/di-1',
    ],
    [
      'deleteOscalProfile',
      () => complianceApi.deleteOscalProfile('op-1'),
      '/v1/m/compliance/oscal/profiles/op-1',
    ],
    [
      'deleteUsLawPack',
      () => complianceApi.deleteUsLawPack('dp-1'),
      '/v1/m/compliance/depth/us-law/dp-1',
    ],
    [
      'deleteSectorPack',
      () => complianceApi.deleteSectorPack('dp-2'),
      '/v1/m/compliance/depth/sector/dp-2',
    ],
  ]

  it.each(cases)('%s issues DELETE to its route', async (_name, call, path) => {
    const mock = stubFetch(() => noContent())

    await call()

    const req = sentRequest(mock)
    expect(req.method).toBe('DELETE')
    expect(req.path).toBe(path)
  })

  it('preserves the STATUS, because on these routes it is the only word', async () => {
    stubFetch(() => noContent())

    const res = await complianceApi.deleteDoraRegister('dr-1')

    // deleteWithMeta, not delete: a bodyless 204 carries no `{"deleted":true}`,
    // so the status is the evidence and it has to survive to the caller.
    expect(res.status).toBe(204)
  })
})

// =============================================================================
// PART 2 — the allowlists, and the sibling helper that does NOT fit here
// =============================================================================

describe('confirmedRemoval — 204 and nothing else', () => {
  it('accepts the engine answer these five routes actually send', () => {
    expect(confirmedRemoval({ status: 204 })).toBe(true)
  })

  it('refuses a 202 — accepted is not done', () => {
    // The route does not answer 202 today, which is exactly why nothing else
    // notices: `http.delete` resolves on any 2xx and the console would announce a
    // regulatory artefact as gone the moment this route grew a queued path.
    expect(confirmedRemoval({ status: 202 })).toBe(false)
  })

  it('refuses a 200 — that is the SIBLING plane, not this one', () => {
    // The NIS 2 route answers 200 + {"deleted":true} (nis2incident.go:403) and
    // has its own helper. Accepting 200 here would mean this allowlist had been
    // widened to fit a route it does not guard.
    expect(confirmedRemoval({ status: 200 })).toBe(false)
  })
})

describe('isOpenCoreSeam — by STATUS, not by prose', () => {
  it('recognises the 501 every generator on this tab can answer', () => {
    expect(isOpenCoreSeam(new ApiError(501, 'not_implemented', 'add-on'))).toBe(
      true,
    )
  })

  it('does NOT call a 500 a boundary just because it says "not implemented"', () => {
    expect(
      isOpenCoreSeam(new ApiError(500, 'internal', 'not implemented')),
    ).toBe(false)
  })
})

describe('the engine limits the dialogs mirror', () => {
  it('measures the document in BYTES, not UTF-16 units', () => {
    // helpers.go:33 caps the body at 1 MiB and readBoundedBody REJECTS over it
    // (413, oscalprofile.go:503-507) rather than truncating.
    const oneOverInBytes = '€'.repeat(COMPLIANCE_MAX_DOCUMENT_BYTES / 3 + 1)
    expect(utf8ByteLength('€')).toBe(3)
    expect(documentTooLarge(oneOverInBytes)).toBe(true)
    expect(documentTooLarge('x'.repeat(COMPLIANCE_MAX_DOCUMENT_BYTES))).toBe(
      false,
    )
  })

  it('measures the reference in RUNES, as tooLong does', () => {
    // helpers.go:212-214 counts len([]rune(s)). An astral character is ONE rune
    // and TWO UTF-16 units, so `.length` would refuse a reference the engine
    // accepts — the mirror has to count the same unit or it invents a rejection.
    const astral = '𝄞'.repeat(COMPLIANCE_MAX_REF_RUNES)
    expect(astral.length).toBe(COMPLIANCE_MAX_REF_RUNES * 2)
    expect(refTooLong(astral)).toBe(false)
    expect(refTooLong(astral + '𝄞')).toBe(true)
  })
})
