// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic fixtures for the visual e2e. The visibility views are pure
// presentation, so a fixed node/edge/row set produces a stable, screenshot-able
// render of every view (including the access-map R/RW graph — the marketing asset)
// without depending on a populated engine. Served via Playwright route mocks.

export const serverInfo = {
  version: '0.1.0-dev',
  engine: 'olivares',
  setup_required: false,
  license: { status: 'active', licensee: 'Demo Org' },
}

// ⛔ SIN ESTE FIXTURE EL ARNÉS SE DESAUTENTICA SOLO, y lo hace EN SILENCIO — medido el 2026-08-18.
//    El mock devuelve `{items: [], has_more: false}` a toda ruta `/v1/**` sin fixture. Para un
//    listado eso es una lista vacía y se ve; para `/v1/auth/refresh` es una respuesta SIN `token`, y
//    la consola hace `setSession({token: res.token, …})` con `undefined`. Resultado: `localStorage`
//    queda en `{"state":{}}`, `status` pasa a `anonymous` y **toda ruta autenticada redirige a
//    `/login`**. Se midieron 56 de 58 rutas renderizando «Sign in» con el gate en VERDE.
//    (La razón por la que se llamaba a refresh en el arranque era un desborde de 32 bits en
//    `setTimeout`, ya arreglado en `lib/auth/context.tsx`; este fixture es la segunda capa.)
export const refresh = {
  token: 'olvs_demo',
  session_id: 's1',
  expires_at: '2030-01-01T00:00:00Z',
}

export const whoami = {
  kind: 'user',
  user_id: 'u-demo',
  actor: 'user:admin@demo',
  display_name: 'Demo Admin',
  superadmin: true,
  grants: [{ tenant: 't-demo', role: 'owner' }],
}

// ---- Access map ------------------------------------------------------
const edge = (o: Record<string, unknown>) => ({
  signal_sources: '',
  bridged: true,
  occurrence_count: 7,
  first_seen: '2026-06-03T09:00:00Z',
  last_seen: '2026-06-04T07:30:00Z',
  ...o,
})

export const accessGraph = {
  nodes: [
    { id: 'A1', kind: 'agent', ref: 'orchestrator' },
    { id: 'A2', kind: 'agent', ref: 'ingest-worker' },
    { id: 'A3', kind: 'session', ref: 'sess-9f2a' },
    { id: 'R1', kind: 'postgres.table', ref: 'appdb.public.customers' },
    { id: 'R2', kind: 'postgres.table', ref: 'appdb.public.payments' },
    { id: 'R3', kind: 's3.bucket', ref: 'exports' },
    { id: 'R4', kind: 'mcp.tool', ref: 'github/create_pr' },
    { id: 'R5', kind: 'http.api', ref: 'api.stripe.com' },
  ],
  edges: [
    edge({
      id: 'e1',
      origin_kind: 'agent',
      origin_id: 'A1',
      origin_ref: 'orchestrator',
      resource_id: 'R1',
      resource_kind: 'postgres.table',
      resource_ref: 'appdb.public.customers',
      tool_ref: 'SELECT',
      mode: 'read',
      signal_source: 'pg_audit',
      signal_sources: 'otel,pg_audit',
      confidence: 'attributed',
      coverage_tier: 'clean',
      observed: true,
      permitted: true,
    }),
    edge({
      id: 'e2',
      origin_kind: 'agent',
      origin_id: 'A1',
      origin_ref: 'orchestrator',
      resource_id: 'R2',
      resource_kind: 'postgres.table',
      resource_ref: 'appdb.public.payments',
      tool_ref: 'UPDATE',
      mode: 'readwrite',
      signal_source: 'pg_audit',
      signal_sources: 'otel,pg_audit',
      confidence: 'attributed',
      coverage_tier: 'clean',
      observed: true,
      permitted: false,
    }),
    edge({
      id: 'e3',
      origin_kind: 'agent',
      origin_id: 'A2',
      origin_ref: 'ingest-worker',
      resource_id: 'R1',
      resource_kind: 'postgres.table',
      resource_ref: 'appdb.public.customers',
      tool_ref: 'INSERT',
      mode: 'readwrite',
      signal_source: 'otel',
      signal_sources: 'otel',
      confidence: 'attributed',
      coverage_tier: 'clean',
      observed: true,
      permitted: true,
    }),
    edge({
      id: 'e4',
      origin_kind: 'agent',
      origin_id: 'A2',
      origin_ref: 'ingest-worker',
      resource_id: 'R3',
      resource_kind: 's3.bucket',
      resource_ref: 'exports',
      tool_ref: 'PutObject',
      mode: 'readwrite',
      signal_source: 'cloudtrail',
      signal_sources: 'cloudtrail',
      confidence: 'attributed',
      coverage_tier: 'clean',
      observed: true,
      permitted: true,
    }),
    edge({
      id: 'e5',
      origin_kind: 'session',
      origin_id: 'A3',
      origin_ref: 'sess-9f2a',
      resource_id: 'R4',
      resource_kind: 'mcp.tool',
      resource_ref: 'github/create_pr',
      tool_ref: 'create_pr',
      mode: 'readwrite',
      signal_source: 'otel',
      signal_sources: 'otel',
      confidence: 'attributed',
      coverage_tier: 'lossy',
      observed: true,
      permitted: true,
    }),
    edge({
      id: 'e6',
      origin_kind: 'session',
      origin_id: 'A3',
      origin_ref: 'sess-9f2a',
      resource_id: 'R5',
      resource_kind: 'http.api',
      resource_ref: 'api.stripe.com',
      tool_ref: 'POST',
      mode: 'readwrite',
      signal_source: 'ebpf',
      signal_sources: 'ebpf',
      confidence: 'approximate',
      coverage_tier: 'opaque',
      observed: true,
      permitted: false,
    }),
    edge({
      id: 'e7',
      origin_kind: 'agent',
      origin_id: 'A1',
      origin_ref: 'orchestrator',
      resource_id: 'R3',
      resource_kind: 's3.bucket',
      resource_ref: 'exports',
      tool_ref: 'GetObject',
      mode: 'read',
      signal_source: 'cloudtrail',
      signal_sources: 'cloudtrail',
      confidence: 'attributed',
      coverage_tier: 'clean',
      observed: true,
      permitted: true,
    }),
  ],
  has_more: false,
}

export const accessDrift = {
  unexpected_accesses: [
    {
      kind: 'unexpected_access',
      reconciliation_pending: false,
      edge: accessGraph.edges[1],
    }, // e2 UPDATE payments
    {
      kind: 'unexpected_access',
      reconciliation_pending: true,
      edge: accessGraph.edges[5],
    }, // e6 stripe (pending)
  ],
  unused_grants: [
    {
      kind: 'unused_grant',
      edge: edge({
        id: 'g1',
        origin_kind: 'agent',
        origin_id: 'A2',
        origin_ref: 'ingest-worker',
        resource_id: 'R2',
        resource_kind: 'postgres.table',
        resource_ref: 'appdb.public.payments',
        mode: 'read',
        signal_source: 'policy',
        confidence: 'attributed',
        observed: false,
        permitted: true,
      }),
    },
  ],
  unexpected_count: 2,
  unused_count: 1,
}

// ---- Inventory -------------------------------------------------------
export const inventorySummary = {
  by_kind: {
    agent: { active: 3, stale: 1, total: 4 },
    session: { active: 5, stale: 2, total: 7 },
    mcp_server: { active: 2, stale: 0, total: 2 },
    model: { active: 3, stale: 0, total: 3 },
    identity: { active: 2, stale: 0, total: 2 },
    resource: { active: 6, stale: 1, total: 7 },
  },
  by_source: { otel: 18, pg_audit: 6, ebpf: 3, cloudtrail: 2 },
  total: 25,
}

export const inventoryEntities = {
  items: [
    {
      kind: 'agent',
      entity_id: 'A1',
      name: 'orchestrator',
      ref: 'orchestrator',
      status: 'active',
      signal_sources: ['otel', 'pg_audit'],
      hosts: ['edge-1'],
      first_seen: '2026-06-01T08:00:00Z',
      last_seen: '2026-06-04T07:30:00Z',
      occurrence_count: 142,
    },
    {
      kind: 'agent',
      entity_id: 'A2',
      name: 'ingest-worker',
      ref: 'ingest-worker',
      status: 'active',
      signal_sources: ['otel'],
      hosts: ['edge-2'],
      first_seen: '2026-06-01T08:00:00Z',
      last_seen: '2026-06-04T07:25:00Z',
      occurrence_count: 88,
    },
    {
      kind: 'agent',
      entity_id: 'A9',
      name: 'nightly-reporter',
      ref: 'nightly-reporter',
      status: 'stale',
      signal_sources: ['otel'],
      hosts: ['edge-1'],
      first_seen: '2026-05-20T02:00:00Z',
      last_seen: '2026-06-02T02:10:00Z',
      occurrence_count: 14,
    },
    {
      kind: 'mcp_server',
      entity_id: 'M1',
      name: 'github',
      ref: 'github',
      status: 'active',
      signal_sources: ['otel'],
      first_seen: '2026-06-01T08:00:00Z',
      last_seen: '2026-06-04T07:00:00Z',
      occurrence_count: 51,
    },
    {
      kind: 'model',
      entity_id: 'MO1',
      name: 'claude-opus-4-8',
      ref: 'anthropic/claude-opus-4-8',
      status: 'active',
      signal_sources: ['otel'],
      first_seen: '2026-06-01T08:00:00Z',
      last_seen: '2026-06-04T07:30:00Z',
      occurrence_count: 230,
    },
  ],
  has_more: false,
}

