// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import type {
  EntityProvider,
  EntityProviderConnection,
} from '@backstage/plugin-catalog-node';
import type { Entity } from '@backstage/catalog-model';

/**
 * OlivaresEntityProvider publishes the Olivares AI estate INTO the customer's
 * Backstage Software Catalog. It is the developer-platform /
 * IDP half of the IaC integration suite.
 *
 * READ-FIRST BY CONSTRUCTION (docs/SECURITY-HARDENING.md). It reads the Olivares control-plane
 * inventory over the documented read-only REST surface (GET /v1/agents and GET
 * /v1/m/governance/identities) and SUPPLIES catalog entities to Backstage. It
 * never mutates Backstage configuration and never mutates the Olivares estate;
 * acting on either is out of scope (estate mutation is module VII / HITL-
 * gated). The only side effect is `connection.applyMutation`, which writes into
 * this provider's own catalog bucket — not into any external system.
 *
 * MINIMAL DATA (docs/SECURITY-HARDENING.md). The entities carry only non-sensitive structural
 * identity already published by the control plane (an agent's name/kind/status,
 * an identity's name). No token, payload, secret or PII is read or emitted: the
 * bearer token comes from plugin config and is sent only as an Authorization
 * header, never copied into an entity. See README "Minimal data".
 */
export class OlivaresEntityProvider implements EntityProvider {
  private connection?: EntityProviderConnection;

  /**
   * @param options - resolved plugin configuration and a scheduler hook.
   *   - baseUrl: the Olivares control-plane REST base (olivares.baseUrl), e.g.
   *     https://olivares.example.com. Required.
   *   - token: a bearer token (olivares.token), sent as `Authorization: Bearer`.
   *     Resolve it from an env placeholder in app-config (${OLIVARES_TOKEN});
   *     never inline the literal. Optional but the API requires auth.
   *   - tenant: an optional tenant reference (olivares.tenant) sent as the
   *     X-Olivares-Tenant header for multi-tenant deployments.
   *   - schedule: a TaskRunner the module wires from the SchedulerService; the
   *     provider does NOT own a timer (the same contract as a Go SourceConnector
   *     — scheduling is the host's job, sdk/connector.go). Required.
   *   - fetchImpl: an injectable fetch (tests); defaults to the global fetch.
   */
  constructor(
    private readonly options: {
      baseUrl: string;
      token?: string;
      tenant?: string;
      schedule: { run: (task: { id: string; fn: () => Promise<void> }) => Promise<void> };
      fetchImpl?: typeof fetch;
    },
  ) {}

  /**
   * getProviderName returns a stable, unique identifier for this provider
   * instance. Backstage namespaces this provider's catalog bucket by this
   * string, so it MUST be stable across restarts (changing it orphans the
   * previously published entities). Verified against backstage.io external-
   * integrations: the name must be unique + stable.
   */
  getProviderName(): string {
    return 'olivares-entity-provider';
  }

  /**
   * connect stores the catalog connection and schedules the refresh loop on the
   * injected TaskRunner. Backstage calls it once at catalog startup. We do not
   * fetch here: fetching is deferred to the scheduled run so a slow or
   * unreachable control plane never blocks catalog startup.
   */
  async connect(connection: EntityProviderConnection): Promise<void> {
    this.connection = connection;
    await this.options.schedule.run({
      id: this.getProviderName(),
      fn: async () => {
        await this.refresh();
      },
    });
  }

  /**
   * refresh reads the Olivares inventory and reconciles the catalog bucket with
   * a FULL mutation. We use 'full' (not 'delta'): a full inventory snapshot is a
   * complete reconciliation, so 'full' replaces this provider's bucket and lets
   * Backstage compute the add/update/remove delta itself (entities that vanished
   * upstream are removed — no stale-entity leak). 'delta' is for event-driven
   * upserts (a webhook receiving one changed agent); this provider is a poller,
   * so 'full' is the correct posture. Verified against backstage.io external-
   * integrations (applyMutation type 'full' vs 'delta').
   */
  async refresh(): Promise<void> {
    if (!this.connection) {
      throw new Error('olivares: connect() must be called before refresh()');
    }
    const [agents, identities] = await Promise.all([
      this.fetchAgents(),
      this.fetchIdentities(),
    ]);
    const entities = mapEstateToEntities({
      agents,
      identities,
      tenant: this.options.tenant,
      providerName: this.getProviderName(),
    });
    await this.connection.applyMutation({
      type: 'full',
      // locationKey ties every entity to this provider's source so Backstage can
      // resolve a conflict when another provider emits the same ref (it is an
      // opaque source identifier, not a URL). Verified against backstage.io.
      entities: entities.map(entity => ({
        entity,
        locationKey: `olivares-provider:${this.getProviderName()}`,
      })),
    });
  }

  /** fetchAgents reads GET /v1/agents (the agent roster). */
  private async fetchAgents(): Promise<AgentDTO[]> {
    const body = await this.getJSON('/v1/agents');
    return normalizeList<AgentDTO>(body);
  }

  /** fetchIdentities reads GET /v1/m/governance/identities (the identity roster). */
  private async fetchIdentities(): Promise<IdentityDTO[]> {
    const body = await this.getJSON('/v1/m/governance/identities');
    return normalizeList<IdentityDTO>(body);
  }

