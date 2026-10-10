// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// the NIS 2 incident surface, tested against the CONTRACT THE ENGINE
// ACTUALLY ENFORCES rather than against a double that agrees with the console.
//
// WHY THIS FILE DOES NOT MOCK `./api`. The sibling surface got that wrong and the
// cost is measured: `capabilities.test.tsx:189-192` is GREEN while both
// tool-pinning writes answer 400 in production, because the test's double accepts
// the body the engine rejects (toolpins_evidence_test.go:166-191). A cell that
// asserts "the mutationFn was called" has verified the console agrees with itself.
//
// So every request here goes through the REAL client (lib/api/client.ts) into a
// stubbed `fetch`, and the assertions are on the REQUEST THE CLIENT HANDS TO FETCH:
// method, URL, query string, Content-Type and the exact body string. Each one is
// pinned to the line of Go that enforces it, so it fails when either side moves.
//
// ⚠ AND THAT IS WHERE THE OBSERVATION STOPS — the Codex sol max contrast was right
// to narrow the claim (F5). This is the pre-fetch RequestInit, not the bytes on the
// socket and not the bytes Go reads: no browser encodes the body here and no handler
// parses it. What is proved is exact forwarding from the client call to `fetch`. The
// end-to-end byte identity needs a composed binary and a browser, and this session
// ran neither -- it is declared in the session file, not implied away here.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { ApiError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { complianceApi, confirmedDeleted, isOpenCoreSeam } from './api'
import {
  isKnownNis2Phase,
  NIS2_MAX_REFERENCE_RUNES,
  NIS2_PHASES,
  nis2PhasesAfter,
  nis2ReferenceTooLong,
} from './types'

// The client reads the session and tenant through the getters the app wires at boot
// (app/providers.tsx); downloads go through that client.
configureApiClient({
  getCSRFToken: () => useSessionStore.getState().csrfToken,
  getTenant: () => useTenantStore.getState().activeTenant,
})

/** The operator's impact document, as it is typed into the textarea. */
const IMPACT = JSON.stringify({
  awareness_at: '2026-06-04T12:00:00Z',
  affected_services: ['checkout'],
  users_affected: 12000,
  operational_disruption: 'severe',
})