// ---- Sessions --------------------------------------------------------
export const sessionsLive = {
  items: [
    {
      session_ref: 'sess-9f2a',
      agent_ref: 'orchestrator',
      cc_state: 'active',
      current_action: 'create_pr',
      current_resource: 'github/create_pr',
      current_mode: 'readwrite',
      model_ref: 'claude-opus-4-8',
      input_tokens: 18420,
      output_tokens: 5310,
      cost_micro_usd: 184200,
      event_count: 64,
      tool_call_count: 22,
      first_event_at: '2026-06-04T07:00:00Z',
      last_event_at: '2026-06-04T07:31:00Z',
      duration_seconds: 1860,
      goal: 'Open a PR fixing the payments rounding bug',
      summary: '',
    },
    {
      session_ref: 'sess-7c10',
      agent_ref: 'ingest-worker',
      cc_state: 'idle',
      current_action: 'INSERT',
      current_resource: 'appdb.public.customers',
      current_mode: 'readwrite',
      model_ref: 'claude-sonnet-4-6',
      input_tokens: 8200,
      output_tokens: 1200,
      cost_micro_usd: 41000,
      event_count: 30,
      tool_call_count: 12,
      first_event_at: '2026-06-04T06:30:00Z',
      last_event_at: '2026-06-04T07:05:00Z',
      duration_seconds: 2100,
      goal: '',
      summary: '',
    },
    {
      session_ref: 'sess-3b88',
      agent_ref: 'nightly-reporter',
      cc_state: 'silent_evasion',
      current_action: 'web.search',
      current_mode: 'read',
      model_ref: 'claude-haiku-4-5',
      input_tokens: 1200,
      output_tokens: 300,
      cost_micro_usd: 1500,
      event_count: 6,
      tool_call_count: 2,
      first_event_at: '2026-06-04T02:00:00Z',
      last_event_at: '2026-06-04T02:03:00Z',
      duration_seconds: 180,
      goal: '',
      summary: '',
    },
  ],
  has_more: false,
}

export const sessionTimeline = {
  items: [
    {
      at: '2026-06-04T07:00:05Z',
      kind: 'tool',
      tool_ref: 'read_file',
      title: 'read payments.go',
    },
    {
      at: '2026-06-04T07:02:11Z',
      kind: 'mcp',
      tool_ref: 'github/create_pr',
      resource_ref: 'github',
      title: 'open PR',
    },
    { at: '2026-06-04T07:03:00Z', kind: 'cost', title: 'sampled 1.8k tokens' },
    { at: '2026-06-04T07:05:42Z', kind: 'finding', title: 'guardrail: ok' },
  ],
  has_more: false,
}

// ---- Session recording viewer ---------------------------------------
// Concrete fixture for the registry's dynamic /session-viewer/$id route.
export const sessionViewer = {
  schema: 'olivares.recording/v1',
  semconv: '1.41.1',
  session: {
    id: 'sess-a11y',
    subject: 'user:admin@demo',
    subject_kind: 'user',
    subject_user: 'u-demo',
    cred: 'sess-a11y',
    status: 'sealed',
    opened_at: '2026-06-04T07:00:00Z',
    last_at: '2026-06-04T07:31:00Z',
    frames_written: 0,
    frames_reserved: 0,
    gap: false,
  },
  live: null,
  frames: { items: [], has_more: false },
  timeline: { items: [], has_more: false },
  ledger: [],
  ledger_truncated: false,
  verify: {
    ok: true,
    frames_checked: 0,
    tip_match: true,
    anchors_ok: true,
    anchors_checked: 0,
  },
}

// ---- Health ----------------------------------------------------------
export const healthStatus = {
  items: [
    {
      id: 'h1',
      name: 'orchestrator',
      subject_kind: 'agent',
      subject_ref: 'orchestrator',
      state: 'healthy',
      desired_status: 'active',
      expected_interval_seconds: 300,
      grace_factor: 2,
      sla_target_ppm: 999000,
      sla_breach_open: false,
      last_checked_at: '2026-06-04T07:30:00Z',
      last_seen_at: '2026-06-04T07:30:00Z',
      last_latency_ms: 42,
      last_detail_hash: 'sha256:ab12cd',
      created_at: '2026-06-01T08:00:00Z',
    },
    {
      id: 'h2',
      name: 'github mcp',
      subject_kind: 'mcp',
      subject_ref: 'github',
      state: 'degraded',
      desired_status: 'active',
      expected_interval_seconds: 300,
      grace_factor: 2,
      sla_target_ppm: 999000,
      sla_breach_open: false,
      last_checked_at: '2026-06-04T07:28:00Z',
      last_seen_at: '2026-06-04T07:28:00Z',
      last_latency_ms: 880,
      last_detail_hash: 'sha256:ef34gh',
      created_at: '2026-06-01T08:00:00Z',
    },
    {
      id: 'h3',
      name: 'nightly-reporter',
      subject_kind: 'agent',
      subject_ref: 'nightly-reporter',
      state: 'down',
      desired_status: 'active',
      expected_interval_seconds: 300,
      grace_factor: 2,
      sla_target_ppm: 999000,
      sla_breach_open: true,
      last_checked_at: '2026-06-04T02:10:00Z',
      last_seen_at: '2026-06-04T02:10:00Z',
      last_latency_ms: -1,
      last_detail_hash: '',
      created_at: '2026-05-20T02:00:00Z',
    },
  ],
  has_more: false,
}

export const healthSla = {
  subject_kind: 'agent',
  subject_ref: 'orchestrator',
  window_seconds: 2592000,
  observed_seconds: 2592000,
  has_data: true,
  uptime_ppm: 998500,
  uptime_percent: 99.85,
  downtime_seconds: 3888,
  degraded_seconds: 1200,
  current_state: 'healthy',
  has_check: true,
  sla_target_ppm: 999000,
  breaching: true,
}

export const healthIncidents = {
  items: [
    {
      id: 'i1',
      subject_kind: 'agent',
      subject_ref: 'nightly-reporter',
      check_ref: 'h3',
      kind: 'down',
      severity: 'high',
      state: 'open',
      opened_at: '2026-06-04T02:15:00Z',
      summary: 'agent nightly-reporter is DOWN',
    },
    {
      id: 'i2',
      subject_kind: 'mcp',
      subject_ref: 'github',
      kind: 'degraded',
      severity: 'medium',
      state: 'resolved',
      opened_at: '2026-06-03T18:00:00Z',
      resolved_at: '2026-06-03T19:30:00Z',
      summary: 'github mcp degraded',
    },
  ],
  has_more: false,
}

export const healthEvents = {
  items: [
    {
      id: 'ev1',
      subject_kind: 'agent',
      subject_ref: 'nightly-reporter',
      state: 'down',
      prev_state: 'healthy',
      cause: 'sweep',
      latency_ms: -1,
      occurred_at: '2026-06-04T02:15:00Z',
    },
    {
      id: 'ev2',
      subject_kind: 'mcp',
      subject_ref: 'github',
      state: 'degraded',
      prev_state: 'healthy',
      cause: 'report',
      latency_ms: 880,
      occurred_at: '2026-06-03T18:00:00Z',
    },
  ],
  has_more: false,
}

export const healthDependencies = {
  nodes: [
    { id: 'sess-9f2a', kind: 'session', ref: 'sess-9f2a', health: 'healthy' },
    {
      id: 'orchestrator',
      kind: 'agent',
      ref: 'orchestrator',
      health: 'healthy',
    },
    { id: 'github', kind: 'mcp', ref: 'github', health: 'degraded' },
    {
      id: 'github/create_pr',
      kind: 'mcp_tool',
      ref: 'github/create_pr',
      health: 'unknown',
    },
    {
      id: 'nightly-reporter',
      kind: 'agent',
      ref: 'nightly-reporter',
      health: 'down',
    },
  ],
  edges: [
    {
      id: 'd1',
      source: 'sess-9f2a',
      target: 'github',
      from_kind: 'session',
      to_kind: 'mcp',
      relation: 'uses_mcp',
      observed_count: 12,
      first_seen_at: '2026-06-03T09:00:00Z',
      last_seen_at: '2026-06-04T07:00:00Z',
    },
    {
      id: 'd2',
      source: 'github',
      target: 'github/create_pr',
      from_kind: 'mcp',
      to_kind: 'mcp_tool',
      relation: 'uses_tool',
      observed_count: 8,
      first_seen_at: '2026-06-03T09:00:00Z',
      last_seen_at: '2026-06-04T07:00:00Z',
    },
    {
      id: 'd3',
      source: 'orchestrator',
      target: 'sess-9f2a',
      from_kind: 'agent',
      to_kind: 'session',
      relation: 'delegates_to',
      observed_count: 3,
      first_seen_at: '2026-06-03T09:00:00Z',
      last_seen_at: '2026-06-04T07:00:00Z',
    },
  ],
  cursor: '',
  has_more: false,
}

// ---- Executive dashboard (module XXI) sources -------------------------
// The dashboard rolls these up; deterministic data → a stable, presentable shot
// (a marketing asset). All money is integer micro-USD.
const bucket = (key: string, cost: number) => ({
  key,
  cost_micro_usd: cost,
  input_tokens: Math.round(cost / 220),
  output_tokens: Math.round(cost / 540),
  samples: Math.round(cost / 3_600_000),
})

export const finopsSummary = {
  since: '2026-05-05T00:00:00Z',
  until: '2026-06-04T00:00:00Z',
  total_micro_usd: 48_280_000_000,
  input_tokens: 214_000_000,
  output_tokens: 91_000_000,
  samples: 12_840,
  by_model: [
    bucket('claude-opus-4-8', 32_050_000_000),
    bucket('gemini-1.5-pro', 8_200_000_000),
    bucket('claude-sonnet-4-6', 5_900_000_000),
    bucket('claude-haiku-4-5', 2_130_000_000),
  ],
  by_provider: [
    bucket('anthropic', 36_150_000_000),
    bucket('google', 10_270_000_000),
    bucket('mistral', 1_860_000_000),
  ],
  by_agent: [
    bucket('support-triage', 20_600_000_000),
    bucket('orchestrator', 16_400_000_000),
    bucket('ingest-worker', 7_500_000_000),
    bucket('nightly-reporter', 3_780_000_000),
  ],
  truncated: false,
  /** ⚠ ESTE BLOQUE FALTABA, y el caso vale más que el arreglo: `finopsSummary` YA ESTABA
   *  cableada a `/spend/summary` —la ruta TENÍA fixture— y aun así `/finops` caía al error
   *  boundary leyendo `cache.uncached_input_tokens` de `undefined`. **Una fixture presente puede
   *  ser una fixture incompleta**, y desde fuera las dos se ven igual: la ruta está en la tabla.
   *  Las otras diez se cerraron añadiendo fixtures AUSENTES; ésta no habría cedido a ese método.
   *  Valores tal cual los devolvió el motor sembrado: esa estimación de demo no usa caché, así
   *  que el panel pinta 0 %. Es un estado real del contrato, no un hueco. */
  cache: {
    uncached_input_tokens: 227200,
    cache_read_tokens: 0,
    cache_creation_1h_tokens: 0,
    cache_creation_5m_tokens: 0,
    savings_micro_usd: 0,
    hit_rate_pct: 0,
  },
}

