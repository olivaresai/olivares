#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Measure operation summaries and requestBody declarations in an OpenAPI
// document. Mutation operations carry a handler-reviewed disposition so the
// report can distinguish bodies that admit a typed schema, opaque raw bodies,
// deliberately bodyless operations, and work that is still unclassified.

import fs from 'node:fs'
import process from 'node:process'

const DEFAULT_SPEC = 'web/openapi/openapi.beta.json'
const DISPOSITION_EXTENSION = 'x-olivares-request-body-disposition'
const DISPOSITIONS = [
  'schema-published',
  'opaque-body',
  'bodyless',
  'unclassified',
]
const DISPOSITION_SET = new Set(DISPOSITIONS)
const OPERATION_METHODS = new Set([
  'get',
  'put',
  'post',
  'delete',
  'patch',
  'head',
  'options',
  'trace',
])
const MUTATION_METHODS = new Set(['post', 'put', 'patch', 'delete'])
const PAYLOAD_VERBS = new Set(['post', 'put', 'patch'])

function cannotLook(message) {
  const error = new Error(message)
  error.exitCode = 2
  return error
}

function isObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function hasOwn(value, key) {
  return Object.prototype.hasOwnProperty.call(value, key)
}

function featureFor(path) {
  const match = /^\/v1\/m\/([^/]+)/.exec(path)
  return match ? match[1] : '(non-module)'
}

function operationName({ method, path }) {
  return `${method.toUpperCase()} ${path}`
}

export function operationsFrom(document) {
  if (!isObject(document) || !isObject(document.paths)) {
    throw cannotLook('the document has no object-valued paths member')
  }
  const operations = []
  for (const [path, item] of Object.entries(document.paths)) {
    if (!isObject(item)) {
      throw cannotLook(`path item ${path} is not an object`)
    }
    for (const [rawMethod, operation] of Object.entries(item)) {
      const method = rawMethod.toLowerCase()
      if (!OPERATION_METHODS.has(method)) continue
      if (!isObject(operation)) {
        throw cannotLook(`${rawMethod.toUpperCase()} ${path} is not an object`)
      }
      operations.push({ method, path, feature: featureFor(path), operation })
    }
  }
  if (operations.length === 0) {
    throw cannotLook('the document publishes zero operations')
  }
  return operations
}

function percentage(numerator, denominator) {
  if (denominator === 0) return null
  return Math.round((numerator * 1000) / denominator) / 10
}

function countRequestBodies(operations, methods) {
  const eligible = operations.filter(({ method }) => methods.has(method))
  const bodied = eligible.filter(({ operation }) => isObject(operation.requestBody))
  return {
    operations: eligible.length,
    with_requestBody: bodied.length,
    coverage_percent: percentage(bodied.length, eligible.length),
  }
}

function summarize(operations) {
  const summaries = operations
    .map(({ operation }) => operation.summary)
    .filter((summary) => typeof summary === 'string' && summary.trim() !== '')
  return {
    operations: operations.length,
    summaries_present: summaries.length,
    summaries_distinct: new Set(summaries).size,
    summary_distinctness_percent: percentage(new Set(summaries).size, operations.length),
    mutation_verbs: countRequestBodies(operations, MUTATION_METHODS),
    payload_verbs: countRequestBodies(operations, PAYLOAD_VERBS),
  }
}

function typedSchemaPublished(requestBody) {
  if (!isObject(requestBody) || !isObject(requestBody.content)) return false
  for (const media of Object.values(requestBody.content)) {
    if (!isObject(media) || !isObject(media.schema)) continue
    const structuralKeys = Object.keys(media.schema).filter(
      (key) => !['description', 'title', 'example', 'examples', 'default'].includes(key),
    )
    if (structuralKeys.length > 0) return true
  }
  return false
}