const INCIDENT = {
  id: 'ni-1',
  reference: 'INC-001',
  significant: true,
  provisional: true,
  cross_border: true,
  suspected_crime: false,
  criteria_met: ['23(3)(a)', '23(3)(b)'],
  rationale: 'severe operational disruption AND persons affected',
  phase: 'early_warning',
  doc_sha256: 'a'.repeat(64),
  classified_by: 'dpo@example.com',
  classified_at: '2026-06-04T13:00:00Z',
  disclaimer:
    'Provisional NIS 2 Directive significant-incident classification. The verdict is DECISION SUPPORT, not the legal classification.',
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
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

// --- the wire contract of classify -------------------------------------------

describe('classifyNis2Incident — the request the engine actually parses', () => {
  it('puts reference and finding_id in the QUERY STRING, never in the body', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT, 201))

    await complianceApi.classifyNis2Incident('INC-001', IMPACT, 'find-9')

    const req = sentRequest(mock)
    // nis2incident.go:127 reads r.URL.Query().Get("reference") and answers 400
    // "reference is required" when it is absent; :136 reads finding_id the same way.
    expect(req.method).toBe('POST')
    expect(req.path).toBe('/v1/m/compliance/nis2/incidents/classify')
    expect(req.query.get('reference')).toBe('INC-001')
    expect(req.query.get('finding_id')).toBe('find-9')
    // A reference smuggled into the body would be invisible to the handler.
    expect(String(req.body)).not.toContain('INC-001')
  })

  it('omits finding_id entirely when the operator left it blank', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT, 201))

    await complianceApi.classifyNis2Incident('INC-001', IMPACT)

    // Absent, not an empty string: the handler clamps and stores whatever arrives
    // (nis2incident.go:136), so "" would persist as an empty linked finding.
    expect(sentRequest(mock).query.has('finding_id')).toBe(false)
  })

  it('sends the impact document VERBATIM — the bytes that get hashed', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT, 201))

    await complianceApi.classifyNis2Incident('INC-001', IMPACT)

    const req = sentRequest(mock)
    // readBoundedBody (oscalprofile.go:475) takes the body as raw bytes and
    // nis2incident.go:143 hashes exactly those into doc_sha256, then hands them to
    // the add-on to parse. Byte-identical or the anchor attests something else.
    expect(req.body).toBe(IMPACT)
    expect(req.headers.get('Content-Type')).toBe('application/json')
  })

  it('does NOT double-encode it — the defect this shape exists to avoid', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT, 201))

    await complianceApi.classifyNis2Incident('INC-001', IMPACT)

    // THE NEGATIVE CONTROL, and it is the whole reason `rawBody` is used here.
    // Passing the document as the `body` argument runs it through JSON.stringify
    // (client.ts:139), so a STRING comes back out re-quoted and escaped: the engine
    // would hash `"{\"awareness_at\":…}"` — a different document — and hand the
    // classifier a JSON string where it expects the impact object.
    //
    // This assertion is what tells the two shapes apart. It fails the moment the
    // call reverts to `http.post(path, impact, …)`, which is how the DORA sibling
    // one entry above was found broken.
    expect(sentRequest(mock).body).not.toBe(JSON.stringify(IMPACT))
    expect(String(sentRequest(mock).body).startsWith('"')).toBe(false)
  })

  it('classifyIncident (DORA) carries the same fix — same handler shape', async () => {
    const mock = stubFetch(() => jsonResponse({ id: 'di-1' }, 201))

    await complianceApi.classifyIncident('DORA-1', IMPACT)

    const req = sentRequest(mock)
    expect(req.query.get('reference')).toBe('DORA-1')
    expect(req.body).toBe(IMPACT)
    expect(req.body).not.toBe(JSON.stringify(IMPACT))
  })

  it('surfaces the engine 501 as an ApiError the seam test recognises', async () => {
    stubFetch(() =>
      jsonResponse(
        {
          error: {
            message:
              'NIS 2 significant-incident classification requires the Olivares enterprise add-on (nis2incident); not linked in this build',
          },
        },
        501,
      ),
    )

    await expect(
      complianceApi.classifyNis2Incident('INC-001', IMPACT),
    ).rejects.toBeInstanceOf(ApiError)
  })
})

// --- the wire contract of the phase update ------------------------------------

describe('updateNis2Incident — a body the engine will not reject', () => {
  it('sends ONLY phase and note', async () => {
    const mock = stubFetch(() =>
      jsonResponse({ ...INCIDENT, phase: 'notification' }),
    )

    await complianceApi.updateNis2Incident('ni-1', {
      phase: 'notification',
      note: 'CSIRT notified',
    })

    const req = sentRequest(mock)
    expect(req.method).toBe('PUT')
    expect(req.path).toBe('/v1/m/compliance/nis2/incidents/ni-1')
    // The handler decodes into struct{Phase, Note string} (nis2incident.go:270-273)
    // through decodeJSON, which sets DisallowUnknownFields (helpers.go:99). ANY
    // other key is 400 "invalid JSON body" — not an ignored field. So the set of
    // keys on the wire is the assertion, not just their values.
    expect(Object.keys(JSON.parse(String(req.body))).sort()).toEqual([
      'note',
      'phase',
    ])
  })

  it('drops an empty note instead of sending a key with no value', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT))

    await complianceApi.updateNis2Incident('ni-1', { phase: 'final' })

    expect(Object.keys(JSON.parse(String(sentRequest(mock).body)))).toEqual([
      'phase',
    ])
  })

  it('percent-encodes the id into the path', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT))

    await complianceApi.updateNis2Incident('ni/1', { phase: 'final' })

    expect(sentRequest(mock).path).toBe(
      '/v1/m/compliance/nis2/incidents/ni%2F1',
    )
  })
})