export const finopsTrend = {
  since: '2026-05-21T00:00:00Z',
  until: '2026-06-04T00:00:00Z',
  days: [
    ['2026-05-22', 1_180_000_000],
    ['2026-05-23', 1_240_000_000],
    ['2026-05-24', 980_000_000],
    ['2026-05-25', 1_020_000_000],
    ['2026-05-26', 1_460_000_000],
    ['2026-05-27', 1_680_000_000],
    ['2026-05-28', 1_580_000_000],
    ['2026-05-29', 1_940_000_000],
    ['2026-05-30', 2_010_000_000],
    ['2026-05-31', 1_760_000_000],
    ['2026-06-01', 2_120_000_000],
    ['2026-06-02', 2_240_000_000],
    ['2026-06-03', 2_290_000_000],
    ['2026-06-04', 2_380_000_000],
  ].map(([key, cost]) => ({
    key,
    cost_micro_usd: cost,
    input_tokens: 0,
    output_tokens: 0,
    samples: 0,
  })),
  truncated: false,
}

export const finopsForecast = {
  period: 'monthly',
  period_start: '2026-06-01T00:00:00Z',
  now: '2026-06-04T12:00:00Z',
  spend_micro_usd: 9_400_000_000,
  projected_micro_usd: 71_000_000_000,
  samples: 1_200,
  truncated: false,
}

export const finopsSpendTeam = {
  dimension: 'team',
  since: '2026-05-05T00:00:00Z',
  until: '2026-06-04T00:00:00Z',
  total_micro_usd: 48_280_000_000,
  buckets: [
    bucket('platform', 21_400_000_000),
    bucket('support', 14_800_000_000),
    bucket('growth', 8_300_000_000),
    bucket('research', 3_780_000_000),
  ],
  truncated: false,
}

export const modelsList = {
  items: [
    {
      id: 'mdl-opus',
      name: 'claude-opus-4-8',
      provider_id: 'p-anthropic',
      provider: 'anthropic',
      family: 'claude-opus',
      context_window: 200000,
      input_cost_micro_usd: 15,
      output_cost_micro_usd: 75,
      modality: 'text',
      status: 'active',
      capabilities: ['streaming', 'tool_use', 'vision'],
      enriched: true,
    },
    {
      id: 'mdl-flash',
      name: 'gemini-1.5-flash',
      provider_id: 'p-google',
      provider: 'google',
      family: 'gemini',
      context_window: 1000000,
      input_cost_micro_usd: 0,
      output_cost_micro_usd: 0,
      modality: 'text',
      status: 'active',
      capabilities: ['streaming'],
      enriched: true,
    },
    {
      id: 'mdl-local',
      name: 'llama-3.3-70b-local',
      provider_id: 'p-local',
      provider: 'local',
      family: 'llama',
      context_window: 128000,
      input_cost_micro_usd: 0,
      output_cost_micro_usd: 0,
      modality: 'text',
      status: 'active',
      capabilities: [],
      enriched: false,
    },
  ],
  has_more: false,
}

export const securityFindings = {
  items: [
    {
      id: 'fnd-1001',
      kind: 'guardrail',
      severity: 'critical',
      status: 'open',
      source: 'prompt_injection',
      subject_kind: 'session',
      subject_ref: 'sess-9f2a',
      detected_at: '2026-06-04T06:00:00Z',
    },
    {
      id: 'fnd-1002',
      kind: 'forensic',
      severity: 'high',
      status: 'triaged',
      source: 'pii',
      subject_kind: 'agent',
      subject_ref: 'orchestrator',
      detected_at: '2026-06-03T22:00:00Z',
    },
    {
      id: 'fnd-1003',
      kind: 'anomaly',
      severity: 'medium',
      status: 'open',
      source: 'anti_evasion',
      subject_kind: 'agent',
      subject_ref: 'ingest-worker',
      detected_at: '2026-06-03T18:00:00Z',
    },
    {
      id: 'fnd-1004',
      kind: 'guardrail',
      severity: 'low',
      status: 'resolved',
      source: 'output_validation',
      subject_kind: 'session',
      subject_ref: 'sess-7c10',
      detected_at: '2026-06-02T10:00:00Z',
    },
  ],
  has_more: false,
}

export const redteamRuns = {
  items: [
    {
      id: 'run-1',
      target_ref: 'orchestrator',
      suite: 'owasp-llm',
      status: 'completed',
      passed: 7,
      failed: 2,
      errors: 0,
      skipped: 0,
      score: 78,
      started_at: '2026-06-03T20:00:00Z',
      finished_at: '2026-06-03T20:18:00Z',
      by_family: {},
    },
    {
      id: 'run-0',
      target_ref: 'orchestrator',
      suite: 'owasp-llm',
      status: 'completed',
      passed: 6,
      failed: 3,
      errors: 0,
      skipped: 0,
      score: 66,
      started_at: '2026-05-30T20:00:00Z',
      finished_at: '2026-05-30T20:16:00Z',
      by_family: {},
    },
  ],
  has_more: false,
}

const fw = (
  framework: string,
  name: string,
  version: string,
  s: number,
  bd: number,
  p: number,
  g: number,
  u: number,
) => ({
  framework,
  name,
  version,
  summary: {
    total: s + bd + p + g + u,
    satisfied: s,
    by_design: bd,
    partial: p,
    gap: g,
    unmapped: u,
  },
})

export const complianceSummary = {
  frameworks: [
    fw('eu_ai_act', 'EU AI Act', '2024', 3, 2, 4, 1, 1),
    fw('nist_ai_rmf', 'NIST AI RMF', '1.0', 5, 3, 3, 2, 1),
    fw('iso_42001', 'ISO/IEC 42001', '2023', 4, 4, 5, 2, 1),
    fw('soc2_tsc', 'SOC 2 TSC', '2017', 6, 2, 3, 1, 1),
    fw('gdpr', 'GDPR', '2016/679', 4, 1, 3, 1, 1),
  ],
  disclaimer:
    'Control status reflects mapped capabilities and recorded evidence — it is not a certification or a legal compliance claim.',
}

export const complianceRisk = {
  items: [
    {
      id: 'rc-1',
      subject_kind: 'agent',
      subject_ref: 'payments-copilot',
      agent_id: 'a-pay',
      tier: 'high',
      suggested_tier: 'high',
      state: 'approved',
      rationale: '',
      nist_functions: [],
      signals: {},
      reviewed_by: 'user:admin',
      classified_at: '2026-06-01T00:00:00Z',
      disclaimer: '',
    },
    {
      id: 'rc-2',
      subject_kind: 'agent',
      subject_ref: 'support-triage',
      agent_id: 'a-sup',
      tier: 'limited',
      suggested_tier: 'limited',
      state: 'suggested',
      rationale: '',
      nist_functions: [],
      signals: {},
      reviewed_by: '',
      classified_at: '2026-06-01T00:00:00Z',
      disclaimer: '',
    },
    {
      id: 'rc-3',
      subject_kind: 'agent',
      subject_ref: 'nightly-reporter',
      agent_id: 'a-rep',
      tier: 'minimal',
      suggested_tier: 'minimal',
      state: 'suggested',
      rationale: '',
      nist_functions: [],
      signals: {},
      reviewed_by: '',
      classified_at: '2026-06-01T00:00:00Z',
      disclaimer: '',
    },
  ],
  has_more: false,
}

// ---- First-boot wizard (onboarding) ----------------------------------
// Without these the runner answered every one of the wizard's five reads with the
// generic `{items: [], has_more: false}` default, `setupStatus().steps` came back
// undefined and the view threw inside render — so the a11y/AT gate was scanning an
// error card and calling the route covered. A first boot is exactly the state worth
// scanning: nothing set up yet, and a PASSWORD session (whoami carries no `aal`), so
// every privileged step shows its step-up panel.
export const setupStatus = {
  completed: false,
  steps: [
    { id: 'database', completed: true },
    { id: 'connectors', completed: false },
    { id: 'identity', completed: false },
    { id: 'users', completed: false },
  ],
}

export const consoleWorkspaces = {
  has_more: false,
  items: [
    {
      created_at: '2026-06-01T09:00:00Z',
      id: 'w-default',
      is_default: true,
      name: 'Default',
      slug: 'default',
      status: 'active',
      tenant_id: 't-demo',
      updated_at: '2026-06-01T09:00:00Z',
      version: 1,
    },
  ],
}

// A COMPLETE RosterMemberDTO (console/api.ts): the roster row renders
// `members.statuses.${status}`, so a fixture missing `status` makes the harness
// print `members.statuses.undefined` — a defect of the fixture, not of the console.
export const consoleMembers = {
  has_more: false,
  items: [
    {
      display_name: 'Demo Admin',
      email: 'admin@demo',
      role: 'owner',
      sso_only: false,
      status: 'active',
      user_id: 'u-demo',
    },
  ],
}

export const consoleSources = { sources: [] }

export const consoleConnectors = {
  connectors: [
    { fields_known: true, kind: 'claude_code', transport: 'in-process' },
  ],
}

/** Nothing published yet: the PEP step is `pending` with its "author a policy" CTA. */
export const policyDistribution = { scopes: [], surface: 'managed-settings' }

/** Map a request URL path to its fixture JSON (or null to let it 404). */
/** ⛔ EL ESTADO DEL KILL-SWITCH ES UN OBJETO, NO UNA LISTA — y ésa es toda la avería.
 *
 *  El mock de rutas responde `{ items: [], has_more: false }` a lo que no tiene fixture, así que un
 *  listado sin cubrir renderiza vacío y aguanta. `GET /v1/m/sessions/killswitch/state` devuelve
 *  `KillSwitchStateDTO` —`{ estate_stopped, active[] }`, `types.ts:72`—, así que la vista leía
 *  `estate_stopped` de un objeto que no lo tiene, y `/killswitch` caía al error boundary: su
 *  accesibilidad NO se medía, en la pantalla del PARO DE EMERGENCIA.
 *
 *  Se declara PARADO=false con la lista vacía: es el estado normal, y es el que la copy del gate
 *  describe cuando dice «Kill switch = Clear — no active stop». */
export const killswitchState = { estate_stopped: false, active: [] }

/** Las cuatro rutas cuyo ÚNICO endpoint de objeto las tumbaba. Misma avería que el kill-switch:
 *  el mock devuelve `{items: []}` a lo no cubierto, y una vista que espera un objeto con campos lee
 *  `undefined` y cae al error boundary. Cada forma sale de su `types.ts`, no de la imaginación. */
