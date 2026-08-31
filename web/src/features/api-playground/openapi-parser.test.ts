// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import {
  parseOpenAPISpec,
  parseSpecs,
  filterEndpointsForAdmin,
  groupByTag,
  moduleNamespace,
  generateSchemaExample,
  type ParsedSchema,
} from './openapi-parser'

const minimalSpec = {
  openapi: '3.1.0',
  info: { title: 'Test', version: 'v1' },
  tags: [
    { name: 'auth', description: 'Authentication' },
    { name: 'agents', description: 'Agent lifecycle' },
  ],
  components: {
    schemas: {
      Agent: {
        type: 'object',
        properties: {
          id: { type: 'string', format: 'uuid' },
          name: { type: 'string' },
          status: { type: 'string', enum: ['active', 'inactive'] },
        },
        required: ['id', 'name', 'status'],
      },
      AgentInput: {
        type: 'object',
        properties: {
          name: { type: 'string' },
          kind: { type: 'string' },
        },
        required: ['name', 'kind'],
      },
    },
  },
  paths: {
    '/v1/auth/login': {
      post: {
        operationId: 'login',
        summary: 'Login',
        tags: ['auth'],
        security: [{}],
        requestBody: {
          content: {
            'application/json': {
              schema: {
                type: 'object',
                properties: {
                  email: { type: 'string' },
                  password: { type: 'string' },
                },
              },
            },
          },
        },
        responses: {
          '200': {
            description: 'OK',
            content: {
              'application/json': {
                schema: {
                  type: 'object',
                  properties: { token: { type: 'string' } },
                },
              },
            },
          },
        },
      },
    },
    '/v1/agents': {
      get: {
        operationId: 'listAgents',
        summary: 'List agents',
        tags: ['agents'],
        security: [{ bearerAuth: [] }],
        parameters: [
          {
            name: 'X-Olivares-Tenant',
            in: 'header',
            required: false,
            schema: { type: 'string' },
          },
          {
            name: 'limit',
            in: 'query',
            required: false,
            schema: { type: 'integer' },
          },
        ],
        responses: {
          '200': {
            description: 'OK',
            content: {
              'application/json': {
                schema: {
                  type: 'object',
                  properties: {
                    items: {
                      type: 'array',
                      items: { $ref: '#/components/schemas/Agent' },
                    },
                  },
                },
              },
            },
          },
        },
      },
      post: {
        operationId: 'createAgent',
        summary: 'Create agent',
        tags: ['agents'],
        security: [{ bearerAuth: [] }],
        requestBody: {
          content: {
            'application/json': {
              schema: { $ref: '#/components/schemas/AgentInput' },
            },
          },
        },
        responses: {
          '201': {
            description: 'Created',
            content: {
              'application/json': {
                schema: { $ref: '#/components/schemas/Agent' },
              },
            },
          },
        },
      },
    },
    '/v1/agents/{id}': {
      get: {
        operationId: 'getAgent',
        summary: 'Get agent',
        tags: ['agents'],
        security: [{ bearerAuth: [] }],
        parameters: [
          {
            name: 'id',
            in: 'path',
            required: true,
            schema: { type: 'string' },
          },
        ],
        responses: {
          '200': {
            description: 'OK',
            content: {
              'application/json': {
                schema: { $ref: '#/components/schemas/Agent' },
              },
            },
          },
        },
      },
      delete: {
        operationId: 'deleteAgent',
        summary: 'Delete agent',
        tags: ['agents'],
        security: [{ bearerAuth: [] }],
        parameters: [
          {
            name: 'id',
            in: 'path',
            required: true,
            schema: { type: 'string' },
          },
        ],
        responses: { '204': { description: 'No content' } },
      },
    },
  },
}