// --- the export: the auditor's bytes ------------------------------------------

describe('exportNis2Incident — the server bytes, and the auth to get them', () => {
  it('GETs the export route with auth and tenant, and keeps the bytes intact', async () => {
    useSessionStore.setState({ csrfToken: 'tok' } as never)
    useTenantStore.setState({ activeTenant: 'acme' } as never)
    // PRETTY-PRINTED ON PURPOSE. The first version of this fixture was
    // `{"id":"ni-1","ledger_anchor":{"seq":12}}` — already exactly what
    // JSON.stringify emits — so the mutant that replaced `text` with
    // `JSON.stringify(JSON.parse(text))` SURVIVED: the assertion could not tell a
    // byte-exact passthrough from a parse-and-reserialize round trip. Indentation
    // and spacing are the difference the claim is about.
    const body = '{\n  "id": "ni-1",\n  "ledger_anchor": { "seq": 12 }\n}\n'
    const mock = stubFetch(
      () =>
        new Response(body, {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
    )

    const res = await complianceApi.exportNis2Incident('ni-1')

    const req = sentRequest(mock)
    expect(req.method).toBe('GET')
    expect(req.path).toBe('/v1/m/compliance/nis2/incidents/ni-1/export')
    // This route self-audits in the caller's transaction (nis2incident.go:346), so
    // it has to arrive as the real principal or the ledger entry names nobody.
    expect(req.headers.get('X-CSRF-Token')).toBe('tok')
    expect(req.headers.get('X-Olivares-Tenant')).toBe('acme')
    // Byte-exact: what an auditor is handed is what the server sealed, never a
    // parse-and-reserialize round trip.
    expect(res.text).toBe(body)
    expect(res.filename).toBe('nis2-incident-ni-1.json')
  })

  it('raises the engine envelope instead of writing a half file', async () => {
    stubFetch(() => jsonResponse({ error: { message: 'not found' } }, 404))

    await expect(
      complianceApi.exportNis2Incident('ni-x'),
    ).rejects.toBeInstanceOf(ApiError)
  })
})

// --- forward-only phases: the console side of a 409 ---------------------------

describe('nis2PhasesAfter — the engine rule, before the request', () => {
  it('offers only the phases ahead of the current one', () => {
    expect(nis2PhasesAfter('early_warning')).toEqual([
      'notification',
      'intermediate',
      'final',
    ])
    expect(nis2PhasesAfter('intermediate')).toEqual(['final'])
  })

  it('offers nothing past the last phase — no move that could 409', () => {
    // nis2incident.go:295 answers 409 when the target ordinal is <= the current
    // one, and `final` is the last (nis2seam.go:99-101).
    expect(nis2PhasesAfter('final')).toEqual([])
  })

  it('never offers the current phase or an earlier one', () => {
    for (const [i, phase] of NIS2_PHASES.entries()) {
      const offered = nis2PhasesAfter(phase)
      expect(offered).not.toContain(phase)
      expect(offered).toEqual([...NIS2_PHASES].slice(i + 1))
    }
  })

  it('offers NOTHING for a phase this build cannot order', () => {
    // This cell previously required the OPPOSITE — the whole vocabulary — on the
    // reasoning that the engine rejects a bad move anyway. The Codex sol max
    // contrast refuted both halves (F6/F8) and the cell was codifying the defect:
    //
    //   a value from a NEWER vocabulary is ahead of everything this build knows, so
    //   every option offered was backwards, i.e. a guaranteed 409;
    //
    //   and the engine does NOT catch it — nis2PhaseIndex returns -1 for an unknown
    //   current phase (nis2seam.go:113-120), so `-1 < any` and the forward-only
    //   check at nis2incident.go:295 passes for every target, on a column that is
    //   unconstrained text (schema.go:982-998).
    //
    // Both sides fail open, so "the engine is the authority" was not a fallback.
    expect(nis2PhasesAfter('post_mortem')).toEqual([])
    expect(isKnownNis2Phase('post_mortem')).toBe(false)
    // …and the predicate still recognises the real vocabulary, or "unknown" would
    // swallow every phase and the action would vanish everywhere.
    for (const p of NIS2_PHASES) expect(isKnownNis2Phase(p)).toBe(true)
  })
})

// --- the reference guard: the same unit the engine counts ---------------------

describe('nis2ReferenceTooLong — runes, because the engine counts runes', () => {
  it('accepts the boundary and refuses one past it', () => {
    expect(nis2ReferenceTooLong('a'.repeat(NIS2_MAX_REFERENCE_RUNES))).toBe(
      false,
    )
    expect(nis2ReferenceTooLong('a'.repeat(NIS2_MAX_REFERENCE_RUNES + 1))).toBe(
      true,
    )
    expect(nis2ReferenceTooLong('')).toBe(false)
  })

  it('does not refuse an astral reference the engine would accept', () => {
    // 600 emoji: 600 runes to Go's len([]rune(s)) (helpers.go:212-214), but 1200
    // to JavaScript's `.length`, which counts UTF-16 code units. Counting the
    // wrong unit blocks a reference the engine takes — and the operator cannot
    // tell why, because the console never sent it.
    const astral = '🛡'.repeat(600)
    expect(astral.length).toBe(1200)
    expect([...astral].length).toBe(600)
    expect(nis2ReferenceTooLong(astral)).toBe(false)
  })

  it('still refuses an astral reference that is genuinely too long', () => {
    // The direction of non-firing: a guard that accepts everything passes any
    // "accepts" test. This one must still say yes.
    expect(nis2ReferenceTooLong('🛡'.repeat(NIS2_MAX_REFERENCE_RUNES + 1))).toBe(
      true,
    )
  })
})

// --- the two allowlists --------------------------------------------------------

describe('isOpenCoreSeam — by STATUS, not by prose', () => {
  it('recognises 501 as the add-on boundary', () => {
    expect(isOpenCoreSeam(new ApiError(501, 'x', 'not linked'))).toBe(true)
  })

  it('does NOT call a 500 a boundary just because it says "not implemented"', () => {
    // Matching the message is how a real fault gets drawn as a calm add-on notice.
    expect(
      isOpenCoreSeam(new ApiError(500, 'x', 'handler not implemented')),
    ).toBe(false)
    expect(isOpenCoreSeam(new ApiError(403, 'x', 'nope'))).toBe(false)
    expect(isOpenCoreSeam(new Error('501'))).toBe(false)
  })
})

describe('confirmedDeleted — a 2xx is not evidence a classification is gone', () => {
  it('accepts the engine answer', () => {
    // nis2incident.go:403 writes {"deleted": true} with 200.
    expect(confirmedDeleted({ status: 200, data: { deleted: true } })).toBe(
      true,
    )
  })

  it('refuses a 200 that does not say it deleted anything', () => {
    expect(confirmedDeleted({ status: 200, data: {} })).toBe(false)
    expect(confirmedDeleted({ status: 200, data: { deleted: 'true' } })).toBe(
      false,
    )
    expect(confirmedDeleted({ status: 200, data: null })).toBe(false)
  })

  it('refuses a 202 — accepted is not done', () => {
    expect(confirmedDeleted({ status: 202, data: { deleted: true } })).toBe(
      false,
    )
  })

  it('refuses a bodyless 204 — no body is no word', () => {
    // THE MUTANT THAT SURVIVED. An earlier version returned true for any 204, which
    // contradicted the rule one line above it: the engine's word is the evidence.
    // Codex named it (F7) and I removed the branch — but removing a branch without
    // pinning its absence leaves it one careless edit from coming back, and the
    // re-introduction mutant compiled and passed all 35 cells until this one existed.
    // Unreachable on today's route (it answers 200, nis2incident.go:403); that is
    // exactly why nothing else notices.
    expect(confirmedDeleted({ status: 204, data: undefined })).toBe(false)
    expect(confirmedDeleted({ status: 204, data: { deleted: true } })).toBe(
      false,
    )
  })
})