export const eventingEvents = { items: [], next_seq: 0, has_more: false }
export const modelsCatalog = {
  models: [],
  capabilities: [],
  pricing_as_of: '2026-08-01T00:00:00Z',
  pricing_note: 'Demo fixture — no pricing is asserted.',
}
export const observabilityIngestionHealth = {
  standards: [],
  engine_scope: true,
  sources: [],
}
const adoptionTotals = {
  sessions: 0,
  lines_added: 0,
  lines_removed: 0,
  lines_net: 0,
  commits: 0,
  pull_requests: 0,
  active_time_ms: 0,
  tools_accepted: 0,
  tools_rejected: 0,
  acceptance_rate: null,
  input_tokens: 0,
  output_tokens: 0,
  tokens: 0,
}
const adoptionLens = { totals: adoptionTotals, by_model: [], by_tool: [] }
export const orchestrationGraph = {
  nodes: [],
  edges: [],
  coverage: { source: 'fixture', caveats: [] },
  cursor: '',
  has_more: false,
}
export const eventingTypes = { event_types: [] }

// ⛔ LAS CINCO QUE FALTABAN, y por qué importa que estén: `fixtureFor` devuelve `null` cuando
// nada casa, así que una ruta sin fixture no da una pantalla vacía — la tira al *error
// boundary*. `/eventing` era 1 de las 59 del censo de accesibilidad y caía en los dos temas por
// esto, NO por un defecto de producción (`modules/eventing/egressapi.go:66` sirve la superficie
// y `web/src/features/eventing/api.ts:77` la pide). Un arnés que no sirve una ruta no mide esa
// pantalla: la reprueba.
//
// ⛔ `egress-policy` y su `/compat` SÍ se sirven desde aquí desde el lote del pre-push: la
// integración trajo las definiciones TIPADAS de `main` (#1622) y the integrator añadió
// sus dos entradas de ruta, que la unión de definiciones por sí sola no traía. Antes decía
// `#1622` (`sol/truncation-honesty`, creada a las 12:34 de hoy, antes que esto) y los añade
// MEJOR — tipados contra `EgressPolicyStatus` de `features/eventing/types`, con
// `classified_mode`, `generation` y la razón del digest escrita. Los míos eran objetos
// sueltos. Declararlos en los dos sitios no era una redundancia inofensiva: son el MISMO
// nombre en el MISMO fichero, y `merge-tree` lo confirma como conflicto de contenido.
// Aquí quedan las TRES que esa PR no cubre.
//
// El relevo nombraba SOLO `egress-policy`. Medidas las rutas que llama la feature contra las que
// el arnés servía, faltaban CUATRO (`dead-letters`, `deliveries`, `egress-policy`,
// `subscriptions`) más `egress-policy/compat`. Se sirven las cinco: arreglar una y dejar cuatro
// habría movido el error boundary de sitio en vez de quitarlo.
//
// Listas VACÍAS a propósito: el arnés compara capturas, y datos inventados moverían las líneas
// base de pantallas que hoy pasan.
export const eventingDeliveries = { items: [], has_more: false }
export const eventingDeadLetters = { items: [], has_more: false }
export const eventingSubscriptions = { items: [], has_more: false }
export const recordingsConfig = {
  namespaces: [],
  breakglass_always: true,
  consent: 'notice',
  idle_seconds: 300,
  retention_days: 30,
  retention_enforced: false,
  ai_summaries: false,
}
export const recordingsNotice = {
  recorded_namespaces: [],
  breakglass_always: true,
  consent_mode: 'notice',
  consent_required: false,
  acknowledged: true,
  schema: 'olivares.recordings.notice/v1',
}

/** ⛔ ESTAS FIXTURES NO ESTÁN ESCRITAS A MANO: son la RESPUESTA REAL de un motor sembrado
 *  (`olivares serve --insecure --seed-demo`), capturada con `curl` el 2026-08-18 y pegada tal cual.
 *
 *  El motivo es el de siempre en este repositorio: `RunningBinaryAttestation` anida cuatro niveles
 *  —`binary.module_sums`, `binary.vcs_stamp`, `release`, `pipeline`— y una forma escrita de memoria
 *  se equivoca en un campo, la vista lee `undefined` y vuelve a caer al error boundary… con la
 *  fixture puesta, que es peor que sin ella porque parece cubierto. Copiar del motor no admite ese
 *  error.
 */
export const attestationRunning = {
  binary: {
    version: 'c8d3f20d3',
    commit: 'c8d3f20d3',
    build_date: '2026-08-17T15:18:38Z',
    go_version: 'go1.26.6',
    os: 'linux',
    arch: 'amd64',
    fips140: {
      enabled: false,
      version: 'latest',
    },
    self_sha256:
      '9f0879be6327594e8210890f93e69235546c05afae407ce8337bfcf75c35fe13',
    main_module: {
      path: 'github.com/olivaresai/olivares/cmd/olivares',
      version: '(devel)',
    },
    module_sums: {
      external_deps: 79,
      sums_present: true,
      note: 'deps without module sums are (devel) path/workspace members; external deps are counted by non-empty module sum',
    },
    vcs_stamp: {
      available: false,
      reason: 'go.work workspace build: Go stamps no vcs.* settings',
    },
    status: 'measured',
  },
  release: {
    published: false,
    status: 'not_published',
    reason:
      'not a release: this binary\'s version stamp "c8d3f20d3" is not a semantic version (a source build stamps `git describe --tags --always`, which yields a bare commit object when no tag is reachable)',
    provenance: {
      kind: 'self_declared',
      attested: false,
      note: 'SELF-DECLARED build provenance, not an attestation: the version stamp and the OTA anchor are both link-time values chosen by whoever linked this binary, and this process holds no trust anchor that was not also chosen then. A build carrying both facts is release-SHAPED; whether an official release was published is a repository/distribution fact this process cannot observe. `olivares version` reports the same anchors under the same caveat (cmd/olivares/main.go).',
    },
    signature_status: 'not_verified',
    signature_reason:
      'no release artifacts or attestation bundles exist for this binary',
    verifier_available: true,
    transparency_log: {
      verified: false,
      note: 'the native verifier never claims Rekor inclusion (core/secure/modelsign)',
    },
  },
  pipeline: {
    workflows: [
      'release.yml',
      'release-chart.yml',
      'release-provider.yml',
      'scorecard.yml',
      'patch-velocity.yml',
    ],
    status: 'declared',
    note: 'release pipeline exists in the source tree and runs only on a pushed v* tag. The running process cannot observe repository or CI state, so it cannot say whether that has ever happened.',
  },
  captured_at: '2026-08-18T04:14:14.563233683Z',
}

export const notifyDestinations = {
  destinations: [],
}

export const adoptionSummaryReal = {
  analytics: {
    totals: {
      sessions: 0,
      lines_added: 0,
      lines_removed: 0,
      lines_net: 0,
      commits: 0,
      pull_requests: 0,
      active_time_ms: 0,
      tools_accepted: 0,
      tools_rejected: 0,
      acceptance_rate: null,
      input_tokens: 0,
      output_tokens: 0,
      tokens: 0,
    },
    by_model: [],
    by_tool: [],
  },
  telemetry: {
    totals: {
      sessions: 0,
      lines_added: 0,
      lines_removed: 0,
      lines_net: 0,
      commits: 0,
      pull_requests: 0,
      active_time_ms: 0,
      tools_accepted: 0,
      tools_rejected: 0,
      acceptance_rate: null,
      input_tokens: 0,
      output_tokens: 0,
      tokens: 0,
    },
    by_model: [],
    by_tool: [],
  },
  developers: 0,
  teams: 0,
  boundary: {
    claude_api_only: true,
    excludes: [
      'claude-platform-aws',
      'microsoft-foundry',
      'amazon-bedrock',
      'vertex-ai',
    ],
  },
}

export const adoptionTrend = {
  lens: 'analytics',
  days: [],
  boundary: {
    claude_api_only: true,
    excludes: [
      'claude-platform-aws',
      'microsoft-foundry',
      'amazon-bedrock',
      'vertex-ai',
    ],
  },
}

export const identitySsoReal = {
  protocol: '',
  configured: false,
  redirect_uri: 'http://127.0.0.1:18099/v1/auth/federation/callback',
  pkce_method: 'S256',
}

export const governanceNhiPosture = {
  total: 0,
  rotation_known: 0,
  rotation_coverage: 0,
  stale: 0,
  blocked: 0,
  alerting: 0,
  orphaned: 0,
  unsponsored: 0,
  owned: 0,
  soft_deleted: 0,
  finalized: 0,
  critical: 0,
}

export const adoptionDiscrepancy = {
  days: [],
  thresholds: {
    ratio: 0.25,
    floors: {
      'claude_code.commit.count': 10,
      'claude_code.lines_of_code.count': 500,
      'claude_code.pull_request.count': 5,
      'claude_code.session.count': 10,
      'claude_code.token.usage': 100000,
    },
  },
  boundary: {
    claude_api_only: true,
    excludes: [
      'claude-platform-aws',
      'microsoft-foundry',
      'amazon-bedrock',
      'vertex-ai',
    ],
  },
}

export const adoptionDevelopers = {
  developers: [],
  boundary: {
    claude_api_only: true,
    excludes: [
      'claude-platform-aws',
      'microsoft-foundry',
      'amazon-bedrock',
      'vertex-ai',
    ],
  },
}

export const adoptionTeams = {
  teams: [],
  boundary: {
    claude_api_only: true,
    excludes: [
      'claude-platform-aws',
      'microsoft-foundry',
      'amazon-bedrock',
      'vertex-ai',
    ],
  },
}

export const notifyMatchTypes = {
  match_types: [
    {
      type: 'finding.reported',
      description:
        'A guardrail/red-team/forensic finding — the product-wide alert stream every module emits on.',
    },
    {
      type: 'approval.requested',
      description:
        'A governance approval was opened and awaits a human decision (the HITL origination card).',
    },
    {
      type: 'approval.resolved',
      description:
        'A governance approval reached a terminal outcome (approved, denied or cancelled).',
    },
  ],
}

export const finopsForecastReal = {
  period: 'monthly',
  period_start: '2026-08-01T00:00:00.000000000Z',
  now: '2026-08-18T04:21:21Z',
  spend_micro_usd: 1427000,
  projected_micro_usd: 2574688,
  samples: 6,
  method: 'trailing_window',
  window_days: 7,
  daily_run_rate_micro_usd: 178375,
  trend_projected_micro_usd: 3891875,
  confidence_low_micro_usd: 2496686,
  confidence_high_micro_usd: 5287064,
  ewa_daily_rate_micro_usd: 147810,
  ewa_projected_micro_usd: 3469511,
  ewa_confidence_low_micro_usd: 2257396,
  ewa_confidence_high_micro_usd: 4681626,
  ewa_alpha: 0.3,
}

export const finopsModelRates = {
  items: [],
  has_more: false,
}

export const webauthnCredentials = {
  items: [],
}

