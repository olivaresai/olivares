// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console's format list is a closed set mirroring the engine's registry. When it
// drifted, the effect was not cosmetic: the selector could not offer the format at
// all, and the NDJSON classifier decided the Accept header and the saved filename —
// so a format missing here downloads as `text/plain` with a `.log` extension even if
// the engine streamed NDJSON.
//
// The comparison is against the COMMITTED OpenAPI snapshot rather than a second
// TypeScript literal, because a literal-vs-literal expectation proves only that
// someone edited both halves of the same file. The snapshot is generated from the
// engine — `task openapi:dump` runs `olivares openapi`, whose enum is built from
// core/audit.Formats() — and CI fails on any drift between generator and snapshot
// (`task openapi:check`, wired into mainline-ci and release since). That makes it
// the one artifact within this package's reach that the engine actually owns, so a
// format added in Go and forgotten here fails on this side.
import { describe, expect, it } from 'vitest'
import openapi from '../../../openapi/openapi.json'
import { exportFilename } from './export'
import { EXPORT_FORMATS, type ExportFormat } from './types'

/** The engine-owned enum for the GET /v1/audit/export `format` parameter. */
function engineFormats(): string[] {
  const doc = openapi as unknown as {
    paths: Record<
      string,
      { get: { parameters: { name: string; schema: { enum?: string[] } }[] } }
    >
  }
  const params = doc.paths['/v1/audit/export']?.get?.parameters
  if (!params) {
    throw new Error('the OpenAPI snapshot no longer describes GET /v1/audit/export')
  }
  const format = params.find((p) => p.name === 'format')
  if (!format?.schema?.enum?.length) {
    throw new Error('the OpenAPI snapshot has no enum for the export format parameter')
  }
  return format.schema.enum
}

describe('audit export formats', () => {
  it('offers exactly the formats the engine accepts, in the engine order', () => {
    expect([...EXPORT_FORMATS]).toEqual(engineFormats())
  })

  it('saves the JSON formats as .ndjson and the line formats as .log', () => {
    const ndjson: ExportFormat[] = ['otlp', 'otlp_envelope', 'otlp_log_record', 'ocsf']
    const text: ExportFormat[] = ['cef', 'leef', 'syslog']
    for (const f of ndjson) {
      expect(exportFilename(f)).toBe(`olivares-audit-${f}.ndjson`)
    }
    for (const f of text) {
      expect(exportFilename(f)).toBe(`olivares-audit-${f}.log`)
    }
    // Cardinality in both directions: every engine format is classified into exactly
    // one of the two lists, and neither list names a format the engine does not
    // accept. The count alone would stay satisfied if a stale entry replaced a real
    // one, which is how a classifier drifts without failing.
    expect([...ndjson, ...text].sort()).toEqual([...EXPORT_FORMATS].sort())
    expect(ndjson.length + text.length).toBe(EXPORT_FORMATS.length)
  })
})
