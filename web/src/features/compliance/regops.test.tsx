// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// the twelve regulatory-operations writes, tested against the CONTRACT THE
// ENGINE ACTUALLY ENFORCES and, separately, against the SCREEN THAT INVOKES THEM.
//
// WHY BOTH HALVES EXIST, because each is blind to what the other catches.
//
//   The wire half does not mock `./api`. Every request goes through the REAL client
//   (lib/api/client.ts) into a stubbed `fetch`, and the assertions are on the
//   REQUEST THE CLIENT HANDS TO FETCH: method, URL, query string, Content-Type and
//   the exact body string, each pinned to the line of Go that enforces it. A cell
//   asserting "the mutationFn was called" verifies that the console agrees with
//   itself — measured cost: capabilities.test.tsx:189-192 is GREEN while both
//   tool-pinning writes answer 400 in production.
//
//   The screen half mounts from the CONTAINER (`ComplianceView`), opens the tab and
//   presses the button. This is the half that catches the defect this session came
//   to fix: twelve perfectly correct client functions with no caller. A wire test
//   calling `complianceApi.*` directly is green on a tab with no buttons at all,
//   which is exactly the state of the base branch. And it starts at the container
//   rather than at `RegOpsTab` because rendering the child directly cannot see a
//   tab that was never wired into the parent — the failure mode that left 1644
//   cells green here once before.
//
//   ⚠ AND "PRESSED" MEANS ALL TWELVE, which it did not in the first version of this
//   file. Five were pressed and the other seven rested on a direct client test plus
//   a "the button exists" assertion — so the the model contrast rewired the OSCAL
//   button to `generateUsLawPack`, a button that creates the WRONG regulatory
//   artefact, and it compiled with all 40 cells green (F4). The GENERATORS and
//   DELETES tables below press every one and assert the ROUTE each must reach,
//   which is the assertion a wrong-route mutant cannot satisfy.
//
// ⚠ WHERE THE OBSERVATION STOPS, stated rather than implied: this is the pre-fetch
// RequestInit, not the bytes on the socket and not the bytes Go reads. No browser
// encodes the body here and no handler parses it. What is proved is exact
// forwarding from React state to `fetch`. End-to-end byte identity needs a composed
// binary and a browser; this session ran neither.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import bundleComplianceDe from './i18n/de.json'
import bundleComplianceEn from './i18n/en.json'
import bundleComplianceEs from './i18n/es.json'
import bundleComplianceFr from './i18n/fr.json'
import bundleComplianceJa from './i18n/ja.json'
import bundleComplianceRu from './i18n/ru.json'
import bundleComplianceZh from './i18n/zh.json'

const bundlesCompliance = {
  de: bundleComplianceDe,
  en: bundleComplianceEn,
  es: bundleComplianceEs,
  fr: bundleComplianceFr,
  ja: bundleComplianceJa,
  ru: bundleComplianceRu,
  zh: bundleComplianceZh,
}
import {
  DEFAULT_AUTH,
  renderIntel,
  screen,
  userEvent,
  waitFor,
  within,
} from '@/test/intel'
import '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import { complianceApi, confirmedRemoval, isOpenCoreSeam } from './api'
import {
  COMPLIANCE_MAX_DOCUMENT_BYTES,
  COMPLIANCE_MAX_REF_RUNES,
  documentTooLarge,
  refTooLong,
  utf8ByteLength,
} from './types'

/** RBAC is exercised by driving `can()`, which is what the container consults. */
let allowed = new Set<string>()
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    ...DEFAULT_AUTH,
    can: (p: string) => allowed.has(p),
  }),
}))

const toastSpy = {
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}
vi.mock('@/components/ui/toaster', () => ({ toast: toastSpy }))

const { ComplianceView } = await import('./compliance-view')
await import('./i18n')

const ALL_PERMS = [
  'compliance:dora:read',
  'compliance:dora:admin',
  'compliance:oscal:read',
  'compliance:oscal:admin',
  'compliance:depth:read',
  'compliance:depth:admin',
  // ⛔ AIMS NO ES DEPTH, y esta lista lo daba por hecho. Sin estos dos, dos celdas de este
  //    mismo fichero pasaban SÓLO porque la consola gateaba la familia AIMS con `depth`:
  //    estaban fijando el defecto. El motor exige `compliance:aims:*` en las cinco rutas
  //    `/aims/pack*` (`modules/compliance/compliance.go:509-513`).
  'compliance:aims:read',
  'compliance:aims:admin',
  'compliance:ccm:read',
  'compliance:ccm:admin',
]

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

/** ⛔ NO son `...PACK`. FedRAMP y AIMS tienen OTROS DTOs —`system_name`/`impact_level`
 *  (depthseam.go:316) y `organisation_name`/`standard` (aimspack.go:143)— y ninguno lleva
 *  `pack_type`. Un doble que heredara de `PACK` traería un campo que producción nunca manda, y
 *  una fila que lo leyera pasaría aquí y pintaría un hueco en el navegador. */
const FEDRAMP_PACK = {
  id: 'fr-1',
  system_name: 'Olivares Control Plane',
  impact_level: 'IL4',
  oscal_version: '1.1.3',
  error_count: 0,
  doc_sha256: 'f'.repeat(64),
  generated_by: 'ciso@example.com',
  generated_at: '2026-08-18T09:00:00Z',
  disclaimer: 'Draft FedRAMP pack.',
}