export const governanceGroups = {
  items: [],
  has_more: false,
}

/** ⚠ El motor contesta 501 `piv_not_configured` aquí y el mock SIEMPRE responde 200: un 501 no se
 *  puede reproducir con este arnés. Se sirve la forma honesta de lo que un navegador SIN mTLS
 *  produce —ningún certificado presentado—, que es un estado real del contrato, no uno inventado. */
export const pivStatus = { presented: false }

/** REAL, capturada de `GET /v1/m/compliance/frameworks` en un motor sembrado.
 *  Los 26 marcos van ENTEROS a propósito: la vista busca marcos concretos por `id`, así que una
 *  lista recortada devolvería `undefined` de un `find()` y volvería a caer — con la fixture
 *  puesta, que es peor que sin ella porque la tabla la contaría como cubierta. */
export const complianceFrameworks = {
  disclaimer:
    'Technical control-status mapping derived from observed platform evidence. NOT a certification and NOT legal advice (docs/08 \u00a79).',
  items: [
    {
      id: 'eu_ai_act',
      name: 'EU AI Act',
      version: 'Regulation (EU) 2024/1689',
      authority:
        'European Parliament and Council of the European Union (enforced via national market surveillance authorities and the European AI Office)',
      pin: {
        document: 'Regulation (EU) 2024/1689 (Artificial Intelligence Act)',
        published_on: '2024-07-12',
        source_url: 'https://eur-lex.europa.eu/eli/reg/2024/1689/oj',
        verified_on: '2026-06-10',
        status: 'in_force',
      },
      controls: 11,
    },
    {
      id: 'nist_ai_rmf',
      name: 'NIST AI Risk Management Framework (AI RMF 1.0)',
      version: 'NIST AI 100-1 (January 2023)',
      authority:
        'National Institute of Standards and Technology (NIST), U.S. Department of Commerce',
      pin: {
        document: 'NIST AI 100-1 (AI RMF 1.0)',
        published_on: '2023-01-26',
        source_url: 'https://nvlpubs.nist.gov/nistpubs/ai/NIST.AI.100-1.pdf',
        verified_on: '2026-06-10',
        status: 'final',
      },
      controls: 14,
    },
    {
      id: 'iso_42001',
      name: 'ISO/IEC 42001 \u2014 Information technology \u2014 Artificial intelligence \u2014 Management system',
      version: 'ISO/IEC 42001:2023 (Annex A)',
      authority:
        'ISO/IEC JTC 1/SC 42 (International Organization for Standardization / International Electrotechnical Commission)',
      pin: {
        document: 'ISO/IEC 42001:2023',
        published_on: '2023-12',
        source_url: 'https://www.iso.org/standard/81230.html',
        verified_on: '2026-06-10',
        status: 'final',
      },
      controls: 13,
    },
    {
      id: 'soc2_tsc',
      name: 'SOC 2 - Trust Services Criteria (Security / Common Criteria)',
      version:
        '2017 Trust Services Criteria with Revised Points of Focus - 2022',
      authority:
        'AICPA (American Institute of Certified Public Accountants) - Assurance Services Executive Committee',
      pin: {
        document:
          'TSP Section 100: 2017 Trust Services Criteria (With Revised Points of Focus \u2014 2022)',
        published_on: '2022',
        source_url:
          'https://www.aicpa-cima.com/resources/download/2017-trust-services-criteria-with-revised-points-of-focus-2022',
        verified_on: '2026-06-10',
        status: 'final',
      },
      controls: 13,
    },
    {
      id: 'iso_27001_2022',
      name: 'ISO/IEC 27001:2022 \u2014 Annex A controls',
      version:
        'ISO/IEC 27001:2022 (Annex A, aligned to ISO/IEC 27002:2022 control set)',
      authority:
        'International Organization for Standardization (ISO) / International Electrotechnical Commission (IEC)',
      pin: {
        document: 'ISO/IEC 27001:2022',
        published_on: '2022-10',
        source_url: 'https://www.iso.org/standard/27001',
        verified_on: '2026-06-10',
        status: 'final',
      },
      controls: 16,
    },
    {
      id: 'gdpr',
      name: 'General Data Protection Regulation (GDPR)',
      version: 'Regulation (EU) 2016/679',
      authority:
        'European Parliament and Council of the European Union (enforced by national Supervisory Authorities / EDPB)',
      pin: {
        document: 'Regulation (EU) 2016/679 (GDPR)',
        published_on: '2016-05-04',
        source_url: 'https://eur-lex.europa.eu/eli/reg/2016/679/oj',
        verified_on: '2026-06-10',
        status: 'in_force',
      },
      controls: 12,
    },
    {
      id: 'nis2',
      name: 'NIS 2 Directive',
      version: 'Directive (EU) 2022/2555',
      authority:
        "European Parliament and Council of the EU (enforced via national competent authorities and CSIRTs designated under each Member State's transposing law)",
      pin: {
        document: 'Directive (EU) 2022/2555 (NIS 2 Directive)',
        published_on: '2022-12-27',
        source_url: 'https://eur-lex.europa.eu/eli/dir/2022/2555/oj',
        verified_on: '2026-06-30',
        status: 'in_force',
      },
      controls: 13,
    },
    {
      id: 'eu_pld',
      name: 'EU Product Liability Directive (revised) \u2014 defense-evidence crosswalk',
      version: 'Directive (EU) 2024/2853',
      authority:
        'European Parliament and Council of the European Union (national transposition deadline lives in the regulatory calendar: milestone eu_pld.transposition_deadline)',
      pin: {
        document:
          'Directive (EU) 2024/2853 (revised Product Liability Directive)',
        published_on: '2024-11-18',
        source_url: 'https://eur-lex.europa.eu/eli/dir/2024/2853/oj',
        verified_on: '2026-06-10',
        status: 'in_force',
      },
      controls: 2,
    },
    {
      id: 'nist_ai_600_1',
      name: 'NIST AI RMF Generative AI Profile (AI 600-1)',
      version: 'NIST AI 600-1 (July 2024)',
      authority:
        'National Institute of Standards and Technology (NIST), U.S. Department of Commerce',
      pin: {
        document: 'NIST AI 600-1 (Generative AI Profile)',
        published_on: '2024-07-26',
        source_url: 'https://nvlpubs.nist.gov/nistpubs/ai/NIST.AI.600-1.pdf',
        verified_on: '2026-06-10',
        status: 'final',
      },
      controls: 12,
    },
    {
      id: 'csa_maestro',
      name: 'CSA MAESTRO \u2014 Agentic AI threat-modeling framework (7-layer)',
      version: 'Cloud Security Alliance, 2025-02-06',
      authority: 'Cloud Security Alliance (CSA)',
      pin: {
        document:
          'CSA MAESTRO (Multi-Agent Environment, Security, Threat, Risk, & Outcome)',
        published_on: '2025-02-06',
        source_url:
          'https://cloudsecurityalliance.org/blog/2025/02/06/agentic-ai-threat-modeling-framework-maestro',
        verified_on: '2026-06-10',
        status: 'guidance',
      },
      controls: 7,
    },
    {
      id: 'owasp_agentic_tm',
      name: 'OWASP Agentic AI \u2014 Threats and Mitigations (T1\u2013T15)',
      version: 'OWASP GenAI Security Project, v1.0 (2025-02-17)',
      authority:
        'OWASP GenAI Security Project \u2014 Agentic Security Initiative',
      pin: {
        document: 'Agentic AI \u2014 Threats and Mitigations, v1.0',
        published_on: '2025-02-17',
        source_url:
          'https://genai.owasp.org/resource/agentic-ai-threats-and-mitigations/',
        verified_on: '2026-06-10',
        status: 'guidance',
      },
      controls: 15,
    },
    {
      id: 'owasp_agentic_top10',
      name: 'OWASP Top 10 for Agentic Applications (2026)',
      version:
        'OWASP GenAI Security Project, Version 2026 (published 2025-12-09)',
      authority:
        'OWASP GenAI Security Project \u2014 Agentic Security Initiative',
      pin: {
        document: 'OWASP Top 10 For Agentic Applications 2026',
        published_on: '2025-12-09',
        source_url:
          'https://genai.owasp.org/resource/owasp-top-10-for-agentic-applications-for-2026/',
        verified_on: '2026-06-10',
        status: 'guidance',
      },
      controls: 10,
    },
    {
      id: 'cisa_agentic_adoption',
      name: 'Five Eyes \u2014 Careful Adoption of Agentic AI Services',
      version:
        'Joint guidance, 2026-05-01 (ASD ACSC, CISA, NSA, CCCS, NCSC-NZ, NCSC-UK)',
      authority:
        "Co-authored by ASD's ACSC (AU), CISA and NSA (US), the Canadian Centre for Cyber Security, NCSC-NZ and NCSC-UK",
      pin: {
        document: 'Careful adoption of agentic AI services',
        published_on: '2026-05-01',
        source_url:
          'https://www.cisa.gov/resources-tools/resources/careful-adoption-agentic-ai-services',
        verified_on: '2026-06-10',
        status: 'guidance',
      },
      controls: 5,
    },
    {
      id: 'cisa_ai_data_security',
      name: 'CISA/NSA/FBI \u2014 AI Data Security (joint CSI)',
      version:
        'Joint Cybersecurity Information Sheet, 2025-05-22 (U/OO/157249-25, Ver. 1.0)',
      authority:
        "NSA's Artificial Intelligence Security Center (AISC), CISA, FBI, ASD's ACSC, NCSC-NZ and NCSC-UK",
      pin: {
        document:
          'AI Data Security: Best Practices for Securing Data Used to Train & Operate AI Systems',
        published_on: '2025-05-22',
        source_url:
          'https://media.defense.gov/2025/May/22/2003720601/-1/-1/0/CSI_AI_DATA_SECURITY.PDF',
        verified_on: '2026-06-10',
        status: 'guidance',
      },
      controls: 10,
    },
    {
      id: 'csa_aicm',
      name: 'CSA AI Controls Matrix (AICM)',
      version:
        'Cloud Security Alliance \u2014 AI Controls Matrix v1.1 (bundle 2026-06)',
      authority: 'Cloud Security Alliance (CSA)',
      pin: {
        document:
          'CSA AI Controls Matrix (AICM) v1.1 \u2014 247 control objectives across 18 domains',
        published_on: '2026-06-22',
        source_url:
          'https://cloudsecurityalliance.org/artifacts/ai-controls-matrix-v1-1',
        verified_on: '2026-07-03',
        status: 'guidance',
      },
      controls: 18,
    },
    {
      id: 'llm_top10',
      name: 'OWASP Top 10 for LLM Applications 2025',
      version: 'OWASP GenAI Security Project \u2014 2025 list (doc v4.2.0a)',
      authority:
        'OWASP GenAI Security Project (Top 10 for LLM Applications team)',
      pin: {
        document:
          'OWASP Top 10 for Large Language Model Applications, 2025 (LLM01:2025\u2013LLM10:2025)',
        published_on: '2024-11-18',
        source_url: 'https://genai.owasp.org/llm-top-10/',
        verified_on: '2026-06-21',
        status: 'guidance',
      },
      controls: 10,
    },
    {
      id: 'mitre_atlas',
      name: 'MITRE ATLAS \u2014 adversarial technique coverage',
      version: 'MITRE ATLAS \u2014 atlas-data 2026.05 (data format v6.0.0)',
      authority: 'The MITRE Corporation',
      pin: {
        document:
          'MITRE ATLAS (Adversarial Threat Landscape for AI Systems), atlas-data release 2026.05',
        published_on: '2026-05-27',
        source_url: 'https://atlas.mitre.org/',
        verified_on: '2026-06-21',
        status: 'guidance',
      },
      controls: 9,
    },
    {
      id: 'nist_cosais',
      name: 'NIST SP 800-53 Control Overlays for Securing AI Systems (COSAiS) \u2014 design-toward',
      version:
        'NIST COSAiS \u2014 IN DEVELOPMENT (concept paper 2025-08, annotated outline 2026-01)',
      authority:
        'NIST (csrc.nist.gov/Projects/cosais); references the OpenID AI Identity Management (AIIM) Community Group',
      pin: {
        document:
          'NIST SP 800-53 Control Overlays for Securing AI Systems (COSAiS)',
        source_url: 'https://csrc.nist.gov/projects/cosais',
        verified_on: '2026-06-10',
        status: 'in_development',
      },
      controls: 4,
    },
    {
      id: 'tx_traiga',
      name: 'Texas Responsible AI Governance Act (TRAIGA)',
      version: '89(R) HB 149 (2025, signed 2025-06-22)',
      authority:
        'State of Texas (Attorney General exclusive enforcement; no private right of action)',
      pin: {
        document: 'Texas HB 149 (TRAIGA), 89th Legislature, Regular Session',
        published_on: '2025-06-22',
        source_url:
          'https://capitol.texas.gov/BillLookup/History.aspx?LegSess=89R&Bill=HB149',
        verified_on: '2026-06-28',
        status: 'in_force',
      },
      controls: 4,
    },
    {
      id: 'ca_sb53',
      name: 'California Frontier AI Safety Act (SB 53, TFAIA)',
      version: 'SB 53, Chapter 138 (2025, signed 2025-09-29)',
      authority:
        'State of California (AG exclusive enforcement; up to $1M per violation; no private right of action for main provisions; whistleblower PRA for retaliation under Labor Code \u00a71107)',
      pin: {
        document: 'California SB 53, Chapter 138 (Statutes of 2025)',
        published_on: '2025-09-29',
        source_url:
          'https://leginfo.legislature.ca.gov/faces/billTextClient.xhtml?bill_id=202520260SB53',
        verified_on: '2026-06-28',
        status: 'in_force',
      },
      controls: 5,
    },
    {
      id: 'il_hb3773',
      name: 'Illinois HB 3773 \u2014 AI in Employment (IHRA amendment)',
      version: 'HB 3773, Public Act 103-0804 (103rd GA, signed 2024-08-09)',
      authority:
        'Illinois Department of Human Rights (IDHR); AG for pattern-or-practice; private right of action via IHRA framework',
      pin: {
        document:
          'Illinois HB 3773 (Public Act 103-0804), amending 775 ILCS 5/ (Illinois Human Rights Act)',
        published_on: '2024-08-09',
        source_url:
          'https://www.ilga.gov/legislation/publicacts/fulltext.asp?Name=103-0804',
        verified_on: '2026-06-28',
        status: 'in_force',
      },
      controls: 3,
    },
    {
      id: 'co_sb26_189',
      name: 'Colorado ADMT Framework (SB 26-189)',
      version: 'SB 26-189 (2026, signed 2026-05-14)',
      authority:
        'State of Colorado (Attorney General enforcement, AG rulemaking authority)',
      pin: {
        document: 'Colorado SB 26-189 (Automated Decision-Making Technology)',
        published_on: '2026-05-14',
        source_url: 'https://leg.colorado.gov/bills/sb26-189',
        verified_on: '2026-06-28',
        status: 'in_force',
      },
      controls: 5,
    },
    {
      id: 'hipaa_clinical_ai',
      name: 'HIPAA Clinical AI Overlay',
      version: 'HIPAA (45 CFR Parts 160, 164) + HHS AI guidance (2024-2025)',
      authority:
        'U.S. Department of Health and Human Services (HHS), Office for Civil Rights (OCR)',
      pin: {
        document:
          'HIPAA Privacy/Security Rules (45 CFR Parts 160, 164) + HHS OCR AI Guidance',
        published_on: '1996-08-21',
        source_url: 'https://www.hhs.gov/hipaa/for-professionals/index.html',
        verified_on: '2026-06-28',
        status: 'in_force',
      },
      controls: 7,
    },
    {
      id: 'pci_dss_401_ai',
      name: 'PCI DSS 4.0.1 \u2014 AI in Cardholder Data Environments',
      version: 'PCI DSS v4.0.1 (June 2024)',
      authority: 'PCI Security Standards Council (PCI SSC)',
      pin: {
        document: 'PCI DSS v4.0.1',
        published_on: '2024-06',
        source_url:
          'https://www.pcisecuritystandards.org/document_library/?document=pci_dss',
        verified_on: '2026-07-05',
        status: 'final',
      },
      controls: 11,
    },
    {
      id: 'finra_genai',
      name: 'FINRA GenAI Supervision & Recordkeeping',
      version: 'FINRA Regulatory Notice 24-09 + existing rules',
      authority: 'Financial Industry Regulatory Authority (FINRA)',
      pin: {
        document: 'FINRA Regulatory Notice 24-09 (AI Governance)',
        published_on: '2024-06-27',
        source_url: 'https://www.finra.org/rules-guidance/notices/24-09',
        verified_on: '2026-06-28',
        status: 'guidance',
      },
      controls: 6,
    },
    {
      id: 'ferpa',
      name: 'FERPA \u2014 Education Records in AI',
      version: 'FERPA (20 U.S.C. \u00a71232g; 34 CFR Part 99)',
      authority:
        'U.S. Department of Education \u2014 Student Privacy Policy Office (SPPO); enforced by the Family Policy Compliance Office (FPCO)',
      pin: {
        document:
          'Family Educational Rights and Privacy Act \u2014 20 U.S.C. \u00a71232g; 34 CFR Part 99',
        published_on: '1974-08-21',
        source_url: 'https://www.ecfr.gov/current/title-34/part-99',
        verified_on: '2026-07-12',
        status: 'in_force',
      },
      controls: 7,
    },
  ],
}

