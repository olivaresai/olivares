// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

export interface ParsedEndpoint {
  method: string
  path: string
  operationId: string
  summary: string
  tag: string
  secured: boolean
  /** Permission declared by the operation's `x-required-permission`. Missing or
   * empty values remain undefined so non-system admins can deny them closed. */
  requiredPermission?: string
  /** API stability tier from the spec's `x-stability` (default stable). Module
   * routes under /v1/m/ are surfaced as `beta` so the tree can badge them. */
  stability: 'stable' | 'beta'
  /** The module namespace for a /v1/m/<ns>/… route, else undefined (core route). */
  namespace?: string
  parameters: ParsedParam[]
  requestBody: ParsedSchema | null
  responseSchema: ParsedSchema | null
}

export interface ParsedParam {
  name: string
  in: 'path' | 'query' | 'header'
  required: boolean
  description: string
  schema: Record<string, unknown>
}

export interface ParsedSchema {
  type: string
  properties: Record<string, Record<string, unknown>>
  required: string[]
}

export interface TagGroup {
  tag: string
  description: string
  endpoints: ParsedEndpoint[]
  /** True when every endpoint in the group is a beta module route — the tree
   * renders a single "beta" badge on the group header. */
  beta: boolean
}

/** The module namespace for a /v1/m/<ns>/… path, else null (a core route). */
export function moduleNamespace(path: string): string | null {
  return path.match(/^\/v1\/m\/([^/]+)/)?.[1] ?? null
}

const HTTP_METHODS = ['get', 'post', 'put', 'patch', 'delete'] as const

function resolveRef(
  spec: Record<string, unknown>,
  ref: string,
): Record<string, unknown> | null {
  const parts = ref.replace('#/', '').split('/')
  let current: unknown = spec
  for (const part of parts) {
    if (current && typeof current === 'object' && part in current) {
      current = (current as Record<string, unknown>)[part]
    } else {
      return null
    }
  }
  return current as Record<string, unknown> | null
}

function resolveSchema(
  spec: Record<string, unknown>,
  schema: Record<string, unknown>,
): ParsedSchema | null {
  let resolved = schema
  if (resolved['$ref']) {
    const r = resolveRef(spec, resolved['$ref'] as string)
    if (!r) return null
    resolved = r
  }
  return {
    type: (resolved.type as string) || 'object',
    properties:
      (resolved.properties as Record<string, Record<string, unknown>>) || {},
    required: (resolved.required as string[]) || [],
  }
}

export function parseOpenAPISpec(
  spec: Record<string, unknown>,
): ParsedEndpoint[] {
  const paths = spec.paths as
    Record<string, Record<string, unknown>> | undefined
  if (!paths) return []

  const endpoints: ParsedEndpoint[] = []

  for (const [path, methods] of Object.entries(paths)) {
    for (const method of HTTP_METHODS) {
      const operation = methods[method] as Record<string, unknown> | undefined
      if (!operation) continue

      // Core routes tag themselves; module routes (/v1/m/<ns>/…) carry no tag in
      // the beta doc, so we group them by their namespace. `x-stability` badges them.
      const explicitTags = operation.tags as string[] | undefined
      const namespace = moduleNamespace(path) ?? undefined
      const tag =
        explicitTags && explicitTags.length > 0
          ? explicitTags[0]
          : (namespace ?? 'other')
      const stability =
        operation['x-stability'] === 'beta' ? 'beta' : ('stable' as const)
      const permissionExtension = operation['x-required-permission']
      const requiredPermission =
        typeof permissionExtension === 'string' && permissionExtension.trim()
          ? permissionExtension.trim()
          : undefined
      const params = (operation.parameters as Record<string, unknown>[]) || []

      let requestBody: ParsedSchema | null = null
      const reqBodySpec = operation.requestBody as
        Record<string, unknown> | undefined
      if (reqBodySpec?.content) {
        const content = reqBodySpec.content as Record<
          string,
          Record<string, unknown>
        >
        const jsonContent = content['application/json']
        if (jsonContent?.schema) {
          requestBody = resolveSchema(
            spec,
            jsonContent.schema as Record<string, unknown>,
          )
        }
      }

      let responseSchema: ParsedSchema | null = null
      const responses = operation.responses as
        Record<string, Record<string, unknown>> | undefined
      if (responses) {
        const successResp = responses['200'] || responses['201']
        if (successResp?.content) {
          const content = successResp.content as Record<
            string,
            Record<string, unknown>
          >
          const jsonContent = content['application/json']
          if (jsonContent?.schema) {
            responseSchema = resolveSchema(
              spec,
              jsonContent.schema as Record<string, unknown>,
            )
          }
        }
      }

      const security = operation.security as
        Array<Record<string, unknown>> | undefined
      const secured =
        !security || security.some((s) => Object.keys(s).length > 0)

      endpoints.push({
        method: method.toUpperCase(),
        path,
        operationId: (operation.operationId as string) || `${method}_${path}`,
        summary: (operation.summary as string) || '',
        tag,
        secured,
        requiredPermission,
        stability,
        namespace,
        parameters: params.map((p) => ({
          name: (p.name as string) || '',
          in: (p.in as ParsedParam['in']) || 'query',
          required: (p.required as boolean) || false,
          description: (p.description as string) || '',
          schema: (p.schema as Record<string, unknown>) || {},
        })),
        requestBody,
        responseSchema,
      })
    }
  }

  return endpoints
}