const AIMS_PACK = {
  id: 'ai-1',
  standard: 'ISO/IEC 42001:2023',
  organisation_name: 'Olivares AI',
  error_count: 0,
  doc_sha256: 'a'.repeat(64),
  generated_by: 'ciso@example.com',
  generated_at: '2026-08-18T09:05:00Z',
  disclaimer: 'Draft AIMS pack.',
}

/** TWO snapshots, and that is not padding. Drift compares against the PREDECESSOR
 *  (depthhandlers.go:1525-1535), so a one-snapshot fixture that answers 201 accepts
 *  a request production refuses — the exact "green because the double cannot
 *  reproduce what production can" failure the the model contrast caught (F3).*/
const SNAPSHOT = {
  id: 'cs-0',
  snapshot_at: '2026-08-04T14:00:00Z',
  note: 'the oldest — no predecessor, so it is NOT an offerable pinned target',
}

const SNAPSHOT_2 = {
  id: 'cs-1',
  snapshot_at: '2026-08-11T14:00:00Z',
  note: 'weekly',
}

/** A drift finding IN THE ENGINE'S OWN SHAPE (depthseam.go:301-313). Every fixture
 *  in the first version of this file was an EMPTY list, which is why a four-field
 *  name mismatch survived: an empty collection cannot disagree about field names. */