/** REAL, capturada de `GET /v1/scim/v2/ServiceProviderConfig`. Es el descubrimiento SCIM 2.0
 *  (RFC 7643 §5), no un envoltorio de lista: la vista lee `cfg.patch.supported` directamente. */
export const scimServiceProviderConfig = {
  authenticationSchemes: [
    {
      description:
        'Authentication via a tenant-bound Olivares API token (Authorization: Bearer olvk_\u2026).',
      name: 'OAuth Bearer Token',
      primary: true,
      type: 'oauthbearertoken',
    },
  ],
  bulk: {
    maxOperations: 0,
    maxPayloadSize: 0,
    supported: false,
  },
  changePassword: {
    supported: false,
  },
  documentationUri: '',
  etag: {
    supported: false,
  },
  filter: {
    maxResults: 200,
    supported: true,
  },
  meta: {
    location: 'http://127.0.0.1:18103/v1/scim/v2/ServiceProviderConfig',
    resourceType: 'ServiceProviderConfig',
  },
  patch: {
    supported: true,
  },
  schemas: ['urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig'],
  sort: {
    supported: false,
  },
}

/** REAL, capturada de `GET /v1/m/compliance/frameworks/eu_ai_act/status`. Es un objeto
 *  `{ assessment, disclaimer }`, no una lista: la vista lee `res.assessment.framework` sin
 *  guardar, así que el envoltorio genérico `{ items: [] }` la tumbaba.
 *
 *  ⚠ Y ESTA RESPUESTA ES POR MARCO, así que servirla igual para los 26 sería una fixture que
 *  CONTESTA LO MISMO PARA CUALQUIER ENTRADA — la familia de defectos que más cara nos ha salido.
 *  `frameworkStatusFor()` reescribe `framework` con el id que viene en la ruta, de modo que la
 *  vista nunca pinta «EU AI Act» sobre una petición de NIS2. Lo que NO se reescribe son los
 *  controles: son los del marco capturado, y el arnés mide estructura y contraste, no contenido
 *  normativo. Dicho aquí para que nadie lea esta fixture como cobertura de los 26. */
