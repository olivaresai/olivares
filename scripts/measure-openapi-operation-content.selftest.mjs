#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// External black-box battery for measure-openapi-operation-content.mjs. Every
// subject and fixture lives in a disposable directory; text mutations refuse an
// absent or repeated anchor so a mutant can never report a vacuous green.

import { spawnSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const SUBJECT =
  process.env.OPENAPI_METER_SELFTEST_SUBJECT ??
  path.join(HERE, 'measure-openapi-operation-content.mjs')
const SELF = fileURLToPath(import.meta.url)
const EXTENSION = 'x-olivares-request-body-disposition'

function cannotLook(message) {
  const error = new Error(message)
  error.exitCode = 2
  return error
}

function finding(message) {
  const error = new Error(message)
  error.exitCode = 1
  return error
}

function occurrenceCount(source, anchor) {
  if (anchor === '') throw cannotLook('mutation anchor must not be empty')
  let count = 0
  let offset = 0
  while (true) {
    const found = source.indexOf(anchor, offset)
    if (found === -1) return count
    count++
    offset = found + anchor.length
  }
}

function replaceExactlyOnce(source, anchor, replacement, name) {
  const count = occurrenceCount(source, anchor)
  if (count !== 1) {
    throw cannotLook(
      `mutation anchor ${name} matched ${count} times; expected exactly once`,
    )
  }
  return source.replace(anchor, replacement)
}

function fixtureSource() {
  return `${JSON.stringify(
    {
      paths: {
        '/v1/m/demo/typed': {
          post: {
            summary: 'Create a typed item',
            [EXTENSION]: 'schema-published',
            requestBody: {
              required: true,
              content: {
                'application/json': {
                  schema: {
                    type: 'object',
                    properties: { name: { type: 'string' } },
                  },
                },
              },
            },
          },
        },
        '/v1/m/demo/raw': {
          post: {
            summary: 'Import opaque JSON',
            [EXTENSION]: 'opaque-body',
            requestBody: {
              required: true,
              description: 'Bounded raw JSON interpreted by the handler.',
              content: { 'application/json': { schema: {} } },
            },
          },
        },
        '/v1/m/demo/ping': {
          post: {
            summary: 'Trigger a bodyless action',
            [EXTENSION]: 'bodyless',
          },
        },
        '/v1/m/demo/items': {
          get: { summary: 'List items' },
        },
      },
    },
    null,
    2,
  )}\n`
}

function runMeter(meter, spec, options = []) {
  const result = spawnSync(process.execPath, [meter, ...options, spec], {
    encoding: 'utf8',
  })
  if (result.error) throw cannotLook(`cannot execute meter: ${result.error.message}`)
  if (result.signal !== null) {
    throw cannotLook(`meter terminated by signal ${result.signal}`)
  }
  return result
}

function expectExact(actual, expected, label) {
  if (actual !== expected) {
    throw finding(`${label}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`)
  }
}

function expect(condition, message) {
  if (!condition) throw finding(message)
}

function writeFixture(directory, name, source) {
  const target = path.join(directory, `${name}.json`)
  fs.writeFileSync(target, source)
  return target
}

function copySubject(source, target) {
  try {
    fs.copyFileSync(source, target)
  } catch (error) {
    throw cannotLook(`cannot copy meter subject ${source}: ${error.code ?? error.message}`)
  }
}

function parseMeasurement(result, label) {
  try {
    return JSON.parse(result.stdout)
  } catch (error) {
    throw finding(`${label}: stdout is not JSON: ${error.message}`)
  }
}

function expectedHuman(
  spec,
  {
    schemaPublished = 1,
    opaqueBody = 1,
    bodyless = 1,
    unclassified = 0,
    missing = 0,
    invalid = 0,
    declarationCeiling = '2/3 (66.7%)',
    typedCeiling = '1/3 (33.3%)',
    verdict = 'OK',
  } = {},
) {
  return `${[
    `OpenAPI operation content: ${spec}`,
    'operations=4 summaries_present=4 summaries_distinct=4 summary_distinctness=100%',
    'requestBody mutation-verbs(POST|PUT|PATCH|DELETE)=2/3 (66.7%)',
    'requestBody payload-verbs(POST|PUT|PATCH)=2/3 (66.7%)',
    `dispositions schema-published=${schemaPublished} opaque-body=${opaqueBody} bodyless=${bodyless} unclassified=${unclassified} missing=${missing} invalid=${invalid}`,
    `requestBody declaration ceiling=${declarationCeiling}`,
    `typed schema ceiling=${typedCeiling}`,
    `verdict=${verdict}`,
    'limitation=ceilings are unavailable until every mutation is classified and every disposition is internally consistent',
  ].join('\n')}\n`
}

function expectedInconsistentHuman(spec) {
  const unavailable =
    'UNAVAILABLE (requires unclassified=0 and complete, consistent dispositions (consistency_findings=1))'
  return expectedHuman(spec, {
    declarationCeiling: unavailable,
    typedCeiling: unavailable,
    verdict: 'FINDING',
  })
}

function runAnchorProbe() {
  replaceExactlyOnce(fixtureSource(), '"summary":', '"summary":', 'duplicate-summary')
}

function runBattery() {
  const temporary = fs.mkdtempSync(
    path.join(process.env.TMPDIR || os.tmpdir(), 'openapi-operation-content-selftest-'),
  )
  try {
    const meter = path.join(temporary, 'measure-openapi-operation-content.mjs')
    copySubject(SUBJECT, meter)
    const baseline = fixtureSource()

    const greenPath = writeFixture(temporary, 'green', baseline)
    const green = runMeter(meter, greenPath)
    expectExact(green.status, 0, 'green control exit status')
    expectExact(green.stdout, expectedHuman(greenPath), 'green control stdout')
    expectExact(green.stderr, '', 'green control stderr')
    const greenJSON = runMeter(meter, greenPath, ['--json'])
    expectExact(greenJSON.status, 0, 'green JSON control exit status')
    expectExact(greenJSON.stderr, '', 'green JSON control stderr')
    const greenMeasurement = parseMeasurement(greenJSON, 'green control')
    expect(greenMeasurement.verdict === 'OK', 'green control did not report verdict=OK')
    expect(
      greenMeasurement.requestBody_declaration_ceiling.available === true,
      'green control did not publish the requestBody declaration ceiling',
    )
    expect(
      greenMeasurement.typed_schema_ceiling.available === true,
      'green control did not publish the typed schema ceiling',
    )
    expect(
      greenMeasurement.requestBody_declaration_ceiling.numerator >
        greenMeasurement.typed_schema_ceiling.numerator,
      'green control did not keep the opaque declaration outside the typed ceiling',
    )

    const opaqueRequiredAnchor =
      '"required": true,\n          "description": "Bounded raw JSON interpreted by the handler."'
    const requiredAbsent = replaceExactlyOnce(
      baseline,
      opaqueRequiredAnchor,
      '"description": "Bounded raw JSON interpreted by the handler."',
      'required-absent-green',
    )
    const requiredAbsentPath = writeFixture(
      temporary,
      'required-absent-green',
      requiredAbsent,
    )
    const requiredAbsentResult = runMeter(meter, requiredAbsentPath)
    expectExact(requiredAbsentResult.status, 0, 'required-absent green exit status')
    expectExact(
      requiredAbsentResult.stdout,
      expectedHuman(requiredAbsentPath),
      'required-absent green stdout',
    )
    expectExact(requiredAbsentResult.stderr, '', 'required-absent green stderr')

    const opaqueAnchor = `"${EXTENSION}": "opaque-body"`
    const opaqueToBodyless = replaceExactlyOnce(
      baseline,
      opaqueAnchor,
      `"${EXTENSION}": "bodyless"`,
      'opaque-to-bodyless',
    )
    const bodylessPath = writeFixture(
      temporary,
      'opaque-to-bodyless',
      opaqueToBodyless,
    )
    const bodylessResult = runMeter(meter, bodylessPath)
    expectExact(bodylessResult.status, 1, 'opaque-to-bodyless exit status')
    expectExact(
      bodylessResult.stdout,
      expectedHuman(bodylessPath, {
        opaqueBody: 0,
        bodyless: 2,
        declarationCeiling:
          'UNAVAILABLE (requires unclassified=0 and complete, consistent dispositions (consistency_findings=1))',
        typedCeiling:
          'UNAVAILABLE (requires unclassified=0 and complete, consistent dispositions (consistency_findings=1))',
        verdict: 'FINDING',
      }),
      'opaque-to-bodyless stdout',
    )
    expectExact(
      bodylessResult.stderr,
      'measure-openapi-operation-content: FINDING — BODYLESS_WITH_REQUEST_BODY POST /v1/m/demo/raw: bodyless must not publish requestBody\n',
      'opaque-to-bodyless message',
    )

    const pendingAnchor = `"${EXTENSION}": "opaque-body"`
    const pending = replaceExactlyOnce(
      baseline,
      pendingAnchor,
      `"${EXTENSION}": "unclassified"`,
      'ceiling-with-pending',
    )
    const pendingPath = writeFixture(temporary, 'ceiling-with-pending', pending)
    const pendingResult = runMeter(meter, pendingPath)
    expectExact(pendingResult.status, 1, 'ceiling-with-pending exit status')
    expectExact(
      pendingResult.stdout,
      expectedHuman(pendingPath, {
        opaqueBody: 0,
        unclassified: 1,
        declarationCeiling:
          'UNAVAILABLE (requires unclassified=0 and complete, consistent dispositions (unclassified=1, consistency_findings=1))',
        typedCeiling:
          'UNAVAILABLE (requires unclassified=0 and complete, consistent dispositions (unclassified=1, consistency_findings=1))',
        verdict: 'FINDING',
      }),
      'ceiling-with-pending stdout',
    )
    expectExact(
      pendingResult.stderr,
      'measure-openapi-operation-content: FINDING — UNCLASSIFIED POST /v1/m/demo/raw: disposition is pending handler review\n' +
        'measure-openapi-operation-content: FINDING — UNCLASSIFIED_WITH_REQUEST_BODY POST /v1/m/demo/raw: unclassified must not publish requestBody before handler review\n',
      'ceiling-with-pending message',
    )
    const pendingJSON = runMeter(meter, pendingPath, ['--json'])
    expectExact(pendingJSON.status, 1, 'ceiling-with-pending JSON exit status')
    expectExact(
      pendingJSON.stderr,
      pendingResult.stderr,
      'ceiling-with-pending JSON message',
    )
    const pendingMeasurement = parseMeasurement(pendingJSON, 'ceiling-with-pending')
    expect(
      pendingMeasurement.requestBody_declaration_ceiling.available === false &&
        pendingMeasurement.typed_schema_ceiling.available === false,
      'ceiling-with-pending mutant survived: a ceiling remained available',
    )
    expect(
      pendingMeasurement.requestBody_declaration_ceiling.numerator === null &&
        pendingMeasurement.typed_schema_ceiling.numerator === null,
      'ceiling-with-pending mutant survived: an unavailable ceiling leaked a numerator',
    )

    const missing = replaceExactlyOnce(
      baseline,
      `,\n        "${EXTENSION}": "bodyless"`,
      '',
      'extension-absent',
    )
    const missingPath = writeFixture(temporary, 'extension-absent', missing)
    const missingResult = runMeter(meter, missingPath)
    expectExact(missingResult.status, 1, 'extension-absent exit status')
    expectExact(
      missingResult.stdout,
      expectedHuman(missingPath, {
        bodyless: 0,
        missing: 1,
        declarationCeiling:
          'UNAVAILABLE (requires unclassified=0 and complete, consistent dispositions (missing=1))',
        typedCeiling:
          'UNAVAILABLE (requires unclassified=0 and complete, consistent dispositions (missing=1))',
        verdict: 'FINDING',
      }),
      'extension-absent stdout',
    )
    expectExact(
      missingResult.stderr,
      `measure-openapi-operation-content: FINDING — DISPOSITION_MISSING POST /v1/m/demo/ping: ${EXTENSION} is absent\n`,
      'extension-absent message',
    )

    const invented = replaceExactlyOnce(
      baseline,
      '"schema": {}',
      '"schema": {"type":"object","properties":{"invented":{"type":"string"}}}',
      'opaque-invented-schema',
    )
    const inventedPath = writeFixture(
      temporary,
      'opaque-invented-schema',
      invented,
    )
    const inventedResult = runMeter(meter, inventedPath)
    expectExact(inventedResult.status, 1, 'opaque-invented-schema exit status')
    expectExact(
      inventedResult.stdout,
      expectedHuman(inventedPath, {
        declarationCeiling:
          'UNAVAILABLE (requires unclassified=0 and complete, consistent dispositions (consistency_findings=1))',
        typedCeiling:
          'UNAVAILABLE (requires unclassified=0 and complete, consistent dispositions (consistency_findings=1))',
        verdict: 'FINDING',
      }),
      'opaque-invented-schema stdout',
    )
    expectExact(
      inventedResult.stderr,
      'measure-openapi-operation-content: FINDING — OPAQUE_BODY_INVENTED_PROPERTIES POST /v1/m/demo/raw: application/json schema publishes named properties\n',
      'opaque-invented-schema message',
    )

    const typedRequiredAnchor =
      '"requestBody": {\n          "required": true,\n          "content": {'
    const typedRequired = replaceExactlyOnce(
      baseline,
      typedRequiredAnchor,
      '"requestBody": {\n          "required": "yes",\n          "content": {',
      'schema-required-not-boolean',
    )
    const typedRequiredPath = writeFixture(
      temporary,
      'schema-required-not-boolean',
      typedRequired,
    )
    const typedRequiredResult = runMeter(meter, typedRequiredPath)
    expectExact(typedRequiredResult.status, 1, 'schema-required-not-boolean exit status')
    expectExact(
      typedRequiredResult.stdout,
      expectedInconsistentHuman(typedRequiredPath),
      'schema-required-not-boolean stdout',
    )
    expectExact(
      typedRequiredResult.stderr,
      'measure-openapi-operation-content: FINDING — SCHEMA_PUBLISHED_REQUIRED_NOT_BOOLEAN POST /v1/m/demo/typed: requestBody.required must be boolean when present; got "yes"\n',
      'schema-required-not-boolean message',
    )

    const opaqueRequired = replaceExactlyOnce(
      baseline,
      opaqueRequiredAnchor,
      '"required": 1,\n          "description": "Bounded raw JSON interpreted by the handler."',
      'opaque-required-not-boolean',
    )
    const opaqueRequiredPath = writeFixture(
      temporary,
      'opaque-required-not-boolean',
      opaqueRequired,
    )
    const opaqueRequiredResult = runMeter(meter, opaqueRequiredPath)
    expectExact(opaqueRequiredResult.status, 1, 'opaque-required-not-boolean exit status')
    expectExact(
      opaqueRequiredResult.stdout,
      expectedInconsistentHuman(opaqueRequiredPath),
      'opaque-required-not-boolean stdout',
    )
    expectExact(
      opaqueRequiredResult.stderr,
      'measure-openapi-operation-content: FINDING — OPAQUE_BODY_REQUIRED_NOT_BOOLEAN POST /v1/m/demo/raw: requestBody.required must be boolean when present; got 1\n',
      'opaque-required-not-boolean message',
    )

    const typedMediaAnchor =
      '"application/json": {\n              "schema": {\n                "type": "object",'
    const multipleMedia = replaceExactlyOnce(
      baseline,
      typedMediaAnchor,
      '"application/xml": {"schema":{"type":"object"}},\n            ' +
        typedMediaAnchor,
      'multiple-media-types',
    )
    const multipleMediaPath = writeFixture(
      temporary,
      'multiple-media-types',
      multipleMedia,
    )
    const multipleMediaResult = runMeter(meter, multipleMediaPath)
    expectExact(multipleMediaResult.status, 1, 'multiple-media-types exit status')
    expectExact(
      multipleMediaResult.stdout,
      expectedInconsistentHuman(multipleMediaPath),
      'multiple-media-types stdout',
    )
    expectExact(
      multipleMediaResult.stderr,
      'measure-openapi-operation-content: FINDING — SCHEMA_PUBLISHED_MEDIA_TYPE_COUNT POST /v1/m/demo/typed: requestBody.content must declare exactly one media type; got 2\n',
      'multiple-media-types message',
    )

    const opaqueMediaAnchor = '"application/json": {\n              "schema": {}'
    const parameterizedMedia = replaceExactlyOnce(
      baseline,
      opaqueMediaAnchor,
      '"application/json; charset=utf-8": {\n              "schema": {}',
      'parameterized-media-type',
    )
    const parameterizedMediaPath = writeFixture(
      temporary,
      'parameterized-media-type',
      parameterizedMedia,
    )
    const parameterizedMediaResult = runMeter(meter, parameterizedMediaPath)
    expectExact(parameterizedMediaResult.status, 1, 'parameterized-media-type exit status')
    expectExact(
      parameterizedMediaResult.stdout,
      expectedInconsistentHuman(parameterizedMediaPath),
      'parameterized-media-type stdout',
    )
    expectExact(
      parameterizedMediaResult.stderr,
      'measure-openapi-operation-content: FINDING — OPAQUE_BODY_MEDIA_TYPE_NOT_CANONICAL POST /v1/m/demo/raw: requestBody media type "application/json; charset=utf-8" must be canonical without parameters, wildcards, or control characters\n',
      'parameterized-media-type message',
    )

    const wildcardMedia = replaceExactlyOnce(
      baseline,
      opaqueMediaAnchor,
      '"application/*": {\n              "schema": {}',
      'wildcard-media-type',
    )
    const wildcardMediaPath = writeFixture(
      temporary,
      'wildcard-media-type',
      wildcardMedia,
    )
    const wildcardMediaResult = runMeter(meter, wildcardMediaPath)
    expectExact(wildcardMediaResult.status, 1, 'wildcard-media-type exit status')
    expectExact(
      wildcardMediaResult.stdout,
      expectedInconsistentHuman(wildcardMediaPath),
      'wildcard-media-type stdout',
    )
    expectExact(
      wildcardMediaResult.stderr,
      'measure-openapi-operation-content: FINDING — OPAQUE_BODY_MEDIA_TYPE_NOT_CANONICAL POST /v1/m/demo/raw: requestBody media type "application/*" must be canonical without parameters, wildcards, or control characters\n',
      'wildcard-media-type message',
    )

    const crlfMedia = replaceExactlyOnce(
      baseline,
      opaqueMediaAnchor,
      '"application/x-ndjson\\r\\nX-Evil: yes": {\n              "schema": {}',
      'crlf-media-type',
    )
    const crlfMediaPath = writeFixture(temporary, 'crlf-media-type', crlfMedia)
    const crlfMediaResult = runMeter(meter, crlfMediaPath)
    expectExact(crlfMediaResult.status, 1, 'crlf-media-type exit status')
    expectExact(
      crlfMediaResult.stdout,
      expectedInconsistentHuman(crlfMediaPath),
      'crlf-media-type stdout',
    )
    expectExact(
      crlfMediaResult.stderr,
      'measure-openapi-operation-content: FINDING — OPAQUE_BODY_MEDIA_TYPE_NOT_CANONICAL POST /v1/m/demo/raw: requestBody media type "application/x-ndjson\\r\\nX-Evil: yes" must be canonical without parameters, wildcards, or control characters\n',
      'crlf-media-type message',
    )

    const unreadable = writeFixture(temporary, 'cannot-look', '{}\n')
    const cannotLookResult = runMeter(meter, unreadable)
    expectExact(cannotLookResult.status, 2, 'cannot-look exit status')
    expectExact(cannotLookResult.stdout, '', 'cannot-look stdout')
    expectExact(
      cannotLookResult.stderr,
      'measure-openapi-operation-content: CANNOT LOOK — the document has no object-valued paths member\n',
      'cannot-look message',
    )

    const malformedPath = writeFixture(temporary, 'malformed-json', '{\n')
    const malformedResult = runMeter(meter, malformedPath)
    let parserMessage = ''
    try {
      JSON.parse('{\n')
    } catch (error) {
      parserMessage = error.message
    }
    expectExact(malformedResult.status, 2, 'malformed-json exit status')
    expectExact(malformedResult.stdout, '', 'malformed-json stdout')
    expectExact(
      malformedResult.stderr,
      `measure-openapi-operation-content: CANNOT LOOK — ${malformedPath} is not valid JSON: ${parserMessage}\n`,
      'malformed-json message',
    )

    const anchorProbe = spawnSync(process.execPath, [SELF, '--probe-nonunique-anchor'], {
      encoding: 'utf8',
    })
    if (anchorProbe.error) {
      throw cannotLook(`cannot execute anchor probe: ${anchorProbe.error.message}`)
    }
    expectExact(anchorProbe.status, 2, 'non-unique-anchor exit status')
    expectExact(anchorProbe.stdout, '', 'non-unique-anchor stdout')
    expectExact(
      anchorProbe.stderr,
      'measure-openapi-operation-content self-test: CANNOT LOOK — mutation anchor duplicate-summary matched 4 times; expected exactly once\n',
      'non-unique-anchor message',
    )

    const absentSubject = path.join(temporary, 'absent-meter-subject.mjs')
    const subjectProbe = spawnSync(process.execPath, [SELF], {
      encoding: 'utf8',
      env: { ...process.env, OPENAPI_METER_SELFTEST_SUBJECT: absentSubject },
    })
    if (subjectProbe.error) {
      throw cannotLook(`cannot execute missing-subject probe: ${subjectProbe.error.message}`)
    }
    expectExact(subjectProbe.status, 2, 'missing-subject exit status')
    expectExact(subjectProbe.stdout, '', 'missing-subject stdout')
    expectExact(
      subjectProbe.stderr,
      `measure-openapi-operation-content self-test: CANNOT LOOK — cannot copy meter subject ${absentSubject}: ENOENT\n`,
      'missing-subject message',
    )

    console.log(
      'openapi-operation-content external self-test: OK/FINDING/CANNOT LOOK and 11 fail-closed mutants passed',
    )
  } finally {
    fs.rmSync(temporary, { recursive: true, force: true })
  }
}

try {
  const args = process.argv.slice(2)
  if (args.length === 1 && args[0] === '--probe-nonunique-anchor') {
    runAnchorProbe()
  } else if (args.length === 0) {
    runBattery()
  } else {
    throw cannotLook(`unknown self-test option ${args.join(' ')}`)
  }
} catch (error) {
  const verdict = error.exitCode === 2 ? 'CANNOT LOOK' : 'FINDING'
  console.error(`measure-openapi-operation-content self-test: ${verdict} — ${error.message}`)
  process.exitCode = error.exitCode ?? 1
}