  /**
   * getJSON performs an authenticated GET against the control-plane REST surface.
   * The bearer token is sent ONLY as an Authorization header; the optional tenant
   * is sent as X-Olivares-Tenant. Neither ever enters an emitted entity.
   */
  private async getJSON(path: string): Promise<unknown> {
    const doFetch = this.options.fetchImpl ?? fetch;
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (this.options.token) {
      headers.Authorization = `Bearer ${this.options.token}`;
    }
    if (this.options.tenant) {
      headers['X-Olivares-Tenant'] = this.options.tenant;
    }
    const url = joinURL(this.options.baseUrl, path);
    const res = await doFetch(url, { method: 'GET', headers });
    if (!res.ok) {
      // The status line is non-sensitive; the body may not be, so it is not read.
      throw new Error(`olivares: GET ${path} failed: ${res.status} ${res.statusText}`);
    }
    return res.json();
  }
}

/**
 * AgentDTO is the subset of the control plane's GET /v1/agents item the provider
 * reads. Only these non-sensitive structural fields are mapped onto an entity;
 * any other field the API returns is ignored (minimal data, docs/SECURITY-HARDENING.md).
 */
export interface AgentDTO {
  id: string;
  name: string;
  kind?: string;
  status?: string;
  external_id?: string;
}

/**
 * IdentityDTO is the subset of GET /v1/m/governance/identities the provider
 * reads. Identities map to catalog Resources (the things agents act as / touch).
 */
export interface IdentityDTO {
  id: string;
  name: string;
  kind?: string;
}

/** The Backstage catalog API version this provider emits (alpha; see caveat). */
const CATALOG_API_VERSION = 'backstage.io/v1alpha1';

/** A safe DNS-ish entity name slug (Backstage names are constrained). */
function slug(value: string): string {
  const s = value
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63);
  return s || 'unknown';
}

/**
 * mapEstateToEntities turns the Olivares inventory into catalog entities. The
 * mapping (verified against backstage.io kind requirements):
 *   - the tenant/estate        -> System  (spec.owner)
 *   - each Olivares agent       -> Component(spec.type=service, lifecycle=production, owner)
 *   - each governance identity  -> Resource (spec.type=identity, owner)
 * Every entity is owned by `group:default/olivares` and (where the kind permits)
 * belongs to the estate System, so the catalog renders one coherent estate.
 */
export function mapEstateToEntities(input: {
  agents: AgentDTO[];
  identities: IdentityDTO[];
  tenant?: string;
  providerName: string;
}): Entity[] {
  const owner = 'group:default/olivares';
  const systemName = slug(input.tenant ? `olivares-${input.tenant}` : 'olivares-estate');
  const annotations = {
    // The provider annotation lets Backstage attribute the entity to its origin.
    'backstage.io/managed-by-origin-location': `url:olivares-provider:${input.providerName}`,
    'olivares.ai/managed': 'true',
  };

  const out: Entity[] = [];

  // The estate System: the umbrella every Olivares-managed entity belongs to.
  out.push({
    apiVersion: CATALOG_API_VERSION,
    kind: 'System',
    metadata: {
      name: systemName,
      annotations,
      description: 'Olivares AI control-plane estate.',
    },
    spec: { owner },
  });

  for (const a of input.agents) {
    if (!a || !a.name) {
      continue; // a stable name is required; never fabricate one.
    }
    out.push({
      apiVersion: CATALOG_API_VERSION,
      kind: 'Component',
      metadata: {
        name: slug(a.name),
        annotations: {
          ...annotations,
          ...(a.external_id ? { 'olivares.ai/external-id': a.external_id } : {}),
          ...(a.status ? { 'olivares.ai/status': a.status } : {}),
        },
        description: `Olivares agent${a.kind ? ` (${a.kind})` : ''}.`,
      },
      // Component requires type + lifecycle + owner (backstage.io).
      spec: {
        type: 'service',
        lifecycle: 'production',
        owner,
        system: systemName,
      },
    });
  }

  for (const i of input.identities) {
    if (!i || !i.name) {
      continue;
    }
    out.push({
      apiVersion: CATALOG_API_VERSION,
      kind: 'Resource',
      metadata: {
        name: slug(i.name),
        annotations,
        description: `Olivares governance identity${i.kind ? ` (${i.kind})` : ''}.`,
      },
      // Resource requires type + owner (backstage.io).
      spec: { type: i.kind || 'identity', owner, system: systemName },
    });
  }

  return out;
}

/**
 * normalizeList accepts either a bare JSON array or a wrapped `{ items: [...] }`
 * / `{ agents: [...] }` envelope and returns the array. It never invents items:
 * an unrecognized shape yields an empty list rather than a guess.
 */
function normalizeList<T>(body: unknown): T[] {
  if (Array.isArray(body)) {
    return body as T[];
  }
  if (body && typeof body === 'object') {
    const rec = body as Record<string, unknown>;
    for (const key of ['items', 'agents', 'identities', 'data']) {
      if (Array.isArray(rec[key])) {
        return rec[key] as T[];
      }
    }
  }
  return [];
}

/** joinURL concatenates a base and a path with exactly one separating slash. */
function joinURL(base: string, path: string): string {
  return `${base.replace(/\/+$/, '')}/${path.replace(/^\/+/, '')}`;
}