export const frameworkStatus = {
  assessment: {
    framework: 'eu_ai_act',
    name: 'EU AI Act',
    version: 'Regulation (EU) 2024/1689',
    disclaimer:
      'Technical control mapping for an AI control plane only; not legal advice and not a certification or conformity assessment of compliance with Regulation (EU) 2024/1689.',
    summary: {
      total: 11,
      satisfied: 3,
      by_design: 0,
      partial: 8,
      gap: 0,
      unmapped: 0,
    },
    controls: [
      {
        control_id: 'art_5',
        title: 'Prohibited AI practices',
        requirement:
          'Prohibits placing on the market, putting into service, or using AI systems that engage in unacceptable-risk practices (e.g. manipulative/exploitative techniques, social scoring, untargeted facial-recognition scraping, certain biometric categorisation and real-time remote biometric identification).',
        criterion:
          'Evidence that each agent/system is risk-classified and screened so that prohibited-practice (unacceptable-risk) categories are flagged and blocked, with an inventory mapping every system to its tier.',
        status: 'partial',
        note: 'The adopted Digital Omnibus amending act, pending OJ publication, adds a NEW prohibited practice (NCII/CSAM generation) with its own compliance date \u2014 see the linked calendar milestones.',
        capabilities: [
          {
            key: 'risk_classification',
            class: 'operational',
            state: 'absent',
            detail: 'no agent risk classifications recorded for this tenant',
          },
          {
            key: 'transparency_record',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): A system/agent inventory and record-keeping surface is available.',
            refs: [
              {
                kind: 'design',
                detail: 'modules I/II + audit',
              },
            ],
          },
        ],
      },
      {
        control_id: 'art_6',
        title: 'Classification rules for high-risk AI systems',
        requirement:
          'Defines when an AI system is high-risk, including systems listed in Annex III, triggering the Chapter III Section 2 provider obligations.',
        criterion:
          'Each agent/system carries a recorded EU-AI-Act tier classification (incl. Annex III high-risk determination) maintained in an inventory.',
        status: 'partial',
        note: 'High-risk application timing is re-baselined by the adopted Digital Omnibus amending act, pending OJ publication: the in-force dates stand until publication \u2014 both sets live in the regulatory calendar, never as prose here.',
        capabilities: [
          {
            key: 'risk_classification',
            class: 'operational',
            state: 'absent',
            detail: 'no agent risk classifications recorded for this tenant',
          },
          {
            key: 'transparency_record',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): A system/agent inventory and record-keeping surface is available.',
            refs: [
              {
                kind: 'design',
                detail: 'modules I/II + audit',
              },
            ],
          },
        ],
      },
      {
        control_id: 'art_9',
        title: 'Risk management system',
        requirement:
          "Requires a continuous, iterative risk management system across the high-risk AI system's lifecycle: identifying, estimating, evaluating and mitigating risks, including through testing.",
        criterion:
          'Documented risk classifications plus adversarial/robustness testing, quality evaluation and threat-detection findings that evidence an iterative identify-test-mitigate loop over the system lifecycle.',
        status: 'partial',
        capabilities: [
          {
            key: 'risk_classification',
            class: 'operational',
            state: 'absent',
            detail: 'no agent risk classifications recorded for this tenant',
          },
          {
            key: 'adversarial_testing',
            class: 'operational',
            state: 'absent',
            detail: 'no red-team findings recorded for this tenant',
          },
          {
            key: 'quality_evaluation',
            class: 'operational',
            state: 'absent',
            detail: 'no eval results recorded',
          },
          {
            key: 'threat_detection',
            class: 'operational',
            state: 'present',
            detail: '4 security findings recorded',
            count: 4,
            refs: [
              {
                kind: 'entity',
                detail: 'finding',
              },
            ],
          },
        ],
      },
      {
        control_id: 'art_10',
        title: 'Data and data governance',
        requirement:
          'Requires training, validation and testing data to be subject to data governance and management practices appropriate to the purpose, including provenance, relevance, representativeness, bias examination and handling of personal data.',
        criterion:
          'Data-lineage evidence that client/training data stays within the defined perimeter, plus residency attestation, data-minimization by construction, and deterministic PII-discovery scans that surface personal data held in governed knowledge/document sources.',
        status: 'partial',
        note: 'The platform evidences perimeter/provenance, data minimization and personal-data discovery; dataset representativeness, bias examination/mitigation and statistical data quality are NOT evidenced by the control plane.',
        capabilities: [
          {
            key: 'data_lineage',
            class: 'operational',
            state: 'present',
            detail: '2 data-lineage records recorded',
            count: 2,
            refs: [
              {
                kind: 'entity',
                detail: 'knowledge.lineage',
              },
            ],
          },
          {
            key: 'data_residency',
            class: 'operational',
            state: 'absent',
            detail: 'no residency attestation recorded for this tenant',
          },
          {
            key: 'data_minimization',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): Only relations/metadata are persisted, never payloads or PII.',
            refs: [
              {
                kind: 'design',
                detail: 'docs/08 \u00a73',
              },
            ],
          },
          {
            key: 'pii_discovery',
            class: 'operational',
            state: 'absent',
            detail: 'no PII discovery scan runs recorded for this tenant',
          },
        ],
      },
      {
        control_id: 'art_12',
        title: 'Record-keeping',
        requirement:
          'Requires high-risk AI systems to technically allow automatic recording of events (logs) over their lifetime, with traceability appropriate to the intended purpose and supporting post-market monitoring.',
        criterion:
          "An append-only, hash-chained audit ledger with verified integrity, immutable by construction, exportable to WORM/SIEM and reconstructable for forensic timelines, plus per-inference resource accounting recorded over the system's operation.",
        status: 'satisfied',
        capabilities: [
          {
            key: 'audit_trail',
            class: 'operational',
            state: 'present',
            detail: 'audit ledger has 2 sealed events',
            count: 2,
            refs: [
              {
                kind: 'audit_chain',
                detail: 'head seq 2',
              },
            ],
          },
          {
            key: 'audit_integrity',
            class: 'operational',
            state: 'present',
            detail: 'hash-chain verified intact across 2 events',
            count: 2,
            refs: [
              {
                kind: 'audit_chain',
                detail: 'verify ok, checked 2',
              },
            ],
          },
          {
            key: 'audit_immutability',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): The ledger is append-only + hash-chained by construction.',
            refs: [
              {
                kind: 'design',
                detail: 'docs/08 \u00a75 contract \u00a75',
              },
            ],
          },
          {
            key: 'audit_export',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): Continuous WORM/SIEM export (CEF/syslog/OTLP) is available.',
            refs: [
              {
                kind: 'design',
                detail: 'Contract \u00a76',
              },
            ],
          },
          {
            key: 'forensic_capability',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): A reconstructable, integrity-verified incident timeline is available.',
            refs: [
              {
                kind: 'design',
                detail: 'module IX /',
              },
            ],
          },
          {
            key: 'access_observability',
            class: 'operational',
            state: 'present',
            detail: '13 access edges recorded',
            count: 13,
            refs: [
              {
                kind: 'entity',
                detail: 'access_edge',
              },
            ],
          },
          {
            key: 'resource_accounting',
            class: 'operational',
            state: 'present',
            detail:
              '6 resource-accounting records (token/compute/cost per call) recorded',
            count: 6,
            refs: [
              {
                kind: 'entity',
                detail: 'finops.cost_sample',
              },
            ],
          },
        ],
      },
      {
        control_id: 'art_13',
        title: 'Transparency and provision of information to deployers',
        requirement:
          'Requires high-risk AI systems to be sufficiently transparent for deployers to interpret and use outputs, accompanied by instructions for use detailing capabilities, limitations, performance and log-collection mechanisms.',
        criterion:
          'A maintained system/agent inventory and record-keeping surface that documents each system, its capabilities and its logging, supporting the information provided to deployers.',
        status: 'satisfied',
        capabilities: [
          {
            key: 'transparency_record',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): A system/agent inventory and record-keeping surface is available.',
            refs: [
              {
                kind: 'design',
                detail: 'modules I/II + audit',
              },
            ],
          },
          {
            key: 'audit_trail',
            class: 'operational',
            state: 'present',
            detail: 'audit ledger has 2 sealed events',
            count: 2,
            refs: [
              {
                kind: 'audit_chain',
                detail: 'head seq 2',
              },
            ],
          },
        ],
      },
      {
        control_id: 'art_14',
        title: 'Human oversight',
        requirement:
          'Requires high-risk AI systems to be designed so they can be effectively overseen by natural persons during use, including the ability to intervene, override or stop the system.',
        criterion:
          'Human-in-the-loop / approval gates that are deny-by-default, with the oversight actions and access governed and recorded.',
        status: 'satisfied',
        capabilities: [
          {
            key: 'human_oversight',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): HITL/approval gates, deny-by-default, are available.',
            refs: [
              {
                kind: 'design',
                detail: 'module VI /',
              },
            ],
          },
          {
            key: 'identity_governance',
            class: 'operational',
            state: 'present',
            detail: '3 identities and 0 policies governed',
            count: 3,
            refs: [
              {
                kind: 'entity',
                detail: 'identity, policy',
              },
            ],
          },
          {
            key: 'access_control_rbac',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): RBAC + fail-closed multi-tenant isolation enforced by the engine.',
            refs: [
              {
                kind: 'design',
                detail: 'Contract \u00a74,\u00a75',
              },
            ],
          },
        ],
      },
      {
        control_id: 'art_15',
        title: 'Accuracy, robustness and cybersecurity',
        requirement:
          'Requires high-risk AI systems to achieve appropriate levels of accuracy, robustness and cybersecurity and to perform consistently in those respects throughout their lifecycle, including resilience to errors and adversarial manipulation.',
        criterion:
          'Adversarial/robustness testing, quality evaluation, threat-detection findings, encryption in transit and secure defaults that evidence resilience and security posture.',
        status: 'partial',
        capabilities: [
          {
            key: 'adversarial_testing',
            class: 'operational',
            state: 'absent',
            detail: 'no red-team findings recorded for this tenant',
          },
          {
            key: 'quality_evaluation',
            class: 'operational',
            state: 'absent',
            detail: 'no eval results recorded',
          },
          {
            key: 'threat_detection',
            class: 'operational',
            state: 'present',
            detail: '4 security findings recorded',
            count: 4,
            refs: [
              {
                kind: 'entity',
                detail: 'finding',
              },
            ],
          },
          {
            key: 'encryption_transit',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): TLS \u22651.2 by default on every network listener (fail-closed, no plaintext fallback); automatic mutual TLS on the in-host connector channel; verified-client-cert mutual TLS available for remote collectors.',
            refs: [
              {
                kind: 'design',
                detail:
                  'docs/08 \u00a73; docs/SECURITY-HARDENING.md \u00a7transport contract \u00a711',
              },
            ],
          },
          {
            key: 'secure_defaults',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): No default credentials, single-use setup token, TLS on, localhost bind.',
            refs: [
              {
                kind: 'design',
                detail: 'Contract \u00a711',
              },
            ],
          },
          {
            key: 'supply_chain',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): Signed releases + SBOM + pinned, minimal dependencies.',
            refs: [
              {
                kind: 'design',
                detail: 'docs/08 \u00a77',
              },
            ],
          },
        ],
      },
      {
        control_id: 'art_50',
        title:
          'Transparency obligations for providers and deployers of certain AI systems',
        requirement:
          'Requires limited-risk transparency: informing natural persons they are interacting with an AI system and marking/disclosing AI-generated or manipulated content (e.g. deepfakes, synthetic media).',
        criterion:
          'Evidence that systems subject to Art. 50 disclosure are identified via risk classification and recorded in inventory.',
        status: 'partial',
        note: 'The platform evidences inventory/classification of in-scope systems; the substantive Art. 50 duties (end-user AI-interaction notices and synthetic-content watermarking) are not implemented and remain a gap. Application timing under the adopted Digital Omnibus amending act, pending OJ publication, lives in the linked calendar milestones.',
        capabilities: [
          {
            key: 'risk_classification',
            class: 'operational',
            state: 'absent',
            detail: 'no agent risk classifications recorded for this tenant',
          },
          {
            key: 'transparency_record',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): A system/agent inventory and record-keeping surface is available.',
            refs: [
              {
                kind: 'design',
                detail: 'modules I/II + audit',
              },
            ],
          },
        ],
      },
      {
        control_id: 'art_72',
        title:
          'Post-market monitoring by providers and post-market monitoring plan for high-risk AI systems',
        requirement:
          "Requires providers to establish and document a post-market monitoring system that actively and systematically collects, documents and analyses performance data over the system's lifetime to evaluate continued compliance.",
        criterion:
          'Continuous post-deployment telemetry: audit trail, change/deployment records, threat-detection and quality-evaluation findings, exportable for ongoing analysis.',
        status: 'partial',
        capabilities: [
          {
            key: 'audit_trail',
            class: 'operational',
            state: 'present',
            detail: 'audit ledger has 2 sealed events',
            count: 2,
            refs: [
              {
                kind: 'audit_chain',
                detail: 'head seq 2',
              },
            ],
          },
          {
            key: 'change_management',
            class: 'operational',
            state: 'absent',
            detail: 'no deployments recorded for this tenant',
          },
          {
            key: 'threat_detection',
            class: 'operational',
            state: 'present',
            detail: '4 security findings recorded',
            count: 4,
            refs: [
              {
                kind: 'entity',
                detail: 'finding',
              },
            ],
          },
          {
            key: 'quality_evaluation',
            class: 'operational',
            state: 'absent',
            detail: 'no eval results recorded',
          },
          {
            key: 'audit_export',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): Continuous WORM/SIEM export (CEF/syslog/OTLP) is available.',
            refs: [
              {
                kind: 'design',
                detail: 'Contract \u00a76',
              },
            ],
          },
          {
            key: 'access_observability',
            class: 'operational',
            state: 'present',
            detail: '13 access edges recorded',
            count: 13,
            refs: [
              {
                kind: 'entity',
                detail: 'access_edge',
              },
            ],
          },
        ],
      },
      {
        control_id: 'art_11',
        title: 'Technical documentation',
        requirement:
          'Requires technical documentation (per Annex IV) to be drawn up before a high-risk AI system is placed on the market and kept up to date, demonstrating compliance with Chapter III Section 2.',
        criterion:
          "Change-management/deployment records and a maintained system inventory that keep evidence of the system's state current and reconstructable, plus per-inference accounting of the computational resources used to operate the system (Annex IV(2)(c)).",
        status: 'partial',
        note: 'Annex IV(2)(c) requires documenting the computational resources USED to develop, train, test and validate the system; the control plane evidences the operational compute/cost accounting (resource_accounting), NOT training-time figures or dataset quality.',
        capabilities: [
          {
            key: 'change_management',
            class: 'operational',
            state: 'absent',
            detail: 'no deployments recorded for this tenant',
          },
          {
            key: 'transparency_record',
            class: 'architectural',
            state: 'present',
            detail:
              'platform guarantee by design (cited): A system/agent inventory and record-keeping surface is available.',
            refs: [
              {
                kind: 'design',
                detail: 'modules I/II + audit',
              },
            ],
          },
          {
            key: 'resource_accounting',
            class: 'operational',
            state: 'present',
            detail:
              '6 resource-accounting records (token/compute/cost per call) recorded',
            count: 6,
            refs: [
              {
                kind: 'entity',
                detail: 'finops.cost_sample',
              },
            ],
          },
        ],
      },
    ],
  },
  disclaimer:
    'Technical control-status mapping derived from observed platform evidence. NOT a certification and NOT legal advice (docs/08 \u00a79).',
}