/**
 * Limit the operation catalog for a tenant administrator. System administrators
 * may inspect every operation. Everyone else loses both explicit `system:*`
 * operations and operations without permission metadata (deny-closed).
 */
export function filterEndpointsForAdmin(
  endpoints: ParsedEndpoint[],
  isSystemAdmin: boolean,
): ParsedEndpoint[] {
  if (isSystemAdmin) return endpoints
  return endpoints.filter(
    (endpoint) =>
      !!endpoint.requiredPermission &&
      !endpoint.requiredPermission.startsWith('system:'),
  )
}

export function groupByTag(
  spec: Record<string, unknown>,
  endpoints: ParsedEndpoint[],
): TagGroup[] {
  const tagMeta =
    (spec.tags as Array<{ name: string; description: string }>) || []
  const tagDescriptions = new Map(tagMeta.map((t) => [t.name, t.description]))

  const groups = new Map<string, ParsedEndpoint[]>()
  for (const ep of endpoints) {
    const list = groups.get(ep.tag) || []
    list.push(ep)
    groups.set(ep.tag, list)
  }

  const tagOrder = tagMeta.map((t) => t.name)
  return [...groups.entries()]
    .sort(([a], [b]) => {
      const ai = tagOrder.indexOf(a)
      const bi = tagOrder.indexOf(b)
      if (ai === -1 && bi === -1) return a.localeCompare(b)
      if (ai === -1) return 1
      if (bi === -1) return -1
      return ai - bi
    })
    .map(([tag, endpoints]) => ({
      tag,
      description: tagDescriptions.get(tag) || '',
      endpoints,
      beta:
        endpoints.length > 0 && endpoints.every((e) => e.stability === 'beta'),
    }))
}

/**
 * Parse the core (stable) spec plus, when present, the module (beta) spec into one
 * endpoint list. The beta doc (server.go `/openapi.beta.json`) exposes the ~460
 * `/v1/m/<ns>/…` module routes the stable core doc omits; merging both makes the
 * whole product API — not just the 24-month-stable core — navigable in one tree.
 * A duplicate (method, path) from the beta doc is dropped so a route is never listed
 * twice. Beta may be absent (older engine / 404) — then only the core routes show.
 */
export function parseSpecs(
  coreSpec: Record<string, unknown> | undefined,
  betaSpec: Record<string, unknown> | undefined,
): ParsedEndpoint[] {
  const core = coreSpec ? parseOpenAPISpec(coreSpec) : []
  const beta = betaSpec ? parseOpenAPISpec(betaSpec) : []
  const seen = new Set(core.map((e) => `${e.method} ${e.path}`))
  const merged = [...core]
  for (const e of beta) {
    const key = `${e.method} ${e.path}`
    if (!seen.has(key)) {
      seen.add(key)
      merged.push(e)
    }
  }
  return merged
}

export function generateSchemaExample(schema: ParsedSchema | null): string {
  if (!schema || !schema.properties) return '{}'
  const example: Record<string, unknown> = {}
  for (const [key, prop] of Object.entries(schema.properties)) {
    const type = prop.type as string
    const format = prop.format as string | undefined
    const enumValues = prop.enum as unknown[] | undefined
    if (enumValues && enumValues.length > 0) {
      example[key] = enumValues[0]
    } else if (type === 'string') {
      if (format === 'email') example[key] = 'user@example.com'
      else if (format === 'password') example[key] = ''
      else if (format === 'uuid')
        example[key] = '00000000-0000-0000-0000-000000000000'
      else if (format === 'date-time') example[key] = new Date().toISOString()
      else if (format === 'uri') example[key] = 'https://example.com'
      else example[key] = ''
    } else if (type === 'integer' || type === 'number') {
      example[key] = 0
    } else if (type === 'boolean') {
      const def = prop.default
      example[key] = typeof def === 'boolean' ? def : false
    } else if (type === 'object') {
      example[key] = {}
    } else if (type === 'array') {
      example[key] = []
    }
  }
  return JSON.stringify(example, null, 2)
}