const DRIFT = {
  id: 'cd-1',
  snapshot_ref: 'cs-1',
  framework_id: 'nist_800_53',
  control_id: 'AC-2',
  title: 'Account Management',
  prev_status: 'satisfied',
  curr_status: 'not_satisfied',
  direction: 'regressed',
  detail: 'the attesting job stopped reporting',
  detected_at: '2026-08-11T14:05:00Z',
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

/** Route every read the container issues; anything unrouted is a 404 so an
 *  unexpected request fails loudly instead of rendering as an empty panel. */
function stubConsole(overrides: Record<string, () => Response> = {}) {
  return stubFetch((url, init) => {
    const path = new URL(url, 'https://console.invalid').pathname
    const key = `${init.method ?? 'GET'} ${path}`
    if (overrides[key]) return overrides[key]()
    switch (key) {
      case 'GET /v1/m/compliance/summary':
        return jsonResponse({ frameworks: [], disclaimer: 'summary' })
      case 'GET /v1/m/compliance/frameworks':
        return jsonResponse({
          items: [
            {
              id: 'nist_800_53',
              name: 'NIST SP 800-53',
              version: 'r5',
              authority: 'NIST',
              controls: 10,
            },
            {
              id: 'iso_27001',
              name: 'ISO/IEC 27001',
              version: '2022',
              authority: 'ISO',
              controls: 20,
            },
          ],
          disclaimer: 'frameworks',
        })
      case 'GET /v1/m/compliance/dora/register':
        return jsonResponse({ items: [REGISTER] })
      case 'GET /v1/m/compliance/dora/incidents':
        return jsonResponse({ items: [INCIDENT] })
      case 'GET /v1/m/compliance/oscal/profiles':
        return jsonResponse({ items: [PROFILE] })
      case 'GET /v1/m/compliance/depth/us-law':
        return jsonResponse({ items: [PACK] })
      case 'GET /v1/m/compliance/depth/sector':
        return jsonResponse({ items: [SECTOR_PACK] })
      case 'GET /v1/m/compliance/depth/ccm/snapshots':
        return jsonResponse({ items: [SNAPSHOT, SNAPSHOT_2] })
      case 'GET /v1/m/compliance/depth/ccm/drift':
        return jsonResponse({ items: [DRIFT] })
      default:
        return jsonResponse({ error: { message: `unrouted ${key}` } }, 404)
    }
  })
}

/** Mount the CONTAINER and open the regulatory-operations tab — the entry point a
 *  real operator uses, and the only one that can observe the tab being unwired. */
async function openRegOps(user: ReturnType<typeof userEvent.setup>) {
  renderIntel(<ComplianceView />)
  // `tabs.regops`, which is deliberately NOT `regops.title`: the trigger says
  // "Regulatory ops" and the panel heading says "Regulatory operations".
  await user.click(
    await screen.findByRole('tab', { name: /^Regulatory ops$/i }),
  )
}

/** The writes this tab issues, i.e. everything that is not a GET. */
function writes(mock: ReturnType<typeof stubFetch>) {
  return mock.mock.calls.filter(
    ([, init]) => ((init as RequestInit).method ?? 'GET') !== 'GET',
  ) as Array<[string, RequestInit]>
}

beforeEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  Object.values(toastSpy).forEach((fn) => fn.mockReset())
  allowed = new Set(ALL_PERMS)
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

    // This is the defect came to fix, and it is the assertion that fails if
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

// =============================================================================
// PART 3 — the screen, mounted from the container and pressed
// =============================================================================

describe('RegOps writes — reachable from the container, by pressing', () => {
  it('THE WITNESS: generating a register goes from the tab to the wire', async () => {
    const user = userEvent.setup()
    const mock = stubConsole({
      'POST /v1/m/compliance/dora/register': () => jsonResponse(REGISTER, 201),
    })
    await openRegOps(user)

    // Opening the dialog from the panel's own action — if the button is removed,
    // or the dialog is never rendered, or the tab is unwired from the container,
    // this line fails before any request is asserted.
    await user.click(
      await screen.findByRole('button', { name: /Generate register/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.type(
      within(dialog).getByLabelText(/Register document/i),
      '{{"a":1}',
    )
    await user.click(
      within(dialog).getByRole('button', { name: /^Generate register$/i }),
    )

    await waitFor(() => expect(writes(mock)).toHaveLength(1))
    const [url, init] = writes(mock)[0]
    expect(init.method).toBe('POST')
    expect(new URL(url, 'https://console.invalid').pathname).toBe(
      '/v1/m/compliance/dora/register',
    )
    // What the operator typed is what left the console — not a re-serialization.
    expect(init.body).toBe('{"a":1}')
    await waitFor(() => expect(toastSpy.success).toHaveBeenCalled())
  })

  it('THE WITNESS: classifying an incident reaches the engine with its reference', async () => {
    const user = userEvent.setup()
    const mock = stubConsole({
      'POST /v1/m/compliance/dora/incidents': () => jsonResponse(INCIDENT, 201),
    })
    await openRegOps(user)

    await user.click(
      await screen.findByRole('button', { name: /Classify incident/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.type(
      within(dialog).getByLabelText(/Incident reference/i),
      'ICT-9',
    )
    await user.type(
      within(dialog).getByLabelText(/Impact document/i),
      '{{"users":12}',
    )
    await user.click(
      within(dialog).getByRole('button', { name: /^Classify incident$/i }),
    )

    await waitFor(() => expect(writes(mock)).toHaveLength(1))
    const [url, init] = writes(mock)[0]
    const parsed = new URL(url, 'https://console.invalid')
    expect(parsed.pathname).toBe('/v1/m/compliance/dora/incidents')
    expect(parsed.searchParams.get('reference')).toBe('ICT-9')
    expect(init.body).toBe('{"users":12}')
  })

  it('THE WITNESS: detecting drift sends the PICKED snapshot as a query param', async () => {
    const user = userEvent.setup()
    const mock = stubConsole({
      'POST /v1/m/compliance/depth/ccm/drift': () =>
        jsonResponse({ items: [] }, 201),
    })
    await openRegOps(user)

    await user.click(
      await screen.findByRole('button', { name: /Detect drift/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('combobox', { name: /Snapshot to compare/i }),
    )
    await user.click(await screen.findByRole('option', { name: /2026-08-11/ }))
    await user.click(
      within(dialog).getByRole('button', { name: /^Detect drift$/i }),
    )

    await waitFor(() => expect(writes(mock)).toHaveLength(1))
    const [url, init] = writes(mock)[0]
    const parsed = new URL(url, 'https://console.invalid')
    // The end-to-end version of the contract fix: picked in the UI, arrives in the
    // query. If the client reverts to a body, the id is in `init.body` and the
    // engine ignores it — this asserts BOTH halves so neither can drift alone.
    expect(parsed.searchParams.get('snapshot_id')).toBe('cs-1')
    expect(init.body ?? null).toBeNull()
  })

  it('THE WITNESS: an artefact can be deleted, and only with the exact phrase', async () => {
    const user = userEvent.setup()
    const mock = stubConsole({
      'DELETE /v1/m/compliance/dora/register/dr-1': () => noContent(),
    })
    await openRegOps(user)

    const rowDelete = (
      await screen.findAllByRole('button', { name: /^Delete$/i })
    )[0]
    await user.click(rowDelete)
    const confirm = await screen.findByRole('button', {
      name: /Delete register/i,
    })
    const guard = document.getElementById('confirm-phrase') as HTMLInputElement

    // Blank, then WRONG, then right. The wrong-phrase step is the one that keeps a
    // mutant loosening the guard to "any non-empty text" from surviving.
    expect(confirm).toBeDisabled()
    await user.type(guard, 'delete')
    expect(confirm).toBeDisabled()
    expect(writes(mock)).toHaveLength(0)

    await user.clear(guard)
    await user.type(guard, 'DELETE')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(writes(mock)).toHaveLength(1))
    const [url, init] = writes(mock)[0]
    expect(init.method).toBe('DELETE')
    expect(new URL(url, 'https://console.invalid').pathname).toBe(
      '/v1/m/compliance/dora/register/dr-1',
    )
    await waitFor(() => expect(toastSpy.success).toHaveBeenCalled())
    expect(String(toastSpy.success.mock.calls[0][0])).toMatch(
      /Register deleted/i,
    )
  })

  /** ⛔ ESTA CELDA NACE DE UN MUTANTE QUE SOBREVIVIÓ, y por eso existe. Al fundir los dos bloques
   *  de familia de profundidad en una tabla, cambié la fila `sector` para que exportara por la ruta
   *  de `us-law` — un botón que descarga el artefacto EQUIVOCADO — y **las 54 celdas siguieron
   *  verdes**. La exportación estaba cubierta por un test de cliente directo (`exportSectorPack`
   *  llama a su ruta) y por «el botón existe»; ninguna de las dos ve que el botón de una familia
   *  llegue al cliente de la OTRA.
   *
   *  Es la misma lección que ya está escrita sobre la tabla GENERATORS —«un test de cable prueba
   *  que el cliente es correcto; sólo pulsar prueba que el BOTÓN llega a ese cliente»—, que allí se
   *  aprendió con un contraste y aquí faltaba para las lecturas. La exportación es un GET, así que
   *  `writes()` no la ve: hay que mirar TODAS las llamadas.
   */
  it('THE WITNESS: cada familia de profundidad exporta por SU ruta, no por la de la vecina', async () => {
    const user = userEvent.setup()
    const mock = stubConsole()
    await openRegOps(user)

    const rutaDe = (texto: string) =>
      new URL(String(texto), 'https://console.invalid').pathname

    // Las dos filas se distinguen por el artefacto que muestran, no por el orden: un índice fijo
    // haría verde a un mutante que sólo REORDENA las secciones.
    for (const [regulacion, esperada] of [
      [/dp-1|state/i, '/v1/m/compliance/depth/us-law/dp-1/export'],
      [/dp-2|overlay|sector/i, '/v1/m/compliance/depth/sector/dp-2/export'],
    ] as const) {
      mock.mockClear()
      const botones = await screen.findAllByRole('button', {
        name: /^Export$/i,
      })
      const fila = botones.find((b) =>
        regulacion.test(
          b.closest('div.flex')?.parentElement?.textContent ?? '',
        ),
      )
      expect(fila, `no encontré la fila de ${regulacion}`).toBeDefined()
      await user.click(fila as HTMLElement)
      await waitFor(() =>
        expect(mock.mock.calls.map(([u]) => rutaDe(u as string))).toContain(
          esperada,
        ),
      )
    }
  })

  /**
   * ⛔ ESTE DEFECTO LO METÍ YO HOY, y por eso la celda existe. Al añadir FedRAMP y AIMS copié la
   * etiqueta de la primera familia, y la página quedó con TRES botones llamados «Generate pack».
   * Para quien navega con lector de pantalla eso son tres botones indistinguibles que crean tres
   * artefactos regulatorios distintos.
   *
   * ⚠ Y lo cazó el testigo POR ACCIDENTE, con un «Found multiple elements» — un fallo que se lee
   * como un problema del test, no del producto, y que la reacción natural es «arreglar» pasando a
   * `getAllByRole`. Eso habría enterrado el defecto dejando la celda verde. Esta pregunta por lo
   * que importa —¿son distinguibles?— en vez de tropezarse con ello.
   *
   * En los SIETE idiomas: la colisión es de la traducción, no del código, así que una celda que
   * sólo mirase inglés dejaría pasar la misma colisión en alemán.
   */
  it('los generadores de las cuatro familias tienen nombres DISTINTOS', () => {
    const idiomas = ['en', 'es', 'de', 'fr', 'ja', 'ru', 'zh'] as const
    for (const idioma of idiomas) {
      // `as unknown as` y no un cast directo: el JSON importado tiene un tipo estructural
      // exacto —cada clave con su literal— y `regops` mezcla cadenas con objetos, así que no es
      // asignable a un `Record` uniforme. El cast no afloja nada que importe: lo que se lee son
      // cuatro claves que el gate de paridad ya obliga a existir en los siete idiomas.
      const regops = (
        bundlesCompliance[idioma] as unknown as {
          regops: Record<string, { generate: string }>
        }
      ).regops
      const etiquetas = ['usLaw', 'sector', 'fedramp', 'aims'].map(
        (f) => regops[f].generate,
      )
      expect(
        new Set(etiquetas).size,
        `colisión en ${idioma}: ${etiquetas}`,
      ).toBe(4)
    }
  })

  it('THE WITNESS: every plane offers its generator, not just the first', async () => {
    const user = userEvent.setup()
    stubConsole()
    await openRegOps(user)

    // One assertion per plane, so wiring four of five still fails. This is the
    // cell that goes red if a panel's action is dropped while its reads survive —
    // the exact state the base branch was in for all five.
    expect(
      await screen.findByRole('button', { name: /Generate register/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /Classify incident/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /Ingest document/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /Generate pack/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /Generate overlay/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /Generate KSI pack/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /Generate AIMS pack/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /Take snapshot/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /Detect drift/i }),
    ).toBeInTheDocument()
  })
})

// --- F4: EVERY caller pressed, not five of twelve ----------------------------

/** THE GAP THIS TABLE CLOSES, and it was proved, not guessed. The the model
 *  contrast of replaced the OSCAL call site with `generateUsLawPack(document,
 *  scopeNote)` — a button that creates the WRONG regulatory artefact — and it
 *  compiled and left all 40 cells green, because OSCAL was covered by a direct
 *  client test plus a "the button exists" assertion. A wire test proves the client
 *  is right; only a press proves the BUTTON reaches that client.
 *
 *  So every non-delete write is pressed from the container, and each row asserts the
 *  route it must reach — which is what tells a wrong-route mutant apart from a
 *  correct one. */
const GENERATORS: Array<{
  what: string
  open: RegExp
  field: RegExp
  confirm: RegExp
  path: string
  response: unknown
  /** Parámetros de consulta que TIENEN que viajar. Sólo FedRAMP lo usa hoy, y no es un extra:
   *  `impact_level` es opcional de forma y no de hecho — si no llega, el motor escribe `IL2`
   *  (depthhandlers.go:1233-1237) sin decírselo a nadie, así que el operador acaba con un pack
   *  autorizado a un nivel que no eligió. Un diálogo que perdiera el campo mandaría un POST
   *  perfectamente válido a la ruta correcta, y ni la ruta ni el cuerpo lo notarían. */
  query?: Record<string, string>
}> = [
  {
    what: 'OSCAL ingestion',
    open: /Ingest document/i,
    field: /OSCAL document/i,
    confirm: /^Ingest document$/i,
    path: '/v1/m/compliance/oscal/profiles',
    response: PROFILE,
  },
  {
    what: 'US state-law pack',
    open: /Generate pack/i,
    field: /Jurisdiction context/i,
    confirm: /^Generate pack$/i,
    path: '/v1/m/compliance/depth/us-law',
    response: PACK,
  },
  {
    what: 'sector overlay',
    open: /Generate overlay/i,
    field: /Sector context/i,
    confirm: /^Generate overlay$/i,
    path: '/v1/m/compliance/depth/sector',
    response: SECTOR_PACK,
  },
  {
    what: 'FedRAMP 20x KSI pack',
    open: /Generate KSI pack/i,
    field: /Authorization context/i,
    confirm: /^Generate KSI pack$/i,
    path: '/v1/m/compliance/depth/fedramp',
    response: FEDRAMP_PACK,
    query: { impact_level: 'IL2' },
  },
  {
    // ⛔ LA RUTA ES LA ASERCIÓN, y aquí más que en ninguna: AIMS es la única familia que NO
    //    cuelga de `/depth`. Componer la ruta desde el nombre de la familia —lo natural, y lo
    //    que una tabla invita a hacer— manda este POST a `/depth/aims`, que no existe: el
    //    operador ve un 404 donde esperaba un pack de certificación.
    what: 'ISO/IEC 42001 AIMS pack',
    open: /Generate AIMS pack/i,
    field: /Scope document/i,
    confirm: /^Generate AIMS pack$/i,
    path: '/v1/m/compliance/aims/pack',
    response: AIMS_PACK,
  },
]

describe('every generator is PRESSED, and reaches its own route', () => {
  it.each(GENERATORS)(
    '$what: the button reaches $path with the document verbatim',
    async ({ open, field, confirm, path, response, query }) => {
      const user = userEvent.setup()
      const mock = stubConsole({
        [`POST ${path}`]: () => jsonResponse(response, 201),
      })
      await openRegOps(user)

      await user.click(await screen.findByRole('button', { name: open }))
      const dialog = await screen.findByRole('dialog')
      await user.type(within(dialog).getByLabelText(field), '{{"a":1}')
      await user.click(within(dialog).getByRole('button', { name: confirm }))

      await waitFor(() => expect(writes(mock)).toHaveLength(1))
      const [url, init] = writes(mock)[0]
      expect(init.method).toBe('POST')
      // THE ROUTE IS THE ASSERTION. A button wired to a different generator sends a
      // well-formed request to the wrong place and creates the wrong artefact.
      const parsed = new URL(url, 'https://console.invalid')
      expect(parsed.pathname).toBe(path)
      expect(init.body).toBe('{"a":1}')
      for (const [k, v] of Object.entries(query ?? {})) {
        expect(parsed.searchParams.get(k), `falta ?${k}`).toBe(v)
      }
    },
  )
})

/** The five deletes, in the order their panels render. The INDEX alone would be
 *  magic, so each row also asserts the confirm dialog names the artefact it meant
 *  to delete: if the panels are ever reordered, that assertion fails loudly instead
 *  of quietly deleting a different regulatory record. */
const DELETES: Array<{
  what: string
  index: number
  dialog: RegExp
  confirm: RegExp
  path: string
}> = [
  {
    what: 'DORA register',
    index: 0,
    dialog: /Delete the register for Example Bank/i,
    confirm: /^Delete register$/i,
    path: '/v1/m/compliance/dora/register/dr-1',
  },
  {
    what: 'DORA incident',
    index: 1,
    dialog: /Delete the classification of ICT-2026-004/i,
    confirm: /^Delete classification$/i,
    path: '/v1/m/compliance/dora/incidents/di-1',
  },
  {
    what: 'OSCAL profile',
    index: 2,
    dialog: /Unregister nist_800_53/i,
    confirm: /^Unregister document$/i,
    path: '/v1/m/compliance/oscal/profiles/op-1',
  },
  {
    what: 'US state-law pack',
    index: 3,
    dialog: /Delete the TRAIGA pack/i,
    confirm: /^Delete pack$/i,
    path: '/v1/m/compliance/depth/us-law/dp-1',
  },
  {
    what: 'sector overlay pack',
    index: 4,
    dialog: /Delete the HIPAA pack/i,
    confirm: /^Delete pack$/i,
    path: '/v1/m/compliance/depth/sector/dp-2',
  },
]

describe('every delete is PRESSED, and removes its own artefact', () => {
  it.each(DELETES)(
    '$what: the row button reaches $path behind the typed phrase',
    async ({ index, dialog, confirm, path }) => {
      const user = userEvent.setup()
      const mock = stubConsole({ [`DELETE ${path}`]: () => noContent() })
      await openRegOps(user)

      const buttons = await screen.findAllByRole('button', {
        name: /^Delete$/i,
      })
      expect(buttons.length).toBe(DELETES.length)
      await user.click(buttons[index])

      // The dialog names the artefact — this is what makes the index meaningful.
      expect(await screen.findByText(dialog)).toBeInTheDocument()
      const guard = document.getElementById(
        'confirm-phrase',
      ) as HTMLInputElement
      await user.type(guard, 'DELETE')
      await user.click(screen.getByRole('button', { name: confirm }))

      await waitFor(() => expect(writes(mock)).toHaveLength(1))
      const [url, init] = writes(mock)[0]
      expect(init.method).toBe('DELETE')
      expect(new URL(url, 'https://console.invalid').pathname).toBe(path)
    },
  )
})

// --- F1: a real drift finding, in the engine's own field names ----------------

describe('CCM drift findings render the values the engine sent', () => {
  it('shows framework, control, statuses and detail — not "? → ?"', async () => {
    const user = userEvent.setup()
    stubConsole()
    await openRegOps(user)

    // Every one of these came back undefined while the console read `framework`,
    // `from_status`, `to_status` and `note`: a well-formed answer drawn as
    // placeholders. The assertion is on the VALUES, so a renamed field is red.
    // Scoped to the drift block: the OSCAL panel above also shows `nist_800_53`,
    // and an unscoped query would be satisfied by a stranger — the finding would
    // "pass" with the drift list still drawing placeholders.
    const drift = (await screen.findByText('Detected drift'))
      .parentElement as HTMLElement
    expect(within(drift).getByText(/nist_800_53/)).toBeInTheDocument()
    expect(within(drift).getByText(/AC-2/)).toBeInTheDocument()
    expect(
      within(drift).getByText(/satisfied → not_satisfied/),
    ).toBeInTheDocument()
    expect(within(drift).getByText(/Account Management/)).toBeInTheDocument()
    expect(
      within(drift).getByText(/the attesting job stopped reporting/),
    ).toBeInTheDocument()
    expect(within(drift).getByText(/regressed/)).toBeInTheDocument()
    // And the placeholder pair must be GONE, or the cell above could pass on a row
    // that renders both the values and the question marks.
    expect(within(drift).queryByText(/\? → \?/)).toBeNull()
  })

  it('does not offer a pinned comparison the engine must refuse', async () => {
    const user = userEvent.setup()
    stubConsole()
    await openRegOps(user)

    await user.click(
      await screen.findByRole('button', { name: /Detect drift/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('combobox', { name: /Snapshot to compare/i }),
    )
    // The OLDEST snapshot has no predecessor by construction, so pinning it is
    // unsatisfiable (422). It is not on the menu; the newer one is.
    expect(screen.queryByRole('option', { name: /2026-08-04/ })).toBeNull()
    expect(
      screen.getByRole('option', { name: /2026-08-11/ }),
    ).toBeInTheDocument()
  })

  it('refuses to offer drift detection at all with a single snapshot', async () => {
    const user = userEvent.setup()
    stubConsole({
      'GET /v1/m/compliance/depth/ccm/snapshots': () =>
        jsonResponse({ items: [SNAPSHOT] }),
    })
    await openRegOps(user)

    // Disabled and explained, never hidden: a missing button and an impossible one
    // are different news.
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: /Detect drift/i }),
      ).toBeDisabled(),
    )
  })
})

describe('AIMS no se sirve bajo depth — las dos caras del defecto', () => {
  // ⛔ EL MOTOR EXIGE `compliance:aims:*` EN `/aims/pack*` (modules/compliance/compliance.go:509-513),
  //    y la consola gateaba esa familia con `compliance:depth:read`. Eso producía DOS fallos
  //    visibles para el usuario, y estas dos celdas son uno cada uno. Antes del arreglo, la
  //    primera fallaba (no veía nada) y la segunda también (veía AIMS y cada llamada daba 403).
  //
  //    Que la batería pasara ANTES no era una prueba de que estuviera bien: dos de sus celdas
  //    concedían sólo `depth:*` y esperaban el generador de AIMS, es decir, FIJABAN el defecto.

  it('CARA A: con aims:read y SIN depth:read, la familia AIMS se ve', async () => {
    const user = userEvent.setup()
    allowed = new Set(['compliance:aims:read'])
    stubConsole()
    await openRegOps(user)

    // Ancla POSITIVA y esperada primero: sin ella, las ausencias de abajo se cumplirían en el
    // primer tick contra un árbol todavía vacío y la celda no mediría nada.
    expect(
      await screen.findByText(/ISO\/IEC 42001 AIMS packs/i),
    ).toBeInTheDocument()

    // Y NO se cuela ninguna familia de `depth`, que este usuario no puede leer.
    expect(screen.queryByText(/US state-law packs/i)).toBeNull()
    expect(screen.queryByText(/Sector overlays/i)).toBeNull()
    expect(screen.queryByText(/FedRAMP 20x KSI packs/i)).toBeNull()
    // Sin `aims:admin` tampoco aparece su verbo.
    expect(
      screen.queryByRole('button', { name: /Generate AIMS pack/i }),
    ).toBeNull()
  })

  it('CARA B: con depth:read y SIN aims:read, la familia AIMS NO se ve', async () => {
    const user = userEvent.setup()
    allowed = new Set(['compliance:depth:read'])
    stubConsole()
    await openRegOps(user)

    // Ancla positiva: las familias de `depth` SÍ están.
    expect(await screen.findByText(/US state-law packs/i)).toBeInTheDocument()
    expect(screen.getByText(/Sector overlays/i)).toBeInTheDocument()

    // Y AIMS no. Antes del arreglo se veía, y cada llamada suya devolvía 403 — una pantalla que
    // ofrece lo que el motor va a negar le enseña al operador que el producto está roto.
    expect(screen.queryByText(/ISO\/IEC 42001 AIMS packs/i)).toBeNull()
    expect(
      screen.queryByRole('button', { name: /Generate AIMS pack/i }),
    ).toBeNull()
  })

  it('CARA C: sin ninguno de los dos, el panel entero desaparece', async () => {
    const user = userEvent.setup()
    allowed = new Set(['compliance:dora:read'])
    stubConsole()
    await openRegOps(user)

    expect(
      await screen.findByText(/DORA Register of Information/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/ISO\/IEC 42001 AIMS packs/i)).toBeNull()
    expect(screen.queryByText(/US state-law packs/i)).toBeNull()
  })
})

describe('RegOps permissions — the verb is gated separately from the read', () => {
  it('shows every panel but NO write to a reader without the admin verbs', async () => {
    const user = userEvent.setup()
    allowed = new Set(ALL_PERMS.filter((p) => p.endsWith(':read')))
    stubConsole()
    await openRegOps(user)

    // The reads render...
    expect(
      await screen.findByText(/DORA Register of Information/i),
    ).toBeInTheDocument()
    expect(screen.getByText(/OSCAL profiles and SSPs/i)).toBeInTheDocument()
    // ...and not one verb does. A console that renders an action it cannot
    // perform teaches the operator that the product is broken.
    expect(
      screen.queryByRole('button', { name: /Generate register/i }),
    ).toBeNull()
    expect(
      screen.queryByRole('button', { name: /Classify incident/i }),
    ).toBeNull()
    expect(
      screen.queryByRole('button', { name: /Ingest document/i }),
    ).toBeNull()
    expect(screen.queryByRole('button', { name: /Generate pack/i })).toBeNull()
    expect(
      screen.queryByRole('button', { name: /Generate overlay/i }),
    ).toBeNull()
    expect(screen.queryByRole('button', { name: /Take snapshot/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /Detect drift/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /^Delete$/i })).toBeNull()
  })

  /** One row per plane, each with the verbs it MUST show and the verbs it must not.
   *  The engine requires a different permission per plane
   *  (compliance.go:480-534), so a single `canAdmin` threaded to every panel — the
   *  easy mistake, and the state this tab was in before — passes a
   *  "reader sees nothing" test and fails every row here. */
  const PLANES: Array<{ plane: string; admin: string; shows: RegExp[] }> = [
    {
      plane: 'DORA',
      admin: 'compliance:dora:admin',
      shows: [/Generate register/i, /Classify incident/i],
    },
    {
      plane: 'OSCAL',
      admin: 'compliance:oscal:admin',
      shows: [/Ingest document/i],
    },
    {
      plane: 'depth packs',
      admin: 'compliance:depth:admin',
      shows: [/Generate pack/i, /Generate overlay/i],
    },
    {
      plane: 'CCM',
      admin: 'compliance:ccm:admin',
      shows: [/Take snapshot/i, /Detect drift/i],
    },
  ]

  const EVERY_VERB = [
    /Generate register/i,
    /Classify incident/i,
    /Ingest document/i,
    /Generate pack/i,
    /Generate overlay/i,
    /Take snapshot/i,
    /Detect drift/i,
  ]

  it.each(PLANES)(
    "$plane: $admin unlocks its own verbs and NOT another plane's",
    async ({ admin, shows }) => {
      const user = userEvent.setup()
      allowed = new Set([
        ...ALL_PERMS.filter((p) => p.endsWith(':read')),
        admin,
      ])
      stubConsole()
      await openRegOps(user)

      // Positive: every verb this permission is supposed to unlock.
      for (const verb of shows) {
        expect(await screen.findByRole('button', { name: verb })).toBeVisible()
      }
      // Negative: every OTHER verb on the tab stays hidden. Enumerated from the
      // full list rather than hand-picked, so a verb added later without a plane
      // of its own cannot quietly escape the check.
      const mine = shows.map(String)
      for (const verb of EVERY_VERB.filter((v) => !mine.includes(String(v)))) {
        expect(screen.queryByRole('button', { name: verb })).toBeNull()
      }
    },
  )
})

describe('RegOps failure tones — a boundary is not a fault', () => {
  it('draws a 501 generator as the add-on boundary, keeping the document', async () => {
    const user = userEvent.setup()
    stubConsole({
      'POST /v1/m/compliance/dora/register': () =>
        jsonResponse(
          {
            error: {
              message:
                'DORA Register of Information generation requires the Olivares enterprise add-on (doraregister); not linked in this build',
            },
          },
          501,
        ),
    })
    await openRegOps(user)

    await user.click(
      await screen.findByRole('button', { name: /Generate register/i }),
    )
    const dialog = await screen.findByRole('dialog')
    const field = within(dialog).getByLabelText(/Register document/i)
    await user.type(field, '{{"a":1}')
    await user.click(
      within(dialog).getByRole('button', { name: /^Generate register$/i }),
    )

    // Explained where the operator is standing...
    expect(
      await within(dialog).findByText(/enterprise add-on that is not linked/i),
    ).toBeInTheDocument()
    // ...with no red error toast contradicting it...
    expect(toastSpy.error).not.toHaveBeenCalled()
    // ...and the document they pasted still there, because a build that cannot
    // process it has no business discarding it.
    expect((field as HTMLTextAreaElement).value).toBe('{"a":1}')
  })

  it('relays a COMMERCIAL 403 in the engine words, not "not authorized"', async () => {
    const user = userEvent.setup()
    stubConsole({
      'POST /v1/m/compliance/dora/register': () =>
        jsonResponse(
          {
            error: {
              message:
                'the "compliance-packs" add-on is required for DORA register generation; reading, verifying and exporting your data are unaffected',
            },
          },
          403,
        ),
    })
    await openRegOps(user)

    await user.click(
      await screen.findByRole('button', { name: /Generate register/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.type(
      within(dialog).getByLabelText(/Register document/i),
      '{{"a":1}',
    )
    await user.click(
      within(dialog).getByRole('button', { name: /^Generate register$/i }),
    )

    // A 403 here is a purchase boundary, measured on this module by. Telling
    // the operator "not authorized" sends them to ask for a permission nobody can
    // grant them.
    await waitFor(() => expect(toastSpy.warning).toHaveBeenCalled())
    expect(String(toastSpy.warning.mock.calls[0][0])).toMatch(
      /add-on is required/i,
    )
    expect(toastSpy.error).not.toHaveBeenCalled()
  })

  it('still reports a REAL failure loudly — the calm tone is not a blanket mute', async () => {
    const user = userEvent.setup()
    stubConsole({
      'POST /v1/m/compliance/dora/register': () =>
        jsonResponse({ error: { message: 'store unavailable' } }, 500),
    })
    await openRegOps(user)

    await user.click(
      await screen.findByRole('button', { name: /Generate register/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.type(
      within(dialog).getByLabelText(/Register document/i),
      '{{"a":1}',
    )
    await user.click(
      within(dialog).getByRole('button', { name: /^Generate register$/i }),
    )

    await waitFor(() => expect(toastSpy.error).toHaveBeenCalled())
    expect(
      within(dialog).queryByText(/enterprise add-on that is not linked/i),
    ).toBeNull()
  })

  it('does not claim an artefact exists when the engine returned none', async () => {
    const user = userEvent.setup()
    stubConsole({
      // 201 with no id: nothing to point an auditor at.
      'POST /v1/m/compliance/dora/register': () => jsonResponse({}, 201),
    })
    await openRegOps(user)

    await user.click(
      await screen.findByRole('button', { name: /Generate register/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.type(
      within(dialog).getByLabelText(/Register document/i),
      '{{"a":1}',
    )
    await user.click(
      within(dialog).getByRole('button', { name: /^Generate register$/i }),
    )

    await waitFor(() => expect(toastSpy.warning).toHaveBeenCalled())
    expect(String(toastSpy.warning.mock.calls[0][0])).toMatch(
      /Nothing is confirmed as created/i,
    )
    expect(toastSpy.success).not.toHaveBeenCalled()
  })
})

describe('CCM snapshot scope — the safer-looking option is the larger one', () => {
  it('says the empty selection means EVERY framework, and sends no list', async () => {
    const user = userEvent.setup()
    const mock = stubConsole({
      'POST /v1/m/compliance/depth/ccm/snapshot': () =>
        jsonResponse(SNAPSHOT, 201),
    })
    await openRegOps(user)

    await user.click(
      await screen.findByRole('button', { name: /Take snapshot/i }),
    )
    const dialog = await screen.findByRole('dialog')
    // The engine SILENTLY SKIPS an unknown framework id (depthhandlers.go:216-220),
    // so the console offers the catalog rather than a text box — and states which
    // of the two very different actions the current selection is.
    expect(
      within(dialog).getByText(/will cover EVERY framework in the catalog/i),
    ).toBeInTheDocument()
    await user.click(
      within(dialog).getByRole('button', { name: /^Take snapshot$/i }),
    )

    await waitFor(() => expect(writes(mock)).toHaveLength(1))
    const [, init] = writes(mock)[0]
    // `frameworks` absent, not []: absence is how "all" travels (`:821-826`).
    expect(Object.keys(JSON.parse(String(init.body)))).toEqual([])
  })

  it('sends exactly the frameworks ticked, from the catalog the engine knows', async () => {
    const user = userEvent.setup()
    const mock = stubConsole({
      'POST /v1/m/compliance/depth/ccm/snapshot': () =>
        jsonResponse(SNAPSHOT, 201),
    })
    await openRegOps(user)

    await user.click(
      await screen.findByRole('button', { name: /Take snapshot/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('checkbox', { name: /NIST SP 800-53/i }),
    )
    expect(
      within(dialog).getByText(/will cover the 1 framework/i),
    ).toBeInTheDocument()
    await user.click(
      within(dialog).getByRole('button', { name: /^Take snapshot$/i }),
    )

    await waitFor(() => expect(writes(mock)).toHaveLength(1))
    const [, init] = writes(mock)[0]
    expect(JSON.parse(String(init.body)).frameworks).toEqual(['nist_800_53'])
  })
})