export function frameworkStatusFor(id: string) {
  return {
    ...frameworkStatus,
    assessment: { ...frameworkStatus.assessment, framework: id },
  }
}

export function fixtureFor(pathname: string): unknown | null {
  if (pathname.endsWith('/v1/m/adoption/discrepancy'))
    return adoptionDiscrepancy
  if (pathname.endsWith('/v1/m/adoption/developers')) return adoptionDevelopers
  if (pathname.endsWith('/v1/m/adoption/teams')) return adoptionTeams
  if (pathname.endsWith('/v1/m/notify/match-types')) return notifyMatchTypes
  if (pathname.endsWith('/v1/m/finops/forecast')) return finopsForecastReal
  if (pathname.endsWith('/v1/m/finops/model-rates')) return finopsModelRates
  if (pathname.endsWith('/v1/auth/webauthn/credentials'))
    return webauthnCredentials
  if (pathname.endsWith('/v1/m/governance/groups')) return governanceGroups
  if (pathname.endsWith('/v1/auth/piv/status')) return pivStatus
  if (pathname.endsWith('/v1/m/compliance/frameworks'))
    return complianceFrameworks
  if (pathname.endsWith('/v1/scim/v2/ServiceProviderConfig'))
    return scimServiceProviderConfig
  // Antes que `/frameworks`, que es prefijo de esta ruta.
  const st = /\/v1\/m\/compliance\/frameworks\/([^/]+)\/status$/.exec(pathname)
  if (st) return frameworkStatusFor(st[1])
  // Executive dashboard sources (check the specific spend sub-paths before /spend).
  if (pathname.endsWith('/v1/m/finops/spend/summary')) return finopsSummary
  if (pathname.endsWith('/v1/m/finops/spend/trend')) return finopsTrend
  if (pathname.endsWith('/v1/m/finops/spend')) return finopsSpendTeam
  if (pathname.endsWith('/v1/m/finops/forecast')) return finopsForecast
  if (pathname.endsWith('/v1/m/models/models')) return modelsList
  if (pathname.endsWith('/v1/m/security/findings')) return securityFindings
  if (pathname.endsWith('/v1/m/redteam/runs')) return redteamRuns
  if (pathname.endsWith('/v1/m/compliance/summary')) return complianceSummary
  if (pathname.endsWith('/v1/m/compliance/risk')) return complianceRisk
  if (pathname.endsWith('/killswitch/state')) return killswitchState
  if (pathname.endsWith('/v1/m/eventing/dead-letters'))
    return eventingDeadLetters
  if (pathname.endsWith('/v1/m/eventing/deliveries')) return eventingDeliveries
  if (pathname.endsWith('/v1/m/eventing/subscriptions'))
    return eventingSubscriptions
  if (pathname.endsWith('/v1/m/eventing/events')) return eventingEvents
  if (pathname.endsWith('/v1/m/models/catalog')) return modelsCatalog
  if (pathname.endsWith('/ingestion-health'))
    return observabilityIngestionHealth
  if (pathname.endsWith('/v1/m/recording/notice')) return recordingsNotice
  if (pathname.endsWith('/v1/m/eventing/event-types')) return eventingTypes
  if (pathname.endsWith('/v1/m/eventing/egress-policy'))
    return eventingEgressPolicy
  if (pathname.endsWith('/v1/m/eventing/egress-policy/compat'))
    return eventingEgressCompat
  if (pathname.endsWith('/v1/m/recording/config')) return recordingsConfig
  if (pathname.endsWith('/v1/m/orchestration/graph')) return orchestrationGraph
  if (pathname.endsWith('/v1/m/observability/attestation'))
    return attestationRunning
  if (pathname.endsWith('/v1/m/notify/destinations')) return notifyDestinations
  if (pathname.endsWith('/v1/m/adoption/summary')) return adoptionSummaryReal
  if (pathname.endsWith('/v1/m/adoption/trend')) return adoptionTrend
  if (pathname.endsWith('/v1/m/identity/sso')) return identitySsoReal
  if (pathname.endsWith('/v1/m/governance/nhi/posture'))
    return governanceNhiPosture
  if (pathname.endsWith('/v1/server-info')) return serverInfo
  if (pathname.endsWith('/v1/auth/whoami')) return whoami
  if (pathname.endsWith('/v1/auth/refresh')) return refresh
  if (pathname.endsWith('/v1/m/accessmap/graph')) return accessGraph
  if (pathname.endsWith('/v1/m/accessmap/drift')) return accessDrift
  if (pathname.includes('/v1/m/accessmap/neighbors'))
    return { nodes: [], edges: [], has_more: false }
  if (pathname.endsWith('/v1/m/inventory/summary')) return inventorySummary
  if (pathname.endsWith('/v1/m/inventory/entities')) return inventoryEntities
  if (pathname.includes('/v1/m/inventory/entities/'))
    return {
      entry: inventoryEntities.items[0],
      detail: { kind: 'claude_code', identity_id: 'id-7' },
    }
  if (pathname.endsWith('/v1/m/sessions/live')) return sessionsLive
  if (pathname.includes('/timeline')) return sessionTimeline
  if (pathname.includes('/v1/m/sessions/live/')) return sessionsLive.items[0]
  if (pathname.endsWith('/v1/m/recording/sessions/sess-a11y/unified'))
    return sessionViewer
  if (pathname.endsWith('/v1/m/health/status')) return healthStatus
  if (pathname.endsWith('/v1/m/health/sla')) return healthSla
  if (pathname.endsWith('/v1/m/health/dependencies')) return healthDependencies
  if (pathname.endsWith('/v1/m/health/incidents')) return healthIncidents
  if (pathname.endsWith('/v1/m/health/events')) return healthEvents
  if (pathname.endsWith('/v1/m/health/checks')) return healthStatus
  // First-boot wizard (/onboarding).
  if (pathname.endsWith('/v1/console/setup-status')) return setupStatus
  if (pathname.endsWith('/v1/console/sources')) return consoleSources
  if (pathname.endsWith('/v1/console/connectors')) return consoleConnectors
  if (pathname.endsWith('/v1/workspaces')) return consoleWorkspaces
  if (pathname.endsWith('/v1/members')) return consoleMembers
  if (pathname.endsWith('/managed-settings/distribution'))
    return policyDistribution
  return null
}

export const eventingEgressPolicy = {
  in_force: false,
  mode: 'legacy_compat',
  classified_mode: 'legacy_compat',
  enforcement_committed: false,
  writer_fence: {
    armed: false,
    mode: 'legacy_compat',
    generation: 1,
    required_capability: 0,
    binary_capability: 1,
  },
  compat: {
    seeded: true,
    recorded: 0,
    still_needed: 0,
    intact: true,
    unparsable: 0,
  },
} satisfies EgressPolicyStatus

export const eventingEgressCompat = {
  seeded: true,
  intact: true,
  seeded_at: '2026-08-23T09:00:00Z',
  seed_digest:
    'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
  subscriptions: 0,
  unparsed: 0,
  authorities: [],
  still_needed: 0,
} satisfies EgressCompatReportDTO