describe('parseOpenAPISpec', () => {
  it('parses all endpoints from the spec', () => {
    const endpoints = parseOpenAPISpec(
      minimalSpec as unknown as Record<string, unknown>,
    )
    expect(endpoints).toHaveLength(5)
  })

  it('extracts operationId and method correctly', () => {
    const endpoints = parseOpenAPISpec(
      minimalSpec as unknown as Record<string, unknown>,
    )
    const login = endpoints.find((e) => e.operationId === 'login')
    expect(login).toBeDefined()
    expect(login?.method).toBe('POST')
    expect(login?.path).toBe('/v1/auth/login')
  })

  it('extracts tags', () => {
    const endpoints = parseOpenAPISpec(
      minimalSpec as unknown as Record<string, unknown>,
    )
    const authEndpoints = endpoints.filter((e) => e.tag === 'auth')
    expect(authEndpoints).toHaveLength(1)
    const agentEndpoints = endpoints.filter((e) => e.tag === 'agents')
    expect(agentEndpoints).toHaveLength(4)
  })

  it('parses parameters', () => {
    const endpoints = parseOpenAPISpec(
      minimalSpec as unknown as Record<string, unknown>,
    )
    const list = endpoints.find((e) => e.operationId === 'listAgents')
    expect(list).toBeDefined()
    expect(list?.parameters).toHaveLength(2)
    expect(list?.parameters[0].name).toBe('X-Olivares-Tenant')
    expect(list?.parameters[0].in).toBe('header')
    expect(list?.parameters[1].name).toBe('limit')
    expect(list?.parameters[1].in).toBe('query')
  })

  it('parses inline request body', () => {
    const endpoints = parseOpenAPISpec(
      minimalSpec as unknown as Record<string, unknown>,
    )
    const login = endpoints.find((e) => e.operationId === 'login')
    expect(login?.requestBody).not.toBeNull()
    expect(login?.requestBody?.properties).toHaveProperty('email')
    expect(login?.requestBody?.properties).toHaveProperty('password')
  })

  it('resolves $ref request body', () => {
    const endpoints = parseOpenAPISpec(
      minimalSpec as unknown as Record<string, unknown>,
    )
    const create = endpoints.find((e) => e.operationId === 'createAgent')
    expect(create?.requestBody).not.toBeNull()
    expect(create?.requestBody?.properties).toHaveProperty('name')
    expect(create?.requestBody?.properties).toHaveProperty('kind')
    expect(create?.requestBody?.required).toContain('name')
  })

  it('resolves $ref response schema', () => {
    const endpoints = parseOpenAPISpec(
      minimalSpec as unknown as Record<string, unknown>,
    )
    const get = endpoints.find((e) => e.operationId === 'getAgent')
    expect(get?.responseSchema).not.toBeNull()
    expect(get?.responseSchema?.properties).toHaveProperty('id')
    expect(get?.responseSchema?.properties).toHaveProperty('name')
  })

  it('detects secured vs unsecured endpoints', () => {
    const endpoints = parseOpenAPISpec(
      minimalSpec as unknown as Record<string, unknown>,
    )
    const login = endpoints.find((e) => e.operationId === 'login')
    const list = endpoints.find((e) => e.operationId === 'listAgents')
    expect(login?.secured).toBe(false)
    expect(list?.secured).toBe(true)
  })

  it('extracts and normalizes x-required-permission', () => {
    const endpoints = parseOpenAPISpec({
      paths: {
        '/v1/example': {
          get: {
            'x-required-permission': '  example:resource:read  ',
            responses: { '200': { description: 'OK' } },
          },
          delete: {
            'x-required-permission': '',
            responses: { '204': { description: 'No content' } },
          },
        },
      },
    })

    expect(
      endpoints.find((endpoint) => endpoint.method === 'GET'),
    ).toHaveProperty('requiredPermission', 'example:resource:read')
    expect(
      endpoints.find((endpoint) => endpoint.method === 'DELETE')
        ?.requiredPermission,
    ).toBeUndefined()
  })

  it('handles empty spec gracefully', () => {
    expect(parseOpenAPISpec({})).toEqual([])
    expect(parseOpenAPISpec({ paths: {} })).toEqual([])
  })
})

describe('groupByTag', () => {
  it('groups endpoints by tag in spec-defined order', () => {
    const endpoints = parseOpenAPISpec(
      minimalSpec as unknown as Record<string, unknown>,
    )
    const groups = groupByTag(
      minimalSpec as unknown as Record<string, unknown>,
      endpoints,
    )
    expect(groups).toHaveLength(2)
    expect(groups[0].tag).toBe('auth')
    expect(groups[0].description).toBe('Authentication')
    expect(groups[1].tag).toBe('agents')
    expect(groups[1].endpoints).toHaveLength(4)
  })
})

// The module (beta) doc: /v1/m/<ns>/… routes with NO tag, x-stability=beta.
const betaSpec = {
  openapi: '3.1.0',
  info: { title: 'modules (beta)', version: 'v1' },
  paths: {
    '/v1/m/finops/budgets': {
      get: {
        summary: 'finops module route',
        'x-stability': 'beta',
        'x-required-permission': 'finops:budget:read',
        security: [{ bearerAuth: [] }],
        responses: { '200': { description: 'OK' } },
      },
    },
    '/v1/m/notify/routes': {
      post: {
        summary: 'notify module route',
        'x-stability': 'beta',
        'x-required-permission': 'notify:route:write',
        security: [{ bearerAuth: [] }],
        responses: { '201': { description: 'Created' } },
      },
    },
  },
}