function canonicalMediaType(mediaType) {
  if (mediaType !== mediaType.toLowerCase() || mediaType.includes('*')) return false
  const parts = mediaType.split('/')
  if (parts.length !== 2) return false
  const token = /^[a-z0-9!#$%&'+.^_`|~-]+$/
  return parts.every((part) => token.test(part))
}

function requestBodyEnvelopeIssue(requestBody) {
  if (!isObject(requestBody)) {
    return {
      code: 'WITHOUT_REQUEST_BODY',
      message: 'requestBody is absent or is not an object',
    }
  }
  if (hasOwn(requestBody, 'required') && typeof requestBody.required !== 'boolean') {
    return {
      code: 'REQUIRED_NOT_BOOLEAN',
      message: `requestBody.required must be boolean when present; got ${JSON.stringify(requestBody.required)}`,
    }
  }
  if (!isObject(requestBody.content)) {
    return {
      code: 'CONTENT_NOT_OBJECT',
      message: 'requestBody.content must be an object',
    }
  }
  const mediaTypes = Object.keys(requestBody.content)
  if (mediaTypes.length !== 1) {
    return {
      code: 'MEDIA_TYPE_COUNT',
      message: `requestBody.content must declare exactly one media type; got ${mediaTypes.length}`,
    }
  }
  const mediaType = mediaTypes[0]
  if (!canonicalMediaType(mediaType)) {
    return {
      code: 'MEDIA_TYPE_NOT_CANONICAL',
      message:
        `requestBody media type ${JSON.stringify(mediaType)} must be canonical ` +
        'without parameters, wildcards, or control characters',
    }
  }
  return null
}

function opaqueBodyIssue(requestBody) {
  const envelopeIssue = requestBodyEnvelopeIssue(requestBody)
  if (envelopeIssue !== null) return envelopeIssue
  const unexpectedBodyKeys = Object.keys(requestBody).filter(
    (key) => !['description', 'required', 'content'].includes(key) && !key.startsWith('x-'),
  )
  if (unexpectedBodyKeys.length > 0) {
    return {
      code: 'NOT_MINIMAL',
      message: `requestBody is not minimal; unexpected keys=${unexpectedBodyKeys.sort().join(',')}`,
    }
  }
  for (const [mediaType, media] of Object.entries(requestBody.content)) {
    if (!isObject(media)) {
      return { code: 'NOT_MINIMAL', message: `${mediaType} media declaration is not an object` }
    }
    if (!isObject(media.schema)) {
      return {
        code: 'NOT_MINIMAL',
        message: `${mediaType} schema is absent or is not an object`,
      }
    }
    if (hasOwn(media.schema, 'properties')) {
      return {
        code: 'INVENTED_PROPERTIES',
        message: `${mediaType} schema publishes named properties`,
      }
    }
    const allowedKeys = new Set(['description', 'type', 'format'])
    const structuralKeys = Object.keys(media.schema).filter((key) => !allowedKeys.has(key))
    if (structuralKeys.length > 0) {
      return {
        code: 'NOT_MINIMAL',
        message: `${mediaType} schema is not minimal; unexpected keys=${structuralKeys.sort().join(',')}`,
      }
    }
    if (hasOwn(media.schema, 'type') && media.schema.type !== 'string') {
      return {
        code: 'NOT_MINIMAL',
        message: `${mediaType} schema type must be string when present`,
      }
    }
    if (hasOwn(media.schema, 'format')) {
      if (media.schema.type !== 'string' || media.schema.format !== 'binary') {
        return {
          code: 'NOT_MINIMAL',
          message: `${mediaType} schema format must be binary with type string when present`,
        }
      }
    }
    if (hasOwn(media.schema, 'description') && typeof media.schema.description !== 'string') {
      return {
        code: 'NOT_MINIMAL',
        message: `${mediaType} schema description is not a string`,
      }
    }
  }
  return null
}

function analyzeDispositions(operations) {
  const mutations = operations.filter(({ method }) => MUTATION_METHODS.has(method))
  const counts = Object.fromEntries(DISPOSITIONS.map((disposition) => [disposition, 0]))
  let missing = 0
  let invalid = 0
  const pendingFindings = []
  const completenessFindings = []
  const consistencyFindings = []

  for (const candidate of mutations) {
    const { operation } = candidate
    const name = operationName(candidate)
    if (!hasOwn(operation, DISPOSITION_EXTENSION)) {
      missing++
      completenessFindings.push(
        `DISPOSITION_MISSING ${name}: ${DISPOSITION_EXTENSION} is absent`,
      )
      continue
    }
    const disposition = operation[DISPOSITION_EXTENSION]
    if (typeof disposition !== 'string' || !DISPOSITION_SET.has(disposition)) {
      invalid++
      completenessFindings.push(
        `DISPOSITION_INVALID ${name}: expected ${DISPOSITIONS.join('|')}; got ${JSON.stringify(disposition)}`,
      )
      continue
    }
    counts[disposition]++

    if (disposition === 'unclassified') {
      pendingFindings.push(
        `UNCLASSIFIED ${name}: disposition is pending handler review`,
      )
      if (hasOwn(operation, 'requestBody')) {
        consistencyFindings.push(
          `UNCLASSIFIED_WITH_REQUEST_BODY ${name}: unclassified must not publish requestBody before handler review`,
        )
      }
      continue
    }
    if (disposition === 'bodyless') {
      if (hasOwn(operation, 'requestBody')) {
        consistencyFindings.push(
          `BODYLESS_WITH_REQUEST_BODY ${name}: bodyless must not publish requestBody`,
        )
      }
      continue
    }
    if (disposition === 'schema-published') {
      const envelopeIssue = requestBodyEnvelopeIssue(operation.requestBody)
      if (envelopeIssue !== null) {
        consistencyFindings.push(
          `SCHEMA_PUBLISHED_${envelopeIssue.code} ${name}: ${envelopeIssue.message}`,
        )
      } else if (!typedSchemaPublished(operation.requestBody)) {
        consistencyFindings.push(
          `SCHEMA_PUBLISHED_WITHOUT_TYPED_SCHEMA ${name}: schema-published requires a non-empty content schema`,
        )
      }
      continue
    }

    const issue = opaqueBodyIssue(operation.requestBody)
    if (issue !== null) {
      consistencyFindings.push(`OPAQUE_BODY_${issue.code} ${name}: ${issue.message}`)
    }
  }

  const completeness = {
    complete: missing === 0 && invalid === 0,
    missing,
    invalid,
  }
  const consistency = {
    complete: consistencyFindings.length === 0,
    findings: consistencyFindings.length,
  }
  const ceilingsAvailable =
    counts.unclassified === 0 && completeness.complete && consistency.complete
  const blockers = []
  if (counts.unclassified > 0) blockers.push(`unclassified=${counts.unclassified}`)
  if (missing > 0) blockers.push(`missing=${missing}`)
  if (invalid > 0) blockers.push(`invalid=${invalid}`)
  if (consistencyFindings.length > 0) {
    blockers.push(`consistency_findings=${consistencyFindings.length}`)
  }
  const unavailableReason = ceilingsAvailable
    ? null
    : `requires unclassified=0 and complete, consistent dispositions (${blockers.join(', ')})`
  const requestBodyCeiling = counts['schema-published'] + counts['opaque-body']
  const typedSchemaCeiling = counts['schema-published']

  function ceiling(numerator) {
    return {
      available: ceilingsAvailable,
      numerator: ceilingsAvailable ? numerator : null,
      denominator: ceilingsAvailable ? mutations.length : null,
      coverage_percent: ceilingsAvailable
        ? percentage(numerator, mutations.length)
        : null,
      unavailable_reason: unavailableReason,
    }
  }

  return {
    dispositions: {
      mutation_operations: mutations.length,
      schema_published: counts['schema-published'],
      opaque_body: counts['opaque-body'],
      bodyless: counts.bodyless,
      unclassified: counts.unclassified,
      missing,
      invalid,
    },
    completeness,
    consistency,
    requestBody_declaration_ceiling: ceiling(requestBodyCeiling),
    typed_schema_ceiling: ceiling(typedSchemaCeiling),
    findings: [
      ...completenessFindings,
      ...pendingFindings,
      ...consistencyFindings,
    ],
  }
}

export function measureDocument(document) {
  const operations = operationsFrom(document)
  const measurement = summarize(operations)
  const classification = analyzeDispositions(operations)
  const byFeature = {}
  for (const feature of [...new Set(operations.map((operation) => operation.feature))].sort()) {
    byFeature[feature] = summarize(operations.filter((operation) => operation.feature === feature))
  }
  return {
    definitions: {
      summaries_distinct:
        'number of distinct non-empty summary strings divided by all HTTP operations; a repetition proxy, not a semantic verdict',
      mutation_verbs:
        'requestBody declarations on POST, PUT, PATCH and DELETE operations',
      payload_verbs:
        'requestBody declarations on POST, PUT and PATCH operations',
      dispositions:
        `${DISPOSITION_EXTENSION} classifies every mutation as schema-published, opaque-body, bodyless, or unclassified from handler evidence`,
      requestBody_declaration_ceiling:
        'maximum honest requestBody declarations: schema-published plus opaque-body, divided by all mutation operations',
      typed_schema_ceiling:
        'maximum honest typed schemas: schema-published divided by all mutation operations; opaque raw bodies are deliberately excluded',
      limitation:
        'ceilings are unavailable until every mutation is classified and every disposition is internally consistent',
    },
    ...measurement,
    dispositions: classification.dispositions,
    disposition_completeness: classification.completeness,
    disposition_consistency: classification.consistency,
    requestBody_declaration_ceiling: classification.requestBody_declaration_ceiling,
    typed_schema_ceiling: classification.typed_schema_ceiling,
    findings: classification.findings,
    verdict: classification.findings.length === 0 ? 'OK' : 'FINDING',
    by_feature: byFeature,
  }
}

function parseDocument(path) {
  let raw
  try {
    raw = fs.readFileSync(path, 'utf8')
  } catch (error) {
    throw cannotLook(`cannot read ${path}: ${error.message}`)
  }
  try {
    return JSON.parse(raw)
  } catch (error) {
    throw cannotLook(`${path} is not valid JSON: ${error.message}`)
  }
}

function percentageText(value) {
  return value === null ? 'n/a' : `${value}%`
}

function ceilingText(ceiling) {
  if (!ceiling.available) return `UNAVAILABLE (${ceiling.unavailable_reason})`
  return `${ceiling.numerator}/${ceiling.denominator} (${percentageText(ceiling.coverage_percent)})`
}

function printHuman(path, measurement, byFeature) {
  console.log(`OpenAPI operation content: ${path}`)
  console.log(
    `operations=${measurement.operations} summaries_present=${measurement.summaries_present} ` +
      `summaries_distinct=${measurement.summaries_distinct} ` +
      `summary_distinctness=${percentageText(measurement.summary_distinctness_percent)}`,
  )
  const mutation = measurement.mutation_verbs
  console.log(
    `requestBody mutation-verbs(POST|PUT|PATCH|DELETE)=` +
      `${mutation.with_requestBody}/${mutation.operations} (${percentageText(mutation.coverage_percent)})`,
  )
  const payload = measurement.payload_verbs
  console.log(
    `requestBody payload-verbs(POST|PUT|PATCH)=` +
      `${payload.with_requestBody}/${payload.operations} (${percentageText(payload.coverage_percent)})`,
  )
  const dispositions = measurement.dispositions
  console.log(
    `dispositions schema-published=${dispositions.schema_published} ` +
      `opaque-body=${dispositions.opaque_body} bodyless=${dispositions.bodyless} ` +
      `unclassified=${dispositions.unclassified} missing=${dispositions.missing} ` +
      `invalid=${dispositions.invalid}`,
  )
  console.log(
    `requestBody declaration ceiling=${ceilingText(measurement.requestBody_declaration_ceiling)}`,
  )
  console.log(`typed schema ceiling=${ceilingText(measurement.typed_schema_ceiling)}`)
  console.log(`verdict=${measurement.verdict}`)
  console.log(`limitation=${measurement.definitions.limitation}`)
  if (!byFeature) return
  console.log('feature\toperations\tsummaries_distinct\tmutation_requestBody')
  for (const [feature, row] of Object.entries(measurement.by_feature)) {
    console.log(
      `${feature}\t${row.operations}\t${row.summaries_distinct}\t` +
        `${row.mutation_verbs.with_requestBody}/${row.mutation_verbs.operations}`,
    )
  }
}

function usage() {
  console.error(
    'usage: node scripts/measure-openapi-operation-content.mjs [--json] [--by-feature] [spec.json]',
  )
}

function main(argv) {
  const args = new Set(argv.filter((arg) => arg.startsWith('--')))
  const paths = argv.filter((arg) => !arg.startsWith('--'))
  for (const arg of args) {
    if (!['--json', '--by-feature'].includes(arg)) {
      usage()
      throw cannotLook(`unknown option ${arg}`)
    }
  }
  if (paths.length > 1) {
    usage()
    throw cannotLook('more than one spec path was supplied')
  }
  const path = paths[0] ?? DEFAULT_SPEC
  const measurement = measureDocument(parseDocument(path))
  if (args.has('--json')) {
    console.log(JSON.stringify({ file: path, ...measurement }, null, 2))
  } else {
    printHuman(path, measurement, args.has('--by-feature'))
  }
  for (const finding of measurement.findings) {
    console.error(`measure-openapi-operation-content: FINDING — ${finding}`)
  }
  if (measurement.findings.length > 0) process.exitCode = 1
}

try {
  main(process.argv.slice(2))
} catch (error) {
  console.error(`measure-openapi-operation-content: CANNOT LOOK — ${error.message}`)
  process.exitCode = error.exitCode ?? 1
}