describe('module (beta) routes', () => {
  it('derives the namespace from a /v1/m/<ns>/ path', () => {
    expect(moduleNamespace('/v1/m/finops/budgets')).toBe('finops')
    expect(moduleNamespace('/v1/m/notify/routes/{id}/test')).toBe('notify')
    expect(moduleNamespace('/v1/auth/login')).toBeNull()
  })

  it('groups untagged module routes by namespace and marks them beta', () => {
    const endpoints = parseOpenAPISpec(
      betaSpec as unknown as Record<string, unknown>,
    )
    expect(endpoints).toHaveLength(2)
    const finops = endpoints.find((e) => e.path === '/v1/m/finops/budgets')
    expect(finops?.tag).toBe('finops')
    expect(finops?.namespace).toBe('finops')
    expect(finops?.stability).toBe('beta')
  })

  it('marks core routes stable with no namespace', () => {
    const endpoints = parseOpenAPISpec(
      minimalSpec as unknown as Record<string, unknown>,
    )
    const login = endpoints.find((e) => e.operationId === 'login')
    expect(login?.stability).toBe('stable')
    expect(login?.namespace).toBeUndefined()
  })

  it('groupByTag flags a pure-beta group and not a core group', () => {
    const endpoints = parseSpecs(
      minimalSpec as unknown as Record<string, unknown>,
      betaSpec as unknown as Record<string, unknown>,
    )
    const groups = groupByTag(
      minimalSpec as unknown as Record<string, unknown>,
      endpoints,
    )
    expect(groups.find((g) => g.tag === 'auth')?.beta).toBe(false)
    expect(groups.find((g) => g.tag === 'finops')?.beta).toBe(true)
    expect(groups.find((g) => g.tag === 'notify')?.beta).toBe(true)
  })
})

describe('operation permission filtering', () => {
  const permissionSpec = {
    paths: {
      '/tenant-operation': {
        get: {
          'x-required-permission': 'finops:budget:read',
          responses: { '200': { description: 'OK' } },
        },
      },
      '/system-operation': {
        get: {
          'x-required-permission': 'system:admin',
          responses: { '200': { description: 'OK' } },
        },
      },
      '/operation-without-metadata': {
        get: { responses: { '200': { description: 'OK' } } },
      },
    },
  }

  it('denies system and unclassified operations to tenant admins', () => {
    const endpoints = parseOpenAPISpec(
      permissionSpec as unknown as Record<string, unknown>,
    )
    expect(
      filterEndpointsForAdmin(endpoints, false).map(
        (endpoint) => endpoint.path,
      ),
    ).toEqual(['/tenant-operation'])
  })

  it('keeps every operation for system admins', () => {
    const endpoints = parseOpenAPISpec(
      permissionSpec as unknown as Record<string, unknown>,
    )
    expect(filterEndpointsForAdmin(endpoints, true)).toHaveLength(3)
  })
})

describe('parseSpecs', () => {
  it('merges core and beta endpoints', () => {
    const merged = parseSpecs(
      minimalSpec as unknown as Record<string, unknown>,
      betaSpec as unknown as Record<string, unknown>,
    )
    expect(merged).toHaveLength(7) // 5 core + 2 module
    expect(merged.some((e) => e.path === '/v1/m/finops/budgets')).toBe(true)
  })

  it('tolerates an absent beta doc (core only)', () => {
    const merged = parseSpecs(
      minimalSpec as unknown as Record<string, unknown>,
      undefined,
    )
    expect(merged).toHaveLength(5)
  })

  it('drops a duplicate (method, path) already in the core doc', () => {
    const dupBeta = {
      paths: {
        '/v1/agents': {
          get: {
            summary: 'dup',
            'x-stability': 'beta',
            responses: { '200': { description: 'OK' } },
          },
        },
      },
    }
    const merged = parseSpecs(
      minimalSpec as unknown as Record<string, unknown>,
      dupBeta as unknown as Record<string, unknown>,
    )
    expect(merged).toHaveLength(5) // the duplicate GET /v1/agents is dropped
    expect(
      merged.find((e) => e.path === '/v1/agents' && e.method === 'GET')
        ?.stability,
    ).toBe('stable') // the core (stable) one wins
  })
})

describe('generateSchemaExample', () => {
  it('generates JSON example from schema', () => {
    const schema: ParsedSchema = {
      type: 'object',
      properties: {
        email: { type: 'string', format: 'email' },
        password: { type: 'string', format: 'password' },
        count: { type: 'integer' },
        active: { type: 'boolean' },
        status: { type: 'string', enum: ['active', 'inactive'] },
      },
      required: ['email', 'password'],
    }
    const example = JSON.parse(generateSchemaExample(schema))
    expect(example.email).toBe('user@example.com')
    expect(example.password).toBe('')
    expect(example.count).toBe(0)
    expect(example.active).toBe(false)
    expect(example.status).toBe('active')
  })

  it('returns empty object for null schema', () => {
    expect(generateSchemaExample(null)).toBe('{}')
  })
})
